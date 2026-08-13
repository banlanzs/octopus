package relay

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/transformer/model"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/looplj/axonhub/llm"
)

// ============================================================================
// 渠道协议自动检测（OutboundTypeAuto）
//
// 语义（对齐 ccload 的 protocolCandidatesForURL + ShouldFallbackProtocol）：
//   1. 客户端协议识别：路由层已确定入站协议（TextHandler 的 llm.APIFormat /
//      自研路径的 model.APIFormat），此处只做「客户端协议 → 出站协议」映射；
//   2. 候选生成：auto 渠道先用客户端协议请求（上游支持则按原逻辑直接透传，
//      字节级 passthrough 由各 outbound 的 PassthroughCapable 处理）；上游返回
//      「协议能力缺失」信号（400/403-CF 拦截页/404 非模型不可用/405）时，按
//      固定链 OpenAI Chat → Anthropic → OpenAI Response → Gemini 换协议重试
//      （跳过已试的客户端协议）；responses/embedding 请求族为单候选，不转换；
//   3. 学习缓存：成功一次即记住 (channel, baseURL, clientProtocol) → 上游协议
//      （不过期，进程重启或渠道配置变更时清除）；全部候选失败标记「不支持」
//      （TTL 10 分钟，到期后允许重新探测）；
//   4. 能力协商事件不记失败：不触发 Key/渠道熔断、不冷却、不计入统计/AutoRank/
//      outlier（仅审计 attempt span 记录）。
// ============================================================================

// 客户端协议标识（缓存键与候选生成的稳定标识，与具体 outbound 枚举解耦）。
const (
	clientProtocolAnthropic       = "anthropic"
	clientProtocolOpenAIChat      = "openai-chat"
	clientProtocolOpenAIResponse  = "openai-responses"
	clientProtocolOpenAIEmbedding = "openai-embedding"
)

// protocolUnsupportedTTL 「不支持」哨兵的存活时间：到期后允许重新探测。
const protocolUnsupportedTTL = 10 * time.Minute

// clientProtocolFromAxonFormat 将 axonhub 客户端格式映射为客户端协议标识。
func clientProtocolFromAxonFormat(format llm.APIFormat) (string, bool) {
	switch format {
	case llm.APIFormatAnthropicMessage:
		return clientProtocolAnthropic, true
	case llm.APIFormatOpenAIChatCompletion:
		return clientProtocolOpenAIChat, true
	case llm.APIFormatOpenAIResponse:
		return clientProtocolOpenAIResponse, true
	case llm.APIFormatOpenAIEmbedding:
		return clientProtocolOpenAIEmbedding, true
	default:
		return "", false
	}
}

// clientProtocolFromAPIFormat 将自研 transformer 客户端格式映射为客户端协议标识。
func clientProtocolFromAPIFormat(format model.APIFormat) (string, bool) {
	switch format {
	case model.APIFormatAnthropicMessage:
		return clientProtocolAnthropic, true
	case model.APIFormatOpenAIChatCompletion:
		return clientProtocolOpenAIChat, true
	case model.APIFormatOpenAIResponse:
		return clientProtocolOpenAIResponse, true
	case model.APIFormatOpenAIEmbedding:
		return clientProtocolOpenAIEmbedding, true
	default:
		return "", false
	}
}

// clientTypeOf 客户端协议标识 → 出站协议枚举。
func clientTypeOf(clientProtocol string) (outbound.OutboundType, bool) {
	switch clientProtocol {
	case clientProtocolAnthropic:
		return outbound.OutboundTypeAnthropic, true
	case clientProtocolOpenAIChat:
		return outbound.OutboundTypeOpenAIChat, true
	case clientProtocolOpenAIResponse:
		return outbound.OutboundTypeOpenAIResponse, true
	case clientProtocolOpenAIEmbedding:
		return outbound.OutboundTypeOpenAIEmbedding, true
	default:
		return 0, false
	}
}

// chatFamilyFallbackChain chat 请求族的协议兜底链（参考 ccload：OpenAI → Anthropic
// → Codex → Gemini；Codex 即 OpenAI Responses）。客户端协议恒在首位——
// 「网关内被调用到的渠道就用客户端请求的协议」，其余按固定顺序补位。
func chatFamilyFallbackChain(clientType outbound.OutboundType) []outbound.OutboundType {
	ordered := []outbound.OutboundType{
		outbound.OutboundTypeOpenAIChat,
		outbound.OutboundTypeAnthropic,
		outbound.OutboundTypeOpenAIResponse,
		outbound.OutboundTypeGemini,
	}
	chain := make([]outbound.OutboundType, 0, len(ordered)+1)
	chain = append(chain, clientType)
	for _, t := range ordered {
		if t != clientType {
			chain = append(chain, t)
		}
	}
	return chain
}

