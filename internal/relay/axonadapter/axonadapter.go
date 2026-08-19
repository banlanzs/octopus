// Package axonadapter 提供 octopus 文本转发路径接入 axonhub/llm 所需的适配层：
//
//   - 本地数字渠道类型 (outbound.OutboundType) 与 axonhub 渠道类型标识 (llm.APIFormat) 的双向映射；
//   - 入口/出口 transformer 工厂（newInbound / newOutbound），收敛请求类型 × 渠道类型的兼容性判定。
//
// 本次迁移为「保守核心替换」：仅文本 HTTP 路径（chat/completions、messages、responses、embeddings）
// 使用本包；WebSocket、responses/compact、图片编辑、replay、route-learning 仍走自研 transformer。
//
// 关键取舍（相对 97d6a04 的差异）：
//   - Channel.Type 仍为数字枚举，运行时经 OutboundTypeToAPIFormat 映射，不触发 DB 字符串化迁移；
//   - doubao（火山引擎）在 llm 包中无独立 APIFormat 常量，沿用自定义 ChannelTypeDoubao 标识路由，
//     doubao transformer 对外暴露 openai/chat_completions 格式，但其行为包含火山特化处理。
package axonadapter

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/transformer"
	"github.com/looplj/axonhub/llm/transformer/anthropic"
	"github.com/looplj/axonhub/llm/transformer/doubao"
	"github.com/looplj/axonhub/llm/transformer/gemini"
	"github.com/looplj/axonhub/llm/transformer/openai"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
)

// ChannelTypeDoubao 标识本地 volcengine 渠道在 axonhub 侧的渠道类型。
// llm 包未为 doubao 定义 APIFormat 常量（doubao transformer 对外暴露
// openai/chat_completions 格式），此处自定义常量用于 NewOutbound 路由。
const ChannelTypeDoubao llm.APIFormat = "doubao"

// reasoningEffortDisabledSentinel 是 axonhub 的 anthropic inbound 在
// thinking.type == "disabled" 时写入 llm.Request.ReasoningEffort 的内部哨兵
// （axonhub llm/transformer/anthropic/inbound_convert.go）。它只表达「思考已
// 显式禁用」，不是任何上游 API 的合法取值。
const reasoningEffortDisabledSentinel = "none"

// SanitizeReasoningEffortForOutbound 按目标协议决定是否剥离 ReasoningEffort
// 的内部哨兵 "none"，返回可直接交给 outbound transformer 的请求。
//
// 各 outbound 对该哨兵的处理并不一致：
//   - anthropic：用它还原 thinking:{"type":"disabled"}——必须保留；
//   - gemini：用它填 thinking_level:"none"（Gemini 3.x 合法枚举）——必须保留；
//   - deepseek：自行清空后再发出——不受影响；
//   - openai chat / responses：原样写入 reasoning_effort 字段——会外发。
//
// 最后一种是缺陷来源：OpenAI 及多数兼容上游的枚举为
// low/medium/high/xhigh/max，收到 "none" 直接 400
// （"'reasoning_effort' must be one of: ..."）。更糟的是该 400 会被
// ShouldFallbackProtocol 当成协议能力缺失，于是同一个错误在每个候选协议上
// 重放一遍。因此仅对 OpenAI 系出站剥离。
//
// 剥离后「禁用思考」的意图仍由 TransformerMetadata 的 thinking 类型承载
// （DeepSeek 特化路径据此工作）；OpenAI 协议本身没有「禁用推理」的标准表达，
// 省略该字段即为正确行为。
//
// 入参始终不被修改：命中时返回浅拷贝，未命中时原样返回入参指针，
// 保证同一请求换协议重试时哨兵仍在。
func SanitizeReasoningEffortForOutbound(req *llm.Request, channelType llm.APIFormat) *llm.Request {
	if req == nil || req.ReasoningEffort != reasoningEffortDisabledSentinel {
		return req
	}
	if !leaksReasoningEffortSentinel(channelType) {
		return req
	}
	reqCopy := *req
	reqCopy.ReasoningEffort = ""
	return &reqCopy
}

