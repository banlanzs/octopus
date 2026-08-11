package relay

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/transformer/inbound"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
)

// TestAnthropicInboundThinkingReplayEmitsReasoningContent 复现真实场景:
// Claude Code 回传 assistant thinking 块 → Anthropic inbound → OpenAI outbound(DeepSeek)
// 断言出站请求体同时含 content[].thinking 块与顶层 reasoning_content。
func TestAnthropicInboundThinkingReplayEmitsReasoningContent(t *testing.T) {
	rawBody := []byte(`{
		"model":"deepseek-v4-flash","max_tokens":4096,
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"let me think carefully","signature":"sig-abc"},
				{"type":"text","text":"hi"}
			]}
		]
	}`)

	internalReq, err := inbound.Get(inbound.InboundTypeAnthropic).TransformRequest(context.Background(), rawBody)
	if err != nil {
		t.Fatalf("inbound transform failed: %v", err)
	}
	// 模拟 relay 设置渠道模型名
	internalReq.Model = "deepseek-v4-flash"

	out := outbound.Get(outbound.OutboundTypeOpenAIChat)
	httpReq, err := out.TransformRequest(context.Background(), internalReq, "https://api.deepseek.com/v1", "sk-test")
	if err != nil {
		t.Fatalf("outbound transform failed: %v", err)
	}
	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	t.Logf("outbound body: %s", body[:min(len(body), 2000)])
	if !strings.Contains(string(body), `"reasoning_content"`) {
		t.Fatalf("出站请求体缺少顶层 reasoning_content!")
	}
	if !strings.Contains(string(body), `"type":"thinking"`) {
		t.Fatalf("出站请求体缺少 content[].thinking 块!")
	}
	if !strings.Contains(string(body), `"signature":"sig-abc"`) {
		t.Fatalf("出站请求体缺少 signature!")
	}
}