// protocolCandidates 返回该渠道本次请求「要按顺序尝试的上游协议列表」：
//   - 非 auto 渠道：只有声明的协议本身（权威配置，不探测）；
//   - auto 渠道 + 缓存命中 learned：只有已学到的协议（跳过客户端协议首探，
//     避免每次重复打不存在的端点）；
//   - auto 渠道 + 缓存命中 unsupported（TTL 内）：返回空，调用方直接跳过该渠道；
//   - auto 渠道无缓存：客户端协议优先 + 请求族兜底链（chat 族）或单候选
//     （responses / embedding 族）。
func protocolCandidates(channelType outbound.OutboundType, clientProtocol string, channelID int, baseURL string) []outbound.OutboundType {
	if !outbound.IsAutoType(channelType) {
		return []outbound.OutboundType{channelType}
	}

	if learned, unsupported, hit := lookupProtocolCapability(channelID, baseURL, clientProtocol); hit {
		if unsupported {
			return nil
		}
		return []outbound.OutboundType{learned}
	}

	clientType, ok := clientTypeOf(clientProtocol)
	if !ok {
		return nil
	}
	switch clientProtocol {
	case clientProtocolAnthropic, clientProtocolOpenAIChat:
		return chatFamilyFallbackChain(clientType)
	case clientProtocolOpenAIResponse:
		return []outbound.OutboundType{outbound.OutboundTypeOpenAIResponse}
	case clientProtocolOpenAIEmbedding:
		return []outbound.OutboundType{outbound.OutboundTypeOpenAIEmbedding}
	default:
		return nil
	}
}

// ============================================================================
// 协议能力缺失判定
// ============================================================================

// ShouldFallbackProtocol 判定一次失败是否为「协议能力缺失」（端点不存在 / 明确
// 不支持），命中即可安全地换一个协议重放请求。能力协商事件不记失败、不冷却。
//
// 判定表（对齐 ccload util/classifier.go）：
//   - 400：响应未提交时直接算（可安全重放到另一协议）；
//   - 403：仅当为 Cloudflare 拦截页；
//   - 405：算；
//   - 404：仅当非「模型不可用」（模型不存在是客户端/渠道配置问题，换协议无意义）；
//   - 其余状态码（401/429/5xx/连接错误等）不算——走正常的失败/冷却链路。
func ShouldFallbackProtocol(statusCode int, bodyText string, written bool) bool {
	if written {
		return false
	}
	switch statusCode {
	case 400:
		return true
	case 403:
		return isCloudflareBlockPage(bodyText)
	case 405:
		return true
	case 404:
		return !isModelUnavailableResponse(bodyText)
	default:
		return false
	}
}