// leaksReasoningEffortSentinel 标识哪些出站协议会把 ReasoningEffort 原样写入
// 上游请求体（即不消费 "none" 哨兵）。新增渠道类型时需同步评估其 outbound
// 是否消费该值，未评估的类型默认按「消费」处理，不做剥离。
func leaksReasoningEffortSentinel(channelType llm.APIFormat) bool {
	switch channelType {
	case llm.APIFormatOpenAIChatCompletion,
		llm.APIFormatOpenAIResponse,
		llm.APIFormatOpenAIEmbedding,
		ChannelTypeDoubao:
		return true
	default:
		return false
	}
}

// OutboundTypeToAPIFormat 将本地数字渠道类型映射为 axonhub 渠道类型标识。
// 返回的 ok 为 false 表示该渠道类型不在文本路径支持范围内。
func OutboundTypeToAPIFormat(t outbound.OutboundType) (llm.APIFormat, bool) {
	switch t {
	case outbound.OutboundTypeOpenAIChat:
		return llm.APIFormatOpenAIChatCompletion, true
	case outbound.OutboundTypeOpenAIResponse:
		return llm.APIFormatOpenAIResponse, true
	case outbound.OutboundTypeAnthropic:
		return llm.APIFormatAnthropicMessage, true
	case outbound.OutboundTypeGemini:
		return llm.APIFormatGeminiContents, true
	case outbound.OutboundTypeVolcengine:
		return ChannelTypeDoubao, true
	case outbound.OutboundTypeOpenAIEmbedding:
		return llm.APIFormatOpenAIEmbedding, true
	default:
		return "", false
	}
}

// NewInbound 按入口 API 格式返回 axonhub inbound transformer；不支持时返回 nil。
func NewInbound(format llm.APIFormat) transformer.Inbound {
	switch format {
	case llm.APIFormatOpenAIChatCompletion:
		return openai.NewInboundTransformer()
	case llm.APIFormatOpenAIResponse:
		return responses.NewInboundTransformer()
	case llm.APIFormatOpenAIEmbedding:
		return openai.NewEmbeddingInboundTransformer()
	case llm.APIFormatAnthropicMessage:
		return anthropic.NewInboundTransformer()
	default:
		return nil
	}
}

// NewOutbound 按请求类型 × 渠道类型返回 axonhub outbound transformer。
// 兼容性判定收敛于此（不再使用本地 IsChatChannelType/IsEmbeddingChannelType 二次拦截），
// 使 axonhub 已支持的组合（如 Gemini/Doubao 的 embedding）不被本地旧判断挡住。
func NewOutbound(channelType llm.APIFormat, requestType llm.RequestType, baseURL, key string) (transformer.Outbound, error) {
	switch requestType {
	case llm.RequestTypeEmbedding:
		switch channelType {
		case llm.APIFormatOpenAIChatCompletion, llm.APIFormatOpenAIResponse, llm.APIFormatOpenAIEmbedding:
			return openai.NewOutboundTransformer(baseURL, key)
		case llm.APIFormatGeminiContents:
			return gemini.NewOutboundTransformer(baseURL, key)
		case ChannelTypeDoubao:
			return doubao.NewOutboundTransformer(baseURL, key)
		default:
			return nil, fmt.Errorf("channel type %s is not compatible with %s request", channelType, requestType)
		}
	case llm.RequestTypeChat:
		switch channelType {
		case llm.APIFormatOpenAIChatCompletion:
			return openai.NewOutboundTransformer(baseURL, key)
		case llm.APIFormatOpenAIResponse:
			return responses.NewOutboundTransformer(baseURL, key)
		case llm.APIFormatAnthropicMessage:
			return anthropic.NewOutboundTransformer(baseURL, key)
		case llm.APIFormatGeminiContents:
			return gemini.NewOutboundTransformer(baseURL, key)
		case ChannelTypeDoubao:
			// volcengine（火山）渠道保留自研 Responses 协议语义：用 responses outbound
			// 而非 doubao 的 chat_completions；火山特有补偿（partial input / thinking）
			// 在 relay 层 applyVolcengineCompensation 补齐。
			return responses.NewOutboundTransformer(baseURL, key)
		default:
			return nil, fmt.Errorf("channel type %s is not compatible with %s request", channelType, requestType)
		}
	default:
		return nil, fmt.Errorf("request type %s is not supported by text relay", requestType)
	}
}
