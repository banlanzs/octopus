package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	inboundAnthropic "github.com/bestruirui/octopus/internal/transformer/inbound/anthropic"
	inboundOpenai "github.com/bestruirui/octopus/internal/transformer/inbound/openai"
	"github.com/bestruirui/octopus/internal/transformer/model"
)

// TestOpenAIChatThinkingBlockRoundTripToDeepSeek verifies that an OpenAI-chat
// client replaying a DeepSeek thinking-mode assistant message
// (content[].thinking + signature) keeps the block verbatim when forwarded to
// a DeepSeek channel — otherwise DeepSeek rejects the follow-up turn with
// "The content[].thinking in the thinking mode must be passed back to the API".
func TestOpenAIChatThinkingBlockRoundTripToDeepSeek(t *testing.T) {
	body := `{
		"model": "deepseek-chat",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "let me think", "signature": "sig-abc"},
				{"type": "text", "text": "hi"}
			]}
		]
	}`

	inbound := &inboundOpenai.ChatInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("inbound failed: %v", err)
	}

	outbound := &ChatOutbound{}
	httpReq, err := outbound.TransformRequest(context.Background(), internalReq, "https://api.deepseek.com/v1", "test-key")
	if err != nil {
		t.Fatalf("outbound failed: %v", err)
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
		t.Fatalf("expected content array, got: %v", assistant)
	}
	sawThinking := false
	for _, c := range content {
		block := c.(map[string]any)
		if block["type"] == "thinking" {
			sawThinking = true
			if block["thinking"] != "let me think" {
				t.Fatalf("thinking text mismatch: %v", block)
			}
			if block["signature"] != "sig-abc" {
				t.Fatalf("thinking signature lost: %v", block)
			}
		}
	}
	if !sawThinking {
		t.Fatalf("content[].thinking block lost in round-trip: %v", assistant)
	}
}

// TestNonDeepSeekStripsThinkingBlocks verifies that OpenAI-standard channels
// (non-DeepSeek) do not receive thinking/redacted_thinking content blocks,
// which OpenAI Chat Completions rejects.
func TestNonDeepSeekStripsThinkingBlocks(t *testing.T) {
	body := `{
		"model": "gpt-4o",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "let me think", "signature": "sig-abc"},
				{"type": "text", "text": "hi"}
			]}
		]
	}`

	inbound := &inboundOpenai.ChatInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("inbound failed: %v", err)
	}

	outbound := &ChatOutbound{}
	httpReq, err := outbound.TransformRequest(context.Background(), internalReq, "https://api.openai.com/v1", "test-key")
	if err != nil {
		t.Fatalf("outbound failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(readRequestBody(t, httpReq.Body), &payload); err != nil {
		t.Fatalf("failed to parse outgoing request: %v", err)
	}
	msgs := payload["messages"].([]any)
	assistant := msgs[len(msgs)-1].(map[string]any)
	content, ok := assistant["content"]
	if !ok {
		t.Fatalf("content missing, got: %v", assistant)
	}
	// 单 text 块会被折叠为纯字符串，多块时为数组；两者都不应含 thinking 块。
	switch c := content.(type) {
	case string:
		// OK — thinking block was stripped and text folded.
	case []any:
		for _, item := range c {
			if block, ok := item.(map[string]any); ok {
				if block["type"] == "thinking" || block["type"] == "redacted_thinking" {
					t.Fatalf("thinking block must be stripped for non-DeepSeek channel: %v", block)
				}
			}
		}
	default:
		t.Fatalf("unexpected content type %T: %v", content, content)
	}
}

