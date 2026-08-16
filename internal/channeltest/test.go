// Package channeltest 提供管理面板使用的渠道测试能力：
// 按渠道声明的协议向真实上游发送一条极小非流式请求，返回状态码、耗时、
// 上游输出/错误与 token 用量，帮助用户在保存前验证渠道配置是否可用。
package channeltest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/transformer/model"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
)

const (
	// DefaultTimeout 单条测试请求的总超时。测试请求 max_tokens 很小，
	// 30s 对国内中转站也足够；超时按"测试失败"展示而不是挂住管理面板。
	DefaultTimeout = 30 * time.Second
	// maxTestOutputTokens 限制测试请求的上游输出，避免测试产生无谓成本。
	maxTestOutputTokens = int64(16)
	// maxResponseBody 测试结果需要回显给用户，限制读取长度防止大响应撑爆内存。
	maxResponseBody = 256 * 1024
)

// Result 是一次渠道测试的结构化结果，直接作为 /api/v1/channel/test 的 data 返回。
type Result struct {
	Success    bool         `json:"success"`
	StatusCode int          `json:"status_code"`
	DurationMS int64        `json:"duration_ms"`
	Model      string       `json:"model"`
	Protocol   string       `json:"protocol"`
	Output     string       `json:"output,omitempty"`
	Error      string       `json:"error,omitempty"`
	Usage      *model.Usage `json:"usage,omitempty"`
}

// Run 测试一个渠道配置。auto 渠道会按 OpenAI Chat → Anthropic → Gemini 的顺序
// 尝试，任一协议成功即返回成功；全部失败时返回最后一次尝试的失败信息。
func Run(ctx context.Context, channel *dbmodel.Channel) Result {
	startedAt := time.Now()
	if channel == nil {
		return fail(0, "", "unknown", startedAt, "channel is nil")
	}
	if strings.TrimSpace(channel.GetBaseUrl()) == "" {
		return fail(0, "", "unknown", startedAt, "base url is empty")
	}
	usedKey := channel.GetChannelKey()
	if strings.TrimSpace(usedKey.ChannelKey) == "" {
		return fail(0, "", "unknown", startedAt, "no enabled channel key")
	}
	modelName := firstModelName(channel.Model, channel.CustomModel)
	if modelName == "" {
		return fail(0, "", "unknown", startedAt, "channel has no model")
	}

	timeout := DefaultTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	testCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	candidates := testCandidates(channel.Type)
	var last Result
	for _, candidateType := range candidates {
		last = runOnce(testCtx, channel, usedKey.ChannelKey, candidateType, modelName, startedAt)
		if last.Success {
			return last
		}
		// 固定协议渠道不需要多协议回退；auto 渠道遇到网络错误（status=0）
		// 时通常意味着 base url 本身不可达，继续换协议只会得到同样的错误。
		if channel.Type != outbound.OutboundTypeAuto || last.StatusCode == 0 {
			break
		}
	}
	return last
}

// testCandidates 返回需要依次尝试的出站协议。
func testCandidates(channelType outbound.OutboundType) []outbound.OutboundType {
	if channelType == outbound.OutboundTypeAuto {
		return []outbound.OutboundType{
			outbound.OutboundTypeOpenAIChat,
			outbound.OutboundTypeAnthropic,
			outbound.OutboundTypeGemini,
		}
	}
	return []outbound.OutboundType{channelType}
}

