package openai

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	inboundAnthropic "github.com/bestruirui/octopus/internal/transformer/inbound/anthropic"
	"github.com/bestruirui/octopus/internal/transformer/model"
)

// readRequestBody drains an http.Request body into bytes.
func readRequestBody(t *testing.T, r io.ReadCloser) []byte {
	t.Helper()
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("failed to read request body: %v", err)
	}
	return body
}

// TestClaudeCodeThinkingConfigReachesDeepSeek verifies that the thinking
// configuration Claude Code sends over the Anthropic protocol is translated
// into DeepSeek's OpenAI-compatible `thinking` parameter when octopus forwards
// through an OpenAI channel.
//
// Regression for the DeepSeek thinking-mode 400s:
//   - The `reasoning_content` in the thinking mode must be passed back to the API.
//   - The `content[].thinking` in the thinking mode must be passed back to the API.
func TestClaudeCodeThinkingConfigReachesDeepSeek(t *testing.T) {
	// Claude Code sends an Anthropic request with thinking enabled and a
	// budget. The Anthropic inbound should surface this as a DeepSeek
	// `thinking: {type: "enabled"}` on the OpenAI-compatible wire.
	anthropicBody := `{
		"model": "deepseek-chat",
		"max_tokens": 1024,
		"thinking": {"type": "enabled", "budget_tokens": 2048},
		"messages": [{"role": "user", "content": "hello"}]
	}`

	inbound := &inboundAnthropic.MessagesInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(anthropicBody))
	if err != nil {
		t.Fatalf("anthropic inbound failed: %v", err)
	}

	outbound := &ChatOutbound{}
	httpReq, err := outbound.TransformRequest(context.Background(), internalReq, "https://api.deepseek.com/v1", "test-key")
	if err != nil {
		t.Fatalf("openai outbound failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(readRequestBody(t, httpReq.Body), &payload); err != nil {
		t.Fatalf("failed to parse outgoing request: %v", err)
	}

	thinking, ok := payload["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking parameter lost during transformation - DeepSeek cannot enter thinking mode: %v", payload["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("expected thinking.type='enabled', got %q", thinking["type"])
	}
}

// TestClaudeCodeThinkingHistoryPassedBackToDeepSeek verifies that when Claude
// Code sends the assistant thinking block from a previous turn back in the
// history, octopus re-materializes it as a `content[].thinking` block (with
// signature) on the OpenAI-compatible wire — the contract DeepSeek's thinking
// mode requires for multi-turn replay
// ("The content[].thinking in the thinking mode must be passed back to the API").
func TestClaudeCodeThinkingHistoryPassedBackToDeepSeek(t *testing.T) {
	anthropicBody := `{
		"model": "deepseek-chat",
		"max_tokens": 1024,
		"thinking": {"type": "enabled", "budget_tokens": 2048},
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "let me think", "signature": "sig-123"},
				{"type": "text", "text": "hello to you"}
			]}
		]
	}`

	inbound := &inboundAnthropic.MessagesInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(anthropicBody))
	if err != nil {
		t.Fatalf("anthropic inbound failed: %v", err)
	}

	outbound := &ChatOutbound{}
	httpReq, err := outbound.TransformRequest(context.Background(), internalReq, "https://api.deepseek.com/v1", "test-key")
	if err != nil {
		t.Fatalf("openai outbound failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(readRequestBody(t, httpReq.Body), &payload); err != nil {
		t.Fatalf("failed to parse outgoing request: %v", err)
	}

	msgs, ok := payload["messages"].([]any)
	if !ok {
		t.Fatalf("messages field missing")
	}
	assistant := msgs[len(msgs)-1].(map[string]any)
	content, ok := assistant["content"].([]any)
	if !ok {
		t.Fatalf("expected content array with thinking block, got: %v", assistant)
	}
	sawThinking := false
	for _, c := range content {
		block, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] == "thinking" {
			sawThinking = true
			if block["thinking"] != "let me think" {
				t.Fatalf("thinking block text mismatch: %v", block)
			}
			if block["signature"] != "sig-123" {
				t.Fatalf("thinking block signature lost: %v", block)
			}
		}
	}
	if !sawThinking {
		t.Fatalf("content[].thinking not passed back to DeepSeek - assistant message: %v", assistant)
	}
}