// TestNonDeepSeekStripsThinkingParameterKeepsReasoning verifies that the
// DeepSeek-specific `thinking` parameter is NOT forwarded to non-DeepSeek
// OpenAI-compatible upstreams (e.g. xAI grok), while the thinking intent is
// preserved through a normalized `reasoning_effort` (Anthropic "max" →
// "high") so the upstream model still reasons. Stripping thinking without
// keeping reasoning would cripple the model.
func TestNonDeepSeekStripsThinkingParameterKeepsReasoning(t *testing.T) {
	anthropicBody := `{
		"model": "grok-4.5",
		"max_tokens": 64000,
		"thinking": {"type": "adaptive"},
		"output_config": {"effort": "max"},
		"messages": [{"role": "user", "content": "hello"}]
	}`

	inbound := &inboundAnthropic.MessagesInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(anthropicBody))
	if err != nil {
		t.Fatalf("anthropic inbound failed: %v", err)
	}

	outbound := &ChatOutbound{}
	httpReq, err := outbound.TransformRequest(context.Background(), internalReq, "https://api.x.ai/v1", "test-key")
	if err != nil {
		t.Fatalf("openai outbound failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(readRequestBody(t, httpReq.Body), &payload); err != nil {
		t.Fatalf("failed to parse outgoing request: %v", err)
	}
	if _, ok := payload["thinking"]; ok {
		t.Fatalf("thinking parameter must be stripped for non-DeepSeek channel, got: %v", payload["thinking"])
	}
	if effort, ok := payload["reasoning_effort"].(string); !ok || effort != "high" {
		t.Fatalf("expected reasoning_effort normalized to 'high', got: %v", payload["reasoning_effort"])
	}
}

// TestForceDeepSeekThinkingChannelKeepsThinking verifies that a channel with
// ForceDeepSeekThinking enabled (relay-station DeepSeek alias whose model name
// does NOT contain "deepseek") still keeps DeepSeek thinking semantics — the
// content[].thinking blocks are NOT stripped.
func TestForceDeepSeekThinkingChannelKeepsThinkingBlocks(t *testing.T) {
	body := `{
		"model": "ds-relay-v1",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "let me think", "signature": "sig-abc"},
				{"type": "text", "text": "hi"}
			]}
		]
	}`

	inbound := &inboundOpenai.ChatInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("inbound failed: %v", err)
	}
	internalReq.ForceDeepSeekThinking = true

	outbound := &ChatOutbound{}
	httpReq, err := outbound.TransformRequest(context.Background(), internalReq, "https://relay.example.com/v1", "test-key")
	if err != nil {
		t.Fatalf("outbound failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(readRequestBody(t, httpReq.Body), &payload); err != nil {
		t.Fatalf("failed to parse outgoing request: %v", err)
	}
	msgs := payload["messages"].([]any)
	assistant := msgs[len(msgs)-1].(map[string]any)
	content, ok := assistant["content"].([]any)
	if !ok {
		t.Fatalf("expected content array, got: %v", assistant)
	}
	sawThinking := false
	for _, c := range content {
		block := c.(map[string]any)
		if block["type"] == "thinking" {
			sawThinking = true
			if block["signature"] != "sig-abc" {
				t.Fatalf("signature lost: %v", block)
			}
		}
	}
	if !sawThinking {
		t.Fatalf("content[].thinking block lost despite ForceDeepSeekThinking: %v", assistant)
	}
}

// TestForceDeepSeekThinkingKeepsThinkingParam verifies that an Anthropic
// thinking request forwarded to an alias model channel with ForceDeepSeekThinking
// keeps the DeepSeek `thinking` parameter on the wire.
func TestForceDeepSeekThinkingKeepsThinkingParam(t *testing.T) {
	anthropicBody := `{
		"model": "ds-relay-v1",
		"max_tokens": 1024,
		"thinking": {"type": "adaptive"},
		"output_config": {"effort": "max"},
		"messages": [{"role": "user", "content": "hello"}]
	}`

	inbound := &inboundAnthropic.MessagesInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(anthropicBody))
	if err != nil {
		t.Fatalf("anthropic inbound failed: %v", err)
	}
	internalReq.ForceDeepSeekThinking = true

	outbound := &ChatOutbound{}
	httpReq, err := outbound.TransformRequest(context.Background(), internalReq, "https://relay.example.com/v1", "test-key")
	if err != nil {
		t.Fatalf("openai outbound failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(readRequestBody(t, httpReq.Body), &payload); err != nil {
		t.Fatalf("failed to parse outgoing request: %v", err)
	}
	if _, ok := payload["thinking"]; !ok {
		t.Fatalf("thinking parameter must be kept when ForceDeepSeekThinking is enabled, got: %v", payload["thinking"])
	}
}

