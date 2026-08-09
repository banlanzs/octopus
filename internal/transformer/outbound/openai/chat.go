package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/bestruirui/octopus/internal/transformer/model"
)

type ChatOutbound struct{}

// ChatCompletionsTool is the explicit OpenAI chat/completions wire tool payload.
// Keeping this separate from the shared model prevents provider-specific fields
// from leaking into the Chat request body.
type ChatCompletionsTool struct {
	Type     string         `json:"type,omitempty"`
	Function model.Function `json:"function,omitempty"`
}

// ChatCompletionsRequest is the explicit OpenAI chat/completions wire payload.
// Keeping this as a whitelist prevents internal/provider-specific fields on the
// shared InternalLLMRequest from leaking to OpenAI-compatible upstreams.
type ChatCompletionsRequest struct {
	Messages []model.Message `json:"messages"`
	Model    string          `json:"model"`

	FrequencyPenalty    *float64              `json:"frequency_penalty,omitempty"`
	Logprobs            *bool                 `json:"logprobs,omitempty"`
	MaxCompletionTokens *int64                `json:"max_completion_tokens,omitempty"`
	MaxTokens           *int64                `json:"max_tokens,omitempty"`
	PresencePenalty     *float64              `json:"presence_penalty,omitempty"`
	Seed                *int64                `json:"seed,omitempty"`
	Store               *bool                 `json:"store,omitempty"`
	Temperature         *float64              `json:"temperature,omitempty"`
	TopLogprobs         *int64                `json:"top_logprobs,omitempty"`
	TopP                *float64              `json:"top_p,omitempty"`
	LogitBias           map[string]int64      `json:"logit_bias,omitempty"`
	Metadata            map[string]string     `json:"metadata,omitempty"`
	Modalities          []string              `json:"modalities,omitempty"`
	Audio               *ChatCompletionsAudio `json:"audio,omitempty"`
	ReasoningEffort     string                `json:"reasoning_effort,omitempty"`
	Thinking            *model.ThinkingConfig `json:"thinking,omitempty"`
	ServiceTier         *string               `json:"service_tier,omitempty"`
	Stop                *model.Stop           `json:"stop,omitempty"`
	Stream              *bool                 `json:"stream,omitempty"`
	StreamOptions       *model.StreamOptions  `json:"stream_options,omitempty"`
	ParallelToolCalls   *bool                 `json:"parallel_tool_calls,omitempty"`
	Tools               []ChatCompletionsTool `json:"tools,omitempty"`
	ToolChoice          *model.ToolChoice     `json:"tool_choice,omitempty"`
	ResponseFormat      *model.ResponseFormat `json:"response_format,omitempty"`
	SafetyIdentifier    *string               `json:"safety_identifier,omitempty"`
	// PromptCacheKey mirrors the top-level model field. Only forwarded when
	// the client populated the field on the Chat entrypoint — Responses
	// inbound carries its own ResponsesPromptCacheKey pass-through that
	// stays isolated from this builder.
	PromptCacheKey *string `json:"prompt_cache_key,omitempty"`
	// User is OpenAI's legacy caller-supplied end-user identifier. OpenAI now
	// prefers `safety_identifier` + `prompt_cache_key`, but the field is still
	// accepted for backward compatibility; we forward it when the client sets
	// it so downstreams that key on `user` keep working.
	User *string `json:"user,omitempty"`
	// Verbosity is the gpt-5 detail-level knob ("low" | "medium" | "high").
	Verbosity *string `json:"verbosity,omitempty"`
	// Prediction forwards the OpenAI "predicted outputs" payload verbatim.
	Prediction json.RawMessage `json:"prediction,omitempty"`
	// WebSearchOptions configures the Chat Completions built-in web search
	// tool; kept as raw JSON for schema stability.
	WebSearchOptions json.RawMessage `json:"web_search_options,omitempty"`
}

type ChatCompletionsAudio struct {
	Format string `json:"format,omitempty"`
	Voice  string `json:"voice,omitempty"`
}

