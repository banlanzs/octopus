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
// history, octopus preserves it as `reasoning_content` on the OpenAI-compatible
// wire so DeepSeek's thinking-mode contract holds.
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
	reasoning, ok := assistant["reasoning_content"].(string)
	if !ok || reasoning == "" {
		t.Fatalf("reasoning_content not passed back to DeepSeek - assistant message: %v", assistant)
	}
	if reasoning != "let me think" {
		t.Fatalf("reasoning_content mismatch: got %q, want %q", reasoning, "let me think")
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