func TestNormalizeReasoningEffort(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"low":     "low",
		"medium":  "medium",
		"high":    "high",
		"max":     "high",
		"xhigh":   "high",
		"minimal": "low",
		"MAX":     "high",
		"unknown": "high",
	}
	for in, want := range cases {
		if got := normalizeReasoningEffort(in); got != want {
			t.Fatalf("normalizeReasoningEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestChatTransformResponse200ErrorBody verifies that a 200 response carrying
// an {"error": {...}} body (common from aggregators) is treated as failure so
// the relay can retry another channel instead of returning the error to the
// client as a success.
func TestChatTransformResponse200ErrorBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"message":"insufficient quota","type":"insufficient_quota","code":"insufficient_quota"}}`)),
	}
	outbound := &ChatOutbound{}
	_, err := outbound.TransformResponse(context.Background(), resp)
	if err == nil {
		t.Fatal("expected error for 200 + error body")
	}
	var re *model.ResponseError
	if !errors.As(err, &re) {
		t.Fatalf("expected ResponseError, got %T", err)
	}
	if re.StatusCode != http.StatusOK {
		t.Fatalf("expected StatusCode 200, got %d", re.StatusCode)
	}
	if re.Detail.Message != "insufficient quota" {
		t.Fatalf("unexpected error detail: %+v", re.Detail)
	}
}

// TestChatTransformResponseSuccessNoError ensures a genuine 200 response is
// still parsed as success.
func TestChatTransformResponseSuccessNoError(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)),
	}
	outbound := &ChatOutbound{}
	internalResp, err := outbound.TransformResponse(context.Background(), resp)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if internalResp == nil || len(internalResp.Choices) == 0 {
		t.Fatalf("expected parsed response, got: %+v", internalResp)
	}
}

// TestDeepSeekOpenAIRoundTripReplayKeepsReasoningContent verifies that when an
// OpenAI-compatible client replays a DeepSeek thinking-mode assistant message
// already carrying content[].thinking blocks (OpenAI round-trip path), the
// top-level reasoning_content field is still backfilled — upstreams that
// require reasoning_content (DeepSeek V3-compatible layers / some relays)
// otherwise reject the follow-up with
// "The reasoning_content in the thinking mode must be passed back to the API".
func TestDeepSeekOpenAIRoundTripReplayKeepsReasoningContent(t *testing.T) {
	body := `{
		"model": "deepseek-v4-flash",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "let me think", "signature": "sig-abc"},
				{"type": "text", "text": "hi"}
			]}
		]
	}`

	inbound := &inboundOpenai.ChatInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(body))
	if err != nil {
		t.Fatalf("inbound failed: %v", err)
	}

	outbound := &ChatOutbound{}
	httpReq, err := outbound.TransformRequest(context.Background(), internalReq, "https://api.deepseek.com/v1", "test-key")
	if err != nil {
		t.Fatalf("outbound failed: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(readRequestBody(t, httpReq.Body), &payload); err != nil {
		t.Fatalf("failed to parse outgoing request: %v", err)
	}
	msgs := payload["messages"].([]any)
	assistant := msgs[len(msgs)-1].(map[string]any)

	if rc, ok := assistant["reasoning_content"].(string); !ok || rc != "let me think" {
		t.Fatalf("expected top-level reasoning_content backfilled from content[].thinking, got: %v", assistant["reasoning_content"])
	}

	content, ok := assistant["content"].([]any)
	if !ok {
		t.Fatalf("expected content array, got: %v", assistant)
	}
	sawThinking := false
	for _, c := range content {
		if block, ok := c.(map[string]any); ok && block["type"] == "thinking" {
			sawThinking = true
			if block["thinking"] != "let me think" || block["signature"] != "sig-abc" {
				t.Fatalf("thinking block mismatch: %v", block)
			}
		}
	}
	if !sawThinking {
		t.Fatalf("content[].thinking block lost, assistant message: %v", assistant)
	}
}