func (o *ChatOutbound) TransformRequest(ctx context.Context, request *model.InternalLLMRequest, baseUrl, key string) (*http.Request, error) {
	// DeepSeek thinking mode 要求多轮对话把 assistant 的 thinking 内容连同
	// signature 以 content[].thinking 块形式原样回传，否则上游 400
	// ("The content[].thinking in the thinking mode must be passed back to the API")。
	// 必须放在 ClearHelpFields 之前：ReasoningBlocks/ReasoningSignature 是
	// json:"-" 帮助字段，会被 ClearHelpFields 清空。
	materializeDeepSeekThinkingBlocks(request)
	request.ClearHelpFields()
	request.NormalizeMessages()
	request.FlattenUnsupportedBlocks(model.AlternationProviderOpenAI)
	// 非 DeepSeek 的 OpenAI 标准 Chat 不接受 thinking/redacted_thinking 块，
	// 剥离以免被上游拒绝。
	stripOpenAIUnsupportedThinkingBlocks(request)

	// developer role is preserved as-is on OpenAI outbound (O-L5). OpenAI
	// 2025+ model spec treats "developer" as the canonical instruction
	// role for reasoning models; the latest Chat Completions API accepts
	// it natively and silently maps "system" to "developer" on reasoning
	// models for backward compatibility. Previously we forced
	// developer → system which worked on gpt-4 / gpt-4o (where the two
	// are interchangeable) but lost the semantic distinction on gpt-5
	// reasoning models. Keep the original role so upstreams that depend
	// on it (and downstreams that replay it) see the caller's intent.
	// Ref: https://platform.openai.com/docs/api-reference/chat

	if request.Stream != nil && *request.Stream {
		if request.StreamOptions == nil {
			request.StreamOptions = &model.StreamOptions{IncludeUsage: true}
		} else if !request.StreamOptions.IncludeUsage {
			request.StreamOptions.IncludeUsage = true
		}
	}

	body, err := json.Marshal(buildChatCompletionsRequest(request))
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	applyOpenAIOrgProjectHeaders(req, request)

	parsedUrl, err := url.Parse(strings.TrimSuffix(baseUrl, "/"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse base url: %w", err)
	}
	parsedUrl.Path = parsedUrl.Path + "/chat/completions"
	req.URL = parsedUrl
	req.Method = http.MethodPost
	return req, nil
}

// applyOpenAIOrgProjectHeaders forwards the optional OpenAI-Organization and
// OpenAI-Project headers from TransformerMetadata. Both headers are scoped
// to multi-org / multi-project deployments where a single API key can hit
// several billing scopes; callers set the metadata keys upstream (in relay
// configuration or per-request overrides) and the outbound transformer
// blindly forwards the values. Empty / whitespace-only values are dropped
// so we don't emit header keys with blank values. O-M7.
// Ref: https://platform.openai.com/docs/api-reference/debugging-requests
func applyOpenAIOrgProjectHeaders(req *http.Request, request *model.InternalLLMRequest) {
	if req == nil || request == nil {
		return
	}
	if org := request.TransformerMetadataValue(model.TransformerMetadataOpenAIOrganization); org != "" {
		req.Header.Set("OpenAI-Organization", org)
	}
	if project := request.TransformerMetadataValue(model.TransformerMetadataOpenAIProject); project != "" {
		req.Header.Set("OpenAI-Project", project)
	}
}

func chatPromptCacheKey(request *model.InternalLLMRequest) *string {
	if request == nil {
		return nil
	}
	if request.PromptCacheKey != nil {
		return request.PromptCacheKey
	}
	key, _ := derivedAnthropicCacheMetadata(request)
	return key
}

func buildChatCompletionsRequest(request *model.InternalLLMRequest) *ChatCompletionsRequest {
	if request == nil {
		return &ChatCompletionsRequest{}
	}

	result := &ChatCompletionsRequest{
		Messages:            request.Messages,
		Model:               request.Model,
		FrequencyPenalty:    request.FrequencyPenalty,
		Logprobs:            request.Logprobs,
		MaxCompletionTokens: request.MaxCompletionTokens,
		MaxTokens:           request.MaxTokens,
		PresencePenalty:     request.PresencePenalty,
		Seed:                request.Seed,
		Store:               request.Store,
		Temperature:         request.Temperature,
		TopLogprobs:         request.TopLogprobs,
		TopP:                request.TopP,
		LogitBias:           request.LogitBias,
		Metadata:            request.Metadata,
		Modalities:          request.Modalities,
		ReasoningEffort:     request.ReasoningEffort,
		Thinking:            request.Thinking,
		ServiceTier:         request.ServiceTier,
		Stop:                request.Stop,
		Stream:              request.Stream,
		StreamOptions:       request.StreamOptions,
		ParallelToolCalls:   request.ParallelToolCalls,
		Tools:               convertToolsToChatCompletions(request.Tools),
		ToolChoice:          request.ToolChoice,
		ResponseFormat:      request.ResponseFormat,
		SafetyIdentifier:    request.SafetyIdentifier,
		PromptCacheKey:      chatPromptCacheKey(request),
		User:                request.User,
		Verbosity:           request.Verbosity,
		Prediction:          request.Prediction,
		WebSearchOptions:    request.WebSearchOptions,
	}

	// Anthropic 的 stop_sequences 语义比 Chat Completions 的 stop 更宽松
	// （Anthropic 只在模型明确输出该序列时截断，而 Chat 在子串层面进行匹配），
	// 透传会导致 Chat 上游在第一个分段（如 "\n\n"）就提前触发 finish_reason="stop"，
	// 表现为"回一句话就中断"。仅在 inbound 是 Anthropic 时剥离。
	if request.RawAPIFormat == model.APIFormatAnthropicMessage {
		result.Stop = nil
	}

	// 推理系列模型（o1/o3/o4/gpt-5）把 max_tokens 弃用，改为 max_completion_tokens。
	// 若上层只填了 MaxTokens，将其重定向到新字段，避免新模型在还没输出可见内容前
	// 就被 max_tokens 限额打断。
	if result.MaxCompletionTokens == nil && result.MaxTokens != nil && isReasoningChatModel(result.Model) {
		result.MaxCompletionTokens = result.MaxTokens
		result.MaxTokens = nil
	}

	if request.Audio != nil {
		result.Audio = &ChatCompletionsAudio{
			Format: request.Audio.Format,
			Voice:  request.Audio.Voice,
		}
	}

	// `thinking` 参数是 DeepSeek 特有（见 model.ThinkingConfig 注释）；OpenAI/xAI
	// 等 OpenAI 兼容上游不识别该字段。其余模型的思考意图通过 reasoning_effort
	// 表达：剥离 DeepSeek thinking 的同时，把 Anthropic 的 effort 值
	// （low/medium/high/xhigh/max）归一化为 OpenAI 兼容的 low/medium/high，
	// 避免不识别值被上游整体忽略而静默关闭思考（能力大减）。
	if !isDeepSeekTarget(request) {
		result.Thinking = nil
		result.ReasoningEffort = normalizeReasoningEffort(result.ReasoningEffort)
	}

	return result
}

// normalizeReasoningEffort 把 Anthropic 的 effort 值（low/medium/high/xhigh/max）
// 归一化为 OpenAI 兼容上游（o*/gpt-5/grok 等）识别的 low/medium/high。
// 未知值保守回退 high，保证思考不被削弱。
func normalizeReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "", "low", "medium", "high":
		return effort
	case "minimal":
		return "low"
	case "xhigh", "max":
		return "high"
	default:
		return "high"
	}
}