// isModelUnavailableResponse 判断 404 响应体是否表明「模型不存在」（而非端点不存在）。
// 命中则视为客户端/渠道配置问题，不触发协议切换。
func isModelUnavailableResponse(bodyText string) bool {
	lower := strings.ToLower(bodyText)
	for _, marker := range []string{
		"does not exist",  // OpenAI: The model 'x' does not exist
		"model_not_found", // OpenAI error code
		"model not found", // Azure / 常见代理
		"not_found_error", // Anthropic error type（含模型名）
		"no such model",   // 部分兼容层
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// isCloudflareBlockPage 判断 403 响应体是否为 Cloudflare 拦截页。
// 拦截页意味着流量被 CF 风控拦截（非协议问题），不触发协议切换；若 403 来自
// 上游业务（如鉴权/配额），则正常走失败链路。
func isCloudflareBlockPage(bodyText string) bool {
	lower := strings.ToLower(bodyText)
	if strings.Contains(lower, "cf-ray") {
		return true
	}
	if strings.Contains(lower, "cloudflare") &&
		(strings.Contains(lower, "attention required") || strings.Contains(lower, "just a moment") || strings.Contains(lower, "challenge")) {
		return true
	}
	return false
}

// extractUpstreamStatusCode 从错误中提取上游 HTTP 状态码：
//   - 优先 httpclient.Error（axonhub 标准错误）；
//   - 其次解析 "upstream error: NNN:" 文本（anthropic passthrough 路径构造的
//     fmt.Errorf 错误，body 文本随错误消息带出）。
//
// 提取失败返回 0。仅用于 auto 渠道的能力缺失判定与日志，不影响非 auto 路径的
// 既有状态码语义。
func extractUpstreamStatusCode(err error) int {
	if sc := axonErrorStatusCode(err); sc != 0 {
		return sc
	}
	const prefix = "upstream error: "
	msg := err.Error()
	if !strings.HasPrefix(msg, prefix) {
		return 0
	}
	rest := msg[len(prefix):]
	if idx := strings.IndexByte(rest, ':'); idx > 0 {
		if sc, convErr := strconv.Atoi(rest[:idx]); convErr == nil {
			return sc
		}
	}
	return 0
}

// ============================================================================
// 协议能力学习缓存
// ============================================================================

// protocolUnsupported 哨兵值：表示该 (渠道, baseURL, 客户端协议) 的候选链全部
// 能力缺失。非哨兵值为学习到的上游协议。
const protocolUnsupported = -1

type protocolCapabilityEntry struct {
	learned    int       // 学习到的上游协议（protocolUnsupported = 不支持）
	recordedAt time.Time // 记录时间（unsupported 条目用于 TTL 判定）
}

// protocolCapabilityCache 进程内协议学习缓存：key = channelID|baseURL|clientProtocol。
// 成功条目不过期（进程重启或渠道配置变更时清除）；unsupported 哨兵 TTL 10 分钟。
var protocolCapabilityCache sync.Map

func protocolCapKey(channelID int, baseURL, clientProtocol string) string {
	return fmt.Sprintf("%d|%s|%s", channelID, baseURL, clientProtocol)
}

// rememberProtocolCapability 记录一次成功的协议学习（不过期）。
func rememberProtocolCapability(channelID int, baseURL, clientProtocol string, learned outbound.OutboundType) {
	protocolCapabilityCache.Store(protocolCapKey(channelID, baseURL, clientProtocol), protocolCapabilityEntry{
		learned:    int(learned),
		recordedAt: time.Now(),
	})
}

// rememberProtocolUnsupported 记录候选链全部能力缺失（TTL 10 分钟后允许重新探测）。
func rememberProtocolUnsupported(channelID int, baseURL, clientProtocol string) {
	protocolCapabilityCache.Store(protocolCapKey(channelID, baseURL, clientProtocol), protocolCapabilityEntry{
		learned:    protocolUnsupported,
		recordedAt: time.Now(),
	})
}

// lookupProtocolCapability 查询协议学习缓存。
// 返回 (learned, unsupported, hit)：hit=false 表示无缓存（需要探测）；
// unsupported=true 表示命中「不支持」哨兵且仍在 TTL 内（应跳过该渠道）。
func lookupProtocolCapability(channelID int, baseURL, clientProtocol string) (learned outbound.OutboundType, unsupported bool, hit bool) {
	v, ok := protocolCapabilityCache.Load(protocolCapKey(channelID, baseURL, clientProtocol))
	if !ok {
		return 0, false, false
	}
	entry := v.(protocolCapabilityEntry)
	if entry.learned == protocolUnsupported {
		if time.Since(entry.recordedAt) >= protocolUnsupportedTTL {
			// 过期：允许重新探测
			protocolCapabilityCache.Delete(protocolCapKey(channelID, baseURL, clientProtocol))
			return 0, false, false
		}
		return 0, true, true
	}
	return outbound.OutboundType(entry.learned), false, true
}

// clearProtocolCapabilityByChannel 清除某渠道的全部协议学习记录
// （渠道更新/删除时由 op 层回调触发，见 init 注册）。
func clearProtocolCapabilityByChannel(channelID int) {
	prefix := fmt.Sprintf("%d|", channelID)
	protocolCapabilityCache.Range(func(key, _ any) bool {
		if k, ok := key.(string); ok && strings.HasPrefix(k, prefix) {
			protocolCapabilityCache.Delete(k)
		}
		return true
	})
}

func init() {
	// 渠道更新/删除时清除该渠道的协议学习缓存（复用 op 的熔断/粘性重置钩子）。
	op.RegisterRelayBalancerStateReset(clearProtocolCapabilityByChannel)
}
