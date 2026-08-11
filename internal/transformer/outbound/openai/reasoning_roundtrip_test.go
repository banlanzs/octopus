package openai

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/transformer/inbound"
	"github.com/bestruirui/octopus/internal/transformer/model"
)

// TestOpenAIClientThinkingReplayEmitsReasoningContent 复现真实场景:
// OpenAI 格式客户端(assistant 历史带 content[].thinking 块 + tool_use 块)
// → OpenAI outbound(DeepSeek target)
// 断言出站请求体含顶层 reasoning_content(DeepSeek/Console Go 契约)。
func TestOpenAIClientThinkingReplayEmitsReasoningContent(t *testing.T) {
	rawBody := []byte(`{
		"model":"deepseek-v4-flash",
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"let me think","signature":"sig-1"},
				{"type":"text","text":"let me check"},
				{"type":"tool_use","id":"toolu_01A","name":"Bash","input":{"command":"ls"}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01A","content":"done"}]}
		]
	}`)

	internalReq, err := inbound.Get(inbound.InboundTypeOpenAIChat).TransformRequest(context.Background(), rawBody)
	if err != nil {
		t.Fatalf("inbound failed: %v", err)
	}
	internalReq.Model = "deepseek-v4-flash"

	out := &ChatOutbound{}
	httpReq, err := out.TransformRequest(context.Background(), internalReq, "https://api.deepseek.com/v1", "sk-test")
	if err != nil {
		t.Fatalf("outbound failed: %v", err)
	}
	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	t.Logf("body: %s", body)

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("出站请求体 JSON 不完整: %v", err)
	}
	if !strings.Contains(string(body), `"reasoning_content"`) {
		t.Fatalf("出站请求体缺少顶层 reasoning_content!(len=%d)", len(body))
	}
}

// TestOpenAIClientThinkingReplayNoTools 无 tool_use 的对照组
func TestOpenAIClientThinkingReplayNoTools(t *testing.T) {
	rawBody := []byte(`{
		"model":"deepseek-v4-flash",
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"let me think","signature":"sig-1"},
				{"type":"text","text":"hi"}
			]}
		]
	}`)

	internalReq, err := inbound.Get(inbound.InboundTypeOpenAIChat).TransformRequest(context.Background(), rawBody)
	if err != nil {
		t.Fatalf("inbound failed: %v", err)
	}
	internalReq.Model = "deepseek-v4-flash"

	out := &ChatOutbound{}
	httpReq, err := out.TransformRequest(context.Background(), internalReq, "https://api.deepseek.com/v1", "sk-test")
	if err != nil {
		t.Fatalf("outbound failed: %v", err)
	}
	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	if !strings.Contains(string(body), `"reasoning_content"`) {
		t.Fatalf("无 tools 场景出站也缺少 reasoning_content!(len=%d)", len(body))
	}
}

var _ = model.Message{}