// isReasoningChatModel 判断 Chat Completions 端的模型是否属于推理系列
// （o1/o3/o4/gpt-5）。这些系列只接受 max_completion_tokens，旧的 max_tokens
// 会被 OpenAI 官方 API 直接拒绝或悄悄忽略。
func isReasoningChatModel(modelName string) bool {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if name == "" {
		return false
	}
	if strings.HasPrefix(name, "o1") || strings.HasPrefix(name, "o3") || strings.HasPrefix(name, "o4") {
		return true
	}
	if strings.HasPrefix(name, "gpt-5") {
		return true
	}
	return false
}

func convertToolsToChatCompletions(tools []model.Tool) []ChatCompletionsTool {
	result := make([]ChatCompletionsTool, 0, len(tools))
	for _, tool := range tools {
		if tool.Type != "function" {
			continue
		}
		result = append(result, ChatCompletionsTool{
			Type:     "function",
			Function: tool.Function,
		})
	}
	return result
}

func (o *ChatOutbound) TransformResponse(ctx context.Context, response *http.Response) (*model.InternalLLMResponse, error) {
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("response body is empty")
	}

	// 中转站可能以 200 状态码返回 {"error": {...}}：此时必须按失败处理，
	// 否则错误结果会被当作成功返回给客户端，且无法触发重试。
	// 与 TransformStream 保持一致，用独立的 ErrorDetail 探测结构解析。
	var errCheck struct {
		Error *model.ErrorDetail `json:"error"`
	}
	if err := json.Unmarshal(body, &errCheck); err == nil && errCheck.Error != nil && errCheck.Error.Message != "" {
		return nil, &model.ResponseError{
			StatusCode: response.StatusCode,
			Detail:     *errCheck.Error,
		}
	}

	var resp model.InternalLLMResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	return &resp, nil
}

func (o *ChatOutbound) TransformStream(ctx context.Context, eventData []byte) (*model.InternalLLMResponse, error) {
	if bytes.HasPrefix(eventData, []byte("[DONE]")) {
		return &model.InternalLLMResponse{
			Object: "[DONE]",
		}, nil
	}

	var errCheck struct {
		Error *model.ErrorDetail `json:"error"`
	}
	if err := json.Unmarshal(eventData, &errCheck); err == nil && errCheck.Error != nil {
		return nil, &model.ResponseError{
			Detail: *errCheck.Error,
		}
	}

	var resp model.InternalLLMResponse
	if err := json.Unmarshal(eventData, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal stream chunk: %w", err)
	}
	return &resp, nil
}

func (o *ChatOutbound) TransformStreamEvent(ctx context.Context, eventData []byte) ([]model.StreamEvent, error) {
	stream, err := o.TransformStream(ctx, eventData)
	if err != nil {
		return nil, err
	}
	return model.StreamEventsFromInternalResponse(stream), nil
}

