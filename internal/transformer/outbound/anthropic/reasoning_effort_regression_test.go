package anthropic

import (
	"encoding/json"
	"testing"
)

// TestNormalizeRawReasoningEffortWithClaudeCodeShape 模拟 Claude Code 思考模式请求
// (thinking 对象 + system 数组 + messages 数组 + reasoning_effort 在末尾)
func TestNormalizeRawReasoningEffortWithClaudeCodeShape(t *testing.T) {
	raw := `{"model":"deepseek-v4-flash","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":1024},"system":[{"type":"text","text":"You are a helpful assistant"}],"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"thinking","thinking":"let me think","signature":"sig-1"},{"type":"text","text":"hi there"}]}],"reasoning_effort":"minimal","stop_sequences":[],"metadata":{"user_id":"u1"}}`

	got, err := normalizeRawReasoningEffort([]byte(raw))
	if err != nil {
		t.Fatalf("normalizeRawReasoningEffort() error = %v", err)
	}

	// 1. 输出必须是合法 JSON
	var payload map[string]any
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("输出 JSON 损坏: %v\n原始: %s\n输出: %s", err, raw, got)
	}

	// 2. reasoning_effort 应为 low
	if payload["reasoning_effort"] != "low" {
		t.Fatalf("reasoning_effort 未归一化: got %v\n输出: %s", payload["reasoning_effort"], got)
	}

	// 3. thinking 配置保留
	th, ok := payload["thinking"].(map[string]any)
	if !ok || th["type"] != "enabled" || th["budget_tokens"] != float64(1024) {
		t.Fatalf("thinking 配置被破坏: %v\n输出: %s", payload["thinking"], got)
	}

	// 4. messages 完整(assistant thinking 块 + signature)
	msgs, ok := payload["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages 被破坏: %v", payload["messages"])
	}
	asst := msgs[1].(map[string]any)
	blocks, ok := asst["content"].([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("assistant content 被破坏: %v", asst["content"])
	}
	tb := blocks[0].(map[string]any)
	if tb["type"] != "thinking" || tb["signature"] != "sig-1" || tb["thinking"] != "let me think" {
		t.Fatalf("assistant thinking 块被破坏: %v", tb)
	}
}