func runOnce(ctx context.Context, channel *dbmodel.Channel, key string, channelType outbound.OutboundType, modelName string, startedAt time.Time) Result {
	protocol := protocolName(channelType)
	adapter := outbound.Get(channelType)
	if adapter == nil {
		return fail(0, modelName, protocol, startedAt, fmt.Sprintf("unsupported outbound type: %d", channelType))
	}

	internalRequest := buildTestInternalRequest(channelType, modelName)
	internalRequest.ForceDeepSeekThinking = forceDeepSeekThinking(channel)
	httpRequest, err := adapter.TransformRequest(ctx, internalRequest, channel.GetBaseUrl(), key)
	if err != nil {
		return fail(0, modelName, protocol, startedAt, fmt.Sprintf("failed to build request: %v", err))
	}

	applyCustomHeaders(httpRequest, channel.CustomHeader)
	// 与业务转发一致：避免 Go 默认 User-Agent 触发上游风控。
	if httpRequest.Header.Get("User-Agent") == "" {
		httpRequest.Header.Set("User-Agent", "")
	}
	if err := helper.ApplyParamOverride(httpRequest, channel.ParamOverride); err != nil {
		return fail(0, modelName, protocol, startedAt, fmt.Sprintf("failed to apply param override: %v", err))
	}

	httpClient, err := helper.ChannelHTTPClientWithContext(ctx, channel)
	if err != nil {
		return fail(0, modelName, protocol, startedAt, err.Error())
	}

	response, err := httpClient.Do(httpRequest)
	if err != nil {
		return fail(0, modelName, protocol, startedAt, err.Error())
	}
	defer response.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if readErr != nil {
		return fail(response.StatusCode, modelName, protocol, startedAt, fmt.Sprintf("failed to read response body: %v", readErr))
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fail(response.StatusCode, modelName, protocol, startedAt, upstreamErrorText(response.StatusCode, body))
	}

	// 复用出站 transformer 的 200+error 检测（中转站可能以 200 返回错误对象）。
	clone := &http.Response{
		StatusCode: response.StatusCode,
		Header:     response.Header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	internalResponse, transformErr := adapter.TransformResponse(ctx, clone)
	if transformErr != nil {
		return fail(response.StatusCode, modelName, protocol, startedAt, transformErr.Error())
	}
	if internalResponse == nil {
		return fail(response.StatusCode, modelName, protocol, startedAt, "empty upstream response")
	}
	if internalResponse.Error != nil {
		return fail(response.StatusCode, modelName, protocol, startedAt, internalResponse.Error.Error())
	}

	return Result{
		Success:    true,
		StatusCode: response.StatusCode,
		DurationMS: time.Since(startedAt).Milliseconds(),
		Model:      modelName,
		Protocol:   protocol,
		Output:     summarizeOutput(internalResponse),
		Usage:      internalResponse.Usage,
	}
}

func fail(statusCode int, modelName, protocol string, startedAt time.Time, message string) Result {
	return Result{
		Success:    false,
		StatusCode: statusCode,
		DurationMS: time.Since(startedAt).Milliseconds(),
		Model:      modelName,
		Protocol:   protocol,
		Error:      message,
	}
}

func buildTestInternalRequest(channelType outbound.OutboundType, modelName string) *model.InternalLLMRequest {
	stream := false
	text := "Reply with OK."
	maxTokens := maxTestOutputTokens
	maxCompletionTokens := maxTestOutputTokens
	ping := "channel test"

	switch channelType {
	case outbound.OutboundTypeOpenAIEmbedding:
		return &model.InternalLLMRequest{
			Model:        modelName,
			RawAPIFormat: model.APIFormatOpenAIEmbedding,
			EmbeddingInput: &model.EmbeddingInput{
				Single: &ping,
			},
		}
	case outbound.OutboundTypeOpenAIResponse:
		return &model.InternalLLMRequest{
			Model:               modelName,
			RawAPIFormat:        model.APIFormatOpenAIResponse,
			Messages:            []model.Message{{Role: "user", Content: model.MessageContent{Content: &text}}},
			Stream:              &stream,
			MaxCompletionTokens: &maxCompletionTokens,
		}
	case outbound.OutboundTypeAnthropic:
		return &model.InternalLLMRequest{
			Model:        modelName,
			RawAPIFormat: model.APIFormatAnthropicMessage,
			Messages:     []model.Message{{Role: "user", Content: model.MessageContent{Content: &text}}},
			Stream:       &stream,
			MaxTokens:    &maxTokens,
		}
	case outbound.OutboundTypeGemini:
		return &model.InternalLLMRequest{
			Model:        modelName,
			RawAPIFormat: model.APIFormatGeminiContents,
			Messages:     []model.Message{{Role: "user", Content: model.MessageContent{Content: &text}}},
			Stream:       &stream,
			MaxTokens:    &maxTokens,
		}
	case outbound.OutboundTypeVolcengine:
		return &model.InternalLLMRequest{
			Model:               modelName,
			RawAPIFormat:        model.APIFormatOpenAIChatCompletion,
			Messages:            []model.Message{{Role: "user", Content: model.MessageContent{Content: &text}}},
			Stream:              &stream,
			MaxCompletionTokens: &maxCompletionTokens,
		}
	default:
		// OpenAI Chat 与 auto 渠道的首个候选共用。
		return &model.InternalLLMRequest{
			Model:               modelName,
			RawAPIFormat:        model.APIFormatOpenAIChatCompletion,
			Messages:            []model.Message{{Role: "user", Content: model.MessageContent{Content: &text}}},
			Stream:              &stream,
			MaxCompletionTokens: &maxCompletionTokens,
		}
	}
}

func protocolName(channelType outbound.OutboundType) string {
	switch channelType {
	case outbound.OutboundTypeOpenAIChat:
		return "openai_chat"
	case outbound.OutboundTypeOpenAIResponse:
		return "openai_responses"
	case outbound.OutboundTypeAnthropic:
		return "anthropic"
	case outbound.OutboundTypeGemini:
		return "gemini"
	case outbound.OutboundTypeVolcengine:
		return "volcengine"
	case outbound.OutboundTypeOpenAIEmbedding:
		return "openai_embedding"
	default:
		return "unknown"
	}
}

func firstModelName(models ...string) string {
	for _, modelList := range models {
		for _, m := range strings.Split(modelList, ",") {
			if s := strings.TrimSpace(m); s != "" {
				return s
			}
		}
	}
	return ""
}

func forceDeepSeekThinking(channel *dbmodel.Channel) bool {
	if channel == nil {
		return false
	}
	if channel.ForceDeepSeekThinking {
		return true
	}
	base := strings.ToLower(strings.TrimSpace(channel.GetBaseUrl()))
	return strings.Contains(base, "deepseek.com") || strings.Contains(base, "deepseek.ai")
}

func applyCustomHeaders(request *http.Request, headers []dbmodel.CustomHeader) {
	if request == nil {
		return
	}
	for _, header := range headers {
		key := strings.TrimSpace(header.HeaderKey)
		if key == "" {
			continue
		}
		request.Header.Set(key, header.HeaderValue)
	}
}

func upstreamErrorText(statusCode int, body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return fmt.Sprintf("upstream returned HTTP %d", statusCode)
	}
	// 控制面板回显长度：保留足够上下文，但不把超大错误页全部带回。
	const maxErrorText = 8 * 1024
	if len(trimmed) > maxErrorText {
		trimmed = trimmed[:maxErrorText] + "...(truncated)"
	}
	return fmt.Sprintf("upstream error HTTP %d: %s", statusCode, trimmed)
}