// === DeepSeek thinking-mode replay helpers ===

// isDeepSeekModel 判断目标模型是否属于 DeepSeek 系列。
func isDeepSeekModel(modelName string) bool {
	name := strings.ToLower(strings.TrimSpace(modelName))
	return name != "" && strings.Contains(name, "deepseek")
}

// isDeepSeekTarget 判断目标是否按 DeepSeek thinking 语义处理：
// 模型名含 "deepseek"，或渠道显式开启 ForceDeepSeekThinking（中转站 DeepSeek 别名）。
func isDeepSeekTarget(request *model.InternalLLMRequest) bool {
	return request != nil && (isDeepSeekModel(request.Model) || request.ForceDeepSeekThinking)
}

// materializeDeepSeekThinkingBlocks 把 assistant 历史消息中的 thinking
// （ReasoningBlocks / ReasoningContent）重组为 content[].thinking 块回传。
// DeepSeek thinking mode 契约要求多轮对话时把 assistant 的 thinking 连同
// signature 原样回传，否则上游返回 400
// ("The content[].thinking in the thinking mode must be passed back to the API")。
// 若消息已携带 content[].thinking 块（客户端按契约回传），则原样保留。
func materializeDeepSeekThinkingBlocks(request *model.InternalLLMRequest) {
	if request == nil || !isDeepSeekTarget(request) {
		return
	}
	// 显式禁用 thinking 时无需（也不应）回传 thinking 块。
	if request.Thinking != nil && request.Thinking.Type == "disabled" {
		return
	}
	for i := range request.Messages {
		msg := &request.Messages[i]
		if msg.Role != "assistant" {
			continue
		}
		thinkingParts := deepSeekThinkingParts(msg)
		if len(thinkingParts) == 0 {
			// 消息无 reasoning 信息：保留现状。若客户端已按契约回传
			// content[].thinking 块（OpenAI round-trip 路径），字段保留即可。
			continue
		}
		parts := append(thinkingParts, deepSeekTextParts(msg)...)
		msg.Content = model.MessageContent{MultipleContent: parts}
		// thinking 以块形式回传后，不再输出顶层 reasoning_content，避免重复/歧义。
		msg.ReasoningContent = nil
		msg.Reasoning = nil
		msg.ReasoningSignature = nil
		msg.ReasoningBlocks = nil
	}
}

// deepSeekThinkingParts 从消息的 ReasoningBlocks / ReasoningContent 构建
// content[].thinking 与 redacted_thinking 块。
func deepSeekThinkingParts(msg *model.Message) []model.MessageContentPart {
	var parts []model.MessageContentPart
	for _, rb := range msg.ReasoningBlocks {
		switch rb.Kind {
		case model.ReasoningBlockKindThinking:
			parts = append(parts, model.MessageContentPart{
				Type:      "thinking",
				Thinking:  strOrNil(rb.Text),
				Signature: strOrNil(rb.Signature),
			})
		case model.ReasoningBlockKindRedacted:
			parts = append(parts, model.MessageContentPart{
				Type:             "redacted_thinking",
				RedactedThinking: strOrNil(rb.Data),
			})
		}
	}
	if msg.ReasoningContent != nil && *msg.ReasoningContent != "" {
		parts = append(parts, model.MessageContentPart{
			Type:      "thinking",
			Thinking:  msg.ReasoningContent,
			Signature: msg.ReasoningSignature,
		})
	}
	return parts
}

// deepSeekTextParts 提取消息原有文本/图片等内容作为 content 块。
func deepSeekTextParts(msg *model.Message) []model.MessageContentPart {
	var parts []model.MessageContentPart
	if msg.Content.Content != nil && *msg.Content.Content != "" {
		text := *msg.Content.Content
		parts = append(parts, model.MessageContentPart{Type: "text", Text: &text})
	}
	for _, p := range msg.Content.MultipleContent {
		switch p.Type {
		case "text", "image_url", "input_audio":
			parts = append(parts, p)
		}
	}
	return parts
}

// stripOpenAIUnsupportedThinkingBlocks 对非 DeepSeek 的 OpenAI Chat 出站剥离
// thinking/redacted_thinking 内容块（OpenAI 标准 Chat 不接受这些块类型）。
func stripOpenAIUnsupportedThinkingBlocks(request *model.InternalLLMRequest) {
	if request == nil || isDeepSeekTarget(request) {
		return
	}
	for i := range request.Messages {
		msg := &request.Messages[i]
		if len(msg.Content.MultipleContent) == 0 {
			continue
		}
		filtered := msg.Content.MultipleContent[:0]
		for _, p := range msg.Content.MultipleContent {
			if p.Type == "thinking" || p.Type == "redacted_thinking" {
				continue
			}
			filtered = append(filtered, p)
		}
		msg.Content.MultipleContent = filtered
	}
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
