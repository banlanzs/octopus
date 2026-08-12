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

// TestDeepSeekToolCallWithoutThinkingGetsPlaceholder 回归：历史中"做了工具调用但无
// thinking 内容"的 assistant 消息（思考关闭时生成的轮次），OpenAI outbound 必须
// 补 content[].thinking 占位块 + 顶层 reasoning_content，否则 DeepSeek 400
// ("The reasoning_content in the thinking mode must be passed back to the API")。
// 与 Anthropic 透传路径 ensureDeepSeekThinkingReplay 同源问题。
func TestDeepSeekToolCallWithoutThinkingGetsPlaceholder(t *testing.T) {
	rawBody := []byte(`{
		"model":"deepseek-v4-flash",
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"let me think","signature":"sig-1"},
				{"type":"text","text":"let me check"},
				{"type":"tool_use","id":"toolu_01A","name":"Bash","input":{"command":"ls"}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01A","content":"done"}]},
			{"role":"assistant","content":"let me try again",
			 "tool_calls":[{"id":"toolu_02B","type":"function","function":{"name":"Grep","arguments":"{\"p\":\"x\"}"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_02B","content":"nothing"}]}
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
	msgs := payload["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("messages 数量被破坏: %d", len(msgs))
	}

	// 第 2 个 assistant（无 thinking 但有 tool_calls）必须补上占位块
	second := msgs[3].(map[string]any)
	content, ok := second["content"].([]any)
	if !ok {
		t.Fatalf("期望 content 数组含占位 thinking 块, got: %v", second["content"])
	}
	sawThinking := false
	for _, c := range content {
		blk, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if blk["type"] == "thinking" {
			sawThinking = true
			if sig, _ := blk["signature"].(string); sig == "" {
				t.Fatalf("占位 thinking 块缺少 signature: %v", blk)
			}
		}
	}
	if !sawThinking {
		t.Fatalf("tool_calls assistant 未补 content[].thinking 占位块: %v", content)
	}
	// 顶层 reasoning_content 必须存在（omitempty 会省略空串，因此必须非空）
	if rc, ok := second["reasoning_content"].(string); !ok || rc == "" {
		t.Fatalf("顶层 reasoning_content 缺失: %v", second["reasoning_content"])
	}
	// 原 tool_calls 必须保留
	if tc, ok := second["tool_calls"].([]any); !ok || len(tc) == 0 {
		t.Fatalf("tool_calls 丢失: %v", second["tool_calls"])
	}
}

// TestDeepSeekToolCallWithoutThinkingNoDuplicate 已有 thinking 的 assistant 不被重复补块。
func TestDeepSeekToolCallWithoutThinkingNoDuplicate(t *testing.T) {
	rawBody := []byte(`{
		"model":"deepseek-v4-flash",
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"t","signature":"sig-1"},
				{"type":"tool_use","id":"toolu_01A","name":"Bash","input":{"command":"ls"}}
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
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("出站请求体 JSON 不完整: %v", err)
	}
	msgs := payload["messages"].([]any)
	assistant := msgs[1].(map[string]any)
	content := assistant["content"].([]any)
	thinkingCount := 0
	for _, c := range content {
		blk, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if blk["type"] == "thinking" {
			thinkingCount++
		}
	}
	if thinkingCount != 1 {
		t.Fatalf("已有 thinking 的 assistant 被重复补块: %d 个 thinking 块", thinkingCount)
	}
}