func summarizeOutput(response *model.InternalLLMResponse) string {
	if response == nil {
		return ""
	}
	if len(response.EmbeddingData) > 0 {
		dimensions := len(response.EmbeddingData[0].Embedding.FloatArray)
		if dimensions == 0 && response.EmbeddingData[0].Embedding.Base64String != nil {
			dimensions = -1 // base64 编码时维度无法从向量长度直接得到
		}
		if dimensions < 0 {
			return fmt.Sprintf("embedding ok: %d vector(s), base64 encoded", len(response.EmbeddingData))
		}
		return fmt.Sprintf("embedding ok: %d vector(s), %d dimension(s)", len(response.EmbeddingData), dimensions)
	}
	var parts []string
	for _, choice := range response.Choices {
		if text := choiceMessageText(choice); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return "request ok (empty text output)"
}

func choiceMessageText(choice model.Choice) string {
	msg := choice.Message
	if msg == nil {
		msg = choice.Delta
	}
	if msg == nil {
		return ""
	}
	var parts []string
	if msg.Content.Content != nil && strings.TrimSpace(*msg.Content.Content) != "" {
		parts = append(parts, strings.TrimSpace(*msg.Content.Content))
	}
	for _, part := range msg.Content.MultipleContent {
		switch {
		case part.Text != nil && strings.TrimSpace(*part.Text) != "":
			parts = append(parts, strings.TrimSpace(*part.Text))
		case part.Thinking != nil && strings.TrimSpace(*part.Thinking) != "":
			parts = append(parts, "[thinking] "+strings.TrimSpace(*part.Thinking))
		}
	}
	if len(parts) == 0 {
		if reasoning := msg.GetReasoningContent(); reasoning != "" {
			parts = append(parts, "[reasoning] "+reasoning)
		}
	}
	if len(msg.ToolCalls) > 0 {
		parts = append(parts, fmt.Sprintf("[tool_calls: %d]", len(msg.ToolCalls)))
	}
	return strings.Join(parts, "\n")
}