// TestClaudeCodeAdaptiveThinkingToDeepSeek verifies Claude Code's adaptive
// thinking (output_config.effort) also maps to DeepSeek's enabled thinking.
func TestClaudeCodeAdaptiveThinkingToDeepSeek(t *testing.T) {
	anthropicBody := `{
		"model": "deepseek-chat",
		"max_tokens": 1024,
		"thinking": {"type": "adaptive", "budget_tokens": 2048},
		"output_config": {"effort": "high"},
		"messages": [{"role": "user", "content": "hello"}]
	}`

	inbound := &inboundAnthropic.MessagesInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(anthropicBody))
	if err != nil {
		t.Fatalf("anthropic inbound failed: %v", err)
	}

	outbound := &ChatOutbound{}
	httpReq, err := outbound.TransformRequest(context.Background(), internalReq, "https://api.deepseek.com/v1", "test-key")
	if err != nil {
		t.Fatalf("openai outbound failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(readRequestBody(t, httpReq.Body), &payload); err != nil {
		t.Fatalf("failed to parse outgoing request: %v", err)
	}

	thinking, ok := payload["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking parameter lost for adaptive thinking mode: %v", payload["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("expected adaptive thinking to map to thinking.type='enabled', got %q", thinking["type"])
	}
}

// TestClaudeCodeDisabledThinkingToDeepSeek verifies Claude Code explicitly
// disabling thinking maps to DeepSeek's disabled thinking parameter.
func TestClaudeCodeDisabledThinkingToDeepSeek(t *testing.T) {
	anthropicBody := `{
		"model": "deepseek-chat",
		"max_tokens": 1024,
		"thinking": {"type": "disabled"},
		"messages": [{"role": "user", "content": "hello"}]
	}`

	inbound := &inboundAnthropic.MessagesInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(anthropicBody))
	if err != nil {
		t.Fatalf("anthropic inbound failed: %v", err)
	}

	outbound := &ChatOutbound{}
	httpReq, err := outbound.TransformRequest(context.Background(), internalReq, "https://api.deepseek.com/v1", "test-key")
	if err != nil {
		t.Fatalf("openai outbound failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(readRequestBody(t, httpReq.Body), &payload); err != nil {
		t.Fatalf("failed to parse outgoing request: %v", err)
	}

	thinking, ok := payload["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking parameter lost for disabled thinking mode: %v", payload["thinking"])
	}
	if thinking["type"] != "disabled" {
		t.Fatalf("expected thinking.type='disabled', got %q", thinking["type"])
	}
}

// TestDeepSeekReasoningContentBecomesAnthropicThinking verifies the response
// direction: DeepSeek returns reasoning_content, octopus must surface it as an
// Anthropic thinking block so Claude Code stores it and passes it back next
// turn (otherwise DeepSeek returns 400 on the follow-up request).
func TestDeepSeekReasoningContentBecomesAnthropicThinking(t *testing.T) {
	inbound := &inboundAnthropic.MessagesInbound{}

	// Simulate an aggregated OpenAI Chat completion response from DeepSeek.
	internalResp := &model.InternalLLMResponse{
		ID:     "chatcmpl-1",
		Object: "chat.completion",
		Model:  "deepseek-chat",
		Choices: []model.Choice{{
			Index: 0,
			Message: &model.Message{
				Role:             "assistant",
				ReasoningContent: strPtr("let me think"),
				Content:          model.MessageContent{Content: strPtr("hello to you")},
			},
		}},
	}

	out, err := inbound.TransformResponse(context.Background(), internalResp)
	if err != nil {
		t.Fatalf("anthropic outbound response failed: %v", err)
	}

	var anthropicResp map[string]any
	if err := json.Unmarshal(out, &anthropicResp); err != nil {
		t.Fatalf("failed to parse anthropic response: %v", err)
	}

	content, ok := anthropicResp["content"].([]any)
	if !ok {
		t.Fatalf("anthropic response missing content: %v", anthropicResp)
	}
	sawThinking := false
	for _, blk := range content {
		block := blk.(map[string]any)
		if block["type"] == "thinking" {
			sawThinking = true
			if block["thinking"] != "let me think" {
				t.Fatalf("thinking block text mismatch: %v", block)
			}
		}
	}
	if !sawThinking {
		t.Fatalf("reasoning_content was not converted to an Anthropic thinking block: %v", content)
	}
}

// TestDeepSeekStreamingReasoningContentBecomesAnthropicThinking verifies the
// streaming response direction: DeepSeek's reasoning_content deltas are turned
// into Anthropic thinking_delta SSE so Claude Code stores the thinking and
// passes it back on the next turn.
func TestDeepSeekStreamingReasoningContentBecomesAnthropicThinking(t *testing.T) {
	inbound := &inboundAnthropic.MessagesInbound{}

	// One SSE chunk from DeepSeek with reasoning_content (no text yet).
	internalResp := &model.InternalLLMResponse{
		ID:     "chatcmpl-1",
		Object: "chat.completion.chunk",
		Model:  "deepseek-chat",
		Choices: []model.Choice{{
			Index: 0,
			Delta: &model.Message{
				Role:             "assistant",
				ReasoningContent: strPtr("let me think"),
			},
		}},
	}

	out, err := inbound.TransformStream(context.Background(), internalResp)
	if err != nil {
		t.Fatalf("anthropic inbound stream failed: %v", err)
	}

	text := string(out)
	for _, want := range []string{
		`"type":"content_block_start"`,
		`"type":"thinking"`,
		`"type":"thinking_delta"`,
		`"thinking":"let me think"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in stream, got:\n%s", want, text)
		}
	}
}

func strPtr(s string) *string { return &s }

// TestDeepSeekMinimalEffortNormalized verifies that an Anthropic adaptive
// thinking request carrying the "minimal" effort level (which Claude Code
// sends for subagent / classify calls) is normalized to "low" when forwarded
// to a DeepSeek v4 channel. DeepSeek only accepts low/medium/high/xhigh/max
// and rejects "minimal" with 400
// ("'reasoning_effort' must be one of: 'low', 'medium', 'high', 'xhigh', 'max'").
func TestDeepSeekMinimalEffortNormalized(t *testing.T) {
	anthropicBody := `{
		"model": "deepseek-v4-flash-0731",
		"max_tokens": 1024,
		"thinking": {"type": "adaptive"},
		"output_config": {"effort": "minimal"},
		"messages": [{"role": "user", "content": "classify this"}]
	}`

	inbound := &inboundAnthropic.MessagesInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(anthropicBody))
	if err != nil {
		t.Fatalf("anthropic inbound failed: %v", err)
	}

	outbound := &ChatOutbound{}
	httpReq, err := outbound.TransformRequest(context.Background(), internalReq, "https://api.deepseek.com/v1", "test-key")
	if err != nil {
		t.Fatalf("openai outbound failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(readRequestBody(t, httpReq.Body), &payload); err != nil {
		t.Fatalf("failed to parse outgoing request: %v", err)
	}
	if effort, ok := payload["reasoning_effort"].(string); !ok || effort != "low" {
		t.Fatalf("expected reasoning_effort normalized to 'low', got: %v", payload["reasoning_effort"])
	}
}

// TestDeepSeekUnknownEffortDropped verifies that an effort value outside the
// DeepSeek v4 whitelist is dropped entirely (field omitted) instead of being
// forwarded and rejected upstream.
func TestDeepSeekUnknownEffortDropped(t *testing.T) {
	anthropicBody := `{
		"model": "deepseek-v4-flash-0731",
		"max_tokens": 1024,
		"thinking": {"type": "adaptive"},
		"output_config": {"effort": "bogus"},
		"messages": [{"role": "user", "content": "hello"}]
	}`

	inbound := &inboundAnthropic.MessagesInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(anthropicBody))
	if err != nil {
		t.Fatalf("anthropic inbound failed: %v", err)
	}

	outbound := &ChatOutbound{}
	httpReq, err := outbound.TransformRequest(context.Background(), internalReq, "https://api.deepseek.com/v1", "test-key")
	if err != nil {
		t.Fatalf("openai outbound failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(readRequestBody(t, httpReq.Body), &payload); err != nil {
		t.Fatalf("failed to parse outgoing request: %v", err)
	}
	if _, ok := payload["reasoning_effort"]; ok {
		t.Fatalf("unknown reasoning_effort must be dropped, got: %v", payload["reasoning_effort"])
	}
}

// TestDeepSeekDisabledThinkingStillReplaysHistory verifies that a request with
// thinking.type=disabled (e.g. a Claude Code classify / subagent call sharing
// the conversation history) still passes back the assistant thinking blocks
// from earlier turns. DeepSeek requires full replay of thinking when the
// conversation carried tools ("content[].thinking must be passed back"), and
// silently ignores replayed thinking otherwise — so replay is always safe.
func TestDeepSeekDisabledThinkingStillReplaysHistory(t *testing.T) {
	anthropicBody := `{
		"model": "deepseek-v4-flash",
		"max_tokens": 1024,
		"thinking": {"type": "disabled"},
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "let me think", "signature": "sig-123"},
				{"type": "text", "text": "hello to you"}
			]}
		]
	}`

	inbound := &inboundAnthropic.MessagesInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(anthropicBody))
	if err != nil {
		t.Fatalf("anthropic inbound failed: %v", err)
	}

	outbound := &ChatOutbound{}
	httpReq, err := outbound.TransformRequest(context.Background(), internalReq, "https://api.deepseek.com/v1", "test-key")
	if err != nil {
		t.Fatalf("openai outbound failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(readRequestBody(t, httpReq.Body), &payload); err != nil {
		t.Fatalf("failed to parse outgoing request: %v", err)
	}

	msgs, ok := payload["messages"].([]any)
	if !ok {
		t.Fatalf("messages field missing")
	}
	assistant := msgs[len(msgs)-1].(map[string]any)
	content, ok := assistant["content"].([]any)
	if !ok {
		t.Fatalf("expected content array with thinking block, got: %v", assistant)
	}
	sawThinking := false
	for _, c := range content {
		block, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] == "thinking" {
			sawThinking = true
			if block["thinking"] != "let me think" {
				t.Fatalf("thinking block text mismatch: %v", block)
			}
			if block["signature"] != "sig-123" {
				t.Fatalf("thinking block signature lost: %v", block)
			}
		}
	}
	if !sawThinking {
		t.Fatalf("content[].thinking not passed back to DeepSeek despite disabled thinking - assistant message: %v", assistant)
	}
}

// TestDeepSeekArrayContentStreamSurvivesToAnthropic verifies that DeepSeek V4
// streaming chunks carrying content as a block array
// (delta.content = [{"type":"thinking","thinking":"...","signature":"..."},
//                   {"type":"text","text":"..."}]) survive the OpenAI→Anthropic
// stream conversion end-to-end. Regression: the array form was dropped
// entirely by StreamEventsFromInternalResponse, so Claude Code received a
// stream with no content blocks and failed with "API Error: Content block
// not found".
func TestDeepSeekArrayContentStreamSurvivesToAnthropic(t *testing.T) {
	chunks := []string{
		`{"id":"8b87f339","object":"chat.completion.chunk","created":0,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"role":"assistant","content":[{"type":"thinking","thinking":"thinking hard","signature":"sig-1"}]},"finish_reason":null}]}`,
		`{"id":"8b87f339","object":"chat.completion.chunk","created":0,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{"content":[{"type":"text","text":"<block>no"}]},"finish_reason":null}]}`,
		`{"id":"8b87f339","object":"chat.completion.chunk","created":0,"model":"deepseek-v4-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop_sequence","stop_sequence":"</block>"}]}`,
		`{"id":"8b87f339","object":"chat.completion.chunk","created":0,"model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":6062,"completion_tokens":7,"total_tokens":81717,"cache_read_input_tokens":75648}}`,
		`[DONE]`,
	}

	outbound := &ChatOutbound{}
	inbound := &inboundAnthropic.MessagesInbound{}
	var allSSE strings.Builder
	for _, chunk := range chunks {
		events, err := outbound.TransformStreamEvent(context.Background(), []byte(chunk))
		if err != nil {
			t.Fatalf("outbound failed for chunk %s: %v", chunk, err)
		}
		sse, err := inbound.TransformStreamEvents(context.Background(), events)
		if err != nil {
			t.Fatalf("inbound failed for chunk %s: %v", chunk, err)
		}
		allSSE.Write(sse)
	}
	text := allSSE.String()
	for _, want := range []string{
		`"type":"thinking_delta"`,
		`"thinking":"thinking hard"`,
		`"type":"signature_delta"`,
		`"signature":"sig-1"`,
		`"type":"text_delta"`,
		`"text":"\u003cblock\u003eno"`,
		`"stop_reason":"stop_sequence"`,
		`"stop_sequence":"\u003c/block\u003e"`,
		"event:message_stop",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in converted SSE, got:\n%s", want, text)
		}
	}
	// 文本块必须出现在流中（回归点：数组 content 曾整体丢失）
	if !strings.Contains(text, `\u003cblock\u003eno`) {
		t.Fatalf("array text block lost in stream conversion, got:\n%s", text)
	}
}

// TestDeepSeekThinkingReplayKeepsTopLevelReasoningContent verifies that
// DeepSeek thinking replay emits BOTH the content[].thinking block (required
// by DeepSeek V4 / some relays) and the top-level reasoning_content field
// (required by DeepSeek V3-compatible layers / other relays such as Console
// Go). Regression: the top-level field was cleared, so upstreams that require
// reasoning_content rejected the follow-up with
// "The reasoning_content in the thinking mode must be passed back to the API".
func TestDeepSeekThinkingReplayKeepsTopLevelReasoningContent(t *testing.T) {
	anthropicBody := `{
		"model": "deepseek-v4-flash",
		"max_tokens": 1024,
		"thinking": {"type": "enabled", "budget_tokens": 2048},
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "let me think", "signature": "sig-123"},
				{"type": "text", "text": "hello to you"}
			]}
		]
	}`

	inbound := &inboundAnthropic.MessagesInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(anthropicBody))
	if err != nil {
		t.Fatalf("anthropic inbound failed: %v", err)
	}

	outbound := &ChatOutbound{}
	httpReq, err := outbound.TransformRequest(context.Background(), internalReq, "https://api.deepseek.com/v1", "test-key")
	if err != nil {
		t.Fatalf("openai outbound failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(readRequestBody(t, httpReq.Body), &payload); err != nil {
		t.Fatalf("failed to parse outgoing request: %v", err)
	}

	msgs, ok := payload["messages"].([]any)
	if !ok {
		t.Fatalf("messages field missing")
	}
	assistant := msgs[len(msgs)-1].(map[string]any)

	// 顶层 reasoning_content 必须存在（V3 兼容上游 / Console Go 要求）。
	if rc, ok := assistant["reasoning_content"].(string); !ok || rc != "let me think" {
		t.Fatalf("expected top-level reasoning_content='let me think', got: %v", assistant["reasoning_content"])
	}

	// content[].thinking 块仍须存在（DeepSeek V4 / 部分中转站要求）。
	content, ok := assistant["content"].([]any)
	if !ok {
		t.Fatalf("expected content array with thinking block, got: %v", assistant)
	}
	sawThinking := false
	for _, c := range content {
		block, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] == "thinking" {
			sawThinking = true
			if block["thinking"] != "let me think" || block["signature"] != "sig-123" {
				t.Fatalf("thinking block content/signature mismatch: %v", block)
			}
		}
	}
	if !sawThinking {
		t.Fatalf("content[].thinking block lost, assistant message: %v", assistant)
	}

	// 顶层不应出现 reasoning_signature（DeepSeek 请求 schema 无此字段）。
	if _, exists := assistant["reasoning_signature"]; exists {
		t.Fatalf("unexpected top-level reasoning_signature: %v", assistant["reasoning_signature"])
	}
}
