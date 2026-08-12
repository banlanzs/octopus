package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestFindTopLevelStringFieldAfterHugeArray 模拟真实 Claude Code guardrail 请求形状：
// model → max_tokens → system(大) → messages(大 user 文本) → reasoning_effort 在末尾。
// 验证 findTopLevelStringField 在超大 body 上仍能定位数组之后的 reasoning_effort。
func TestFindTopLevelStringFieldAfterHugeArray(t *testing.T) {
	raw := buildHugeBody(t)

	got, err := normalizeRawReasoningEffort(raw)
	if err != nil {
		t.Fatalf("normalizeRawReasoningEffort() error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("输出 JSON 损坏: %v (len=%d)", err, len(got))
	}
	if payload["reasoning_effort"] != "low" {
		t.Fatalf("reasoning_effort 未归一化: got %#v (输出 len=%d)", payload["reasoning_effort"], len(got))
	}
}

// TestTransformRequestRawNormalizesLargeBody 走完整透传路径验证归一化生效。
func TestTransformRequestRawNormalizesLargeBody(t *testing.T) {
	outbound := &MessageOutbound{}
	req, err := outbound.TransformRequestRaw(
		context.Background(),
		buildHugeBody(t),
		"deepseek-v4-flash-0731",
		"https://api.deepseek.com/anthropic",
		"sk-test",
		nil,
	)
	if err != nil {
		t.Fatalf("TransformRequestRaw() error = %v", err)
	}
	body, err := readHTTPBody(req)
	if err != nil {
		t.Fatalf("read body error = %v", err)
	}
	t.Logf("outbound body len=%d", len(body))

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("输出 JSON 损坏: %v (len=%d)", err, len(body))
	}
	if payload["reasoning_effort"] != "low" {
		t.Fatalf("reasoning_effort 未归一化: got %#v", payload["reasoning_effort"])
	}
	if payload["model"] != "deepseek-v4-flash-0731" {
		t.Fatalf("model 未重写: got %#v", payload["model"])
	}
}

// TestStripDisabledThinkingForDeepSeek 覆盖字节级剥离 thinking.type=disabled 的各场景。
func TestStripDisabledThinkingForDeepSeek(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		strip bool // 期望是否发生剥离
	}{
		{
			name:  "disabled 中间字段(删前逗号)",
			body:  `{"model":"m","thinking":{"type":"disabled"},"max_tokens":8}`,
			strip: true,
		},
		{
			name:  "disabled 首字段(删后逗号)",
			body:  `{"thinking":{"type":"disabled"},"model":"m"}`,
			strip: true,
		},
		{
			name:  "disabled 末尾字段(删前逗号)",
			body:  `{"model":"m","thinking":{"type":"disabled"}}`,
			strip: true,
		},
		{
			name:  "enabled 保留",
			body:  `{"model":"m","thinking":{"type":"enabled","budget_tokens":100},"max_tokens":8}`,
			strip: false,
		},
		{
			name:  "adaptive 保留",
			body:  `{"model":"m","thinking":{"type":"adaptive"},"max_tokens":8}`,
			strip: false,
		},
		{
			name:  "无 thinking 不动",
			body:  `{"model":"m","max_tokens":8}`,
			strip: false,
		},
		{
			name:  "非对象 value 不动",
			body:  `{"model":"m","thinking":"disabled","max_tokens":8}`,
			strip: false,
		},
		{
			name:  "嵌套对象里的 disabled 不动",
			body:  `{"model":"m","metadata":{"thinking":{"type":"disabled"}},"max_tokens":8}`,
			strip: false,
		},
		{
			name:  "数组后的 disabled 剥离(expectKey 回归)",
			body:  `{"model":"m","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`,
			strip: true,
		},
		{
			name:  "带空白保留格式",
			body:  "{\n  \"model\": \"m\",\n  \"thinking\": {\"type\": \"disabled\"},\n  \"max_tokens\": 8\n}",
			strip: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, stripped, err := stripDisabledThinkingForDeepSeek([]byte(tc.body))
			if err != nil {
				t.Fatalf("stripDisabledThinkingForDeepSeek() error = %v", err)
			}
			if stripped != tc.strip {
				t.Fatalf("stripped = %v, want %v (输出: %s)", stripped, tc.strip, got)
			}
			// 输出必须是合法 JSON
			var payload map[string]any
			if err := json.Unmarshal(got, &payload); err != nil {
				t.Fatalf("输出 JSON 损坏: %v\n输出: %s", err, got)
			}
			// 剥离场景:thinking 必须不存在,且其余字段完整
			if tc.strip {
				if _, ok := payload["thinking"]; ok {
					t.Fatalf("剥离后不应包含 thinking: %s", got)
				}
				return
			}
			// 保留场景:输出必须与输入逐字节一致
			if string(got) != tc.body {
				t.Fatalf("保留场景输出被改动:\n输入: %s\n输出: %s", tc.body, got)
			}
		})
	}
}

// TestTransformRequestRawStripsDisabledThinking 透传路径:DeepSeek 目标剥离 disabled。
func TestTransformRequestRawStripsDisabledThinking(t *testing.T) {
	outbound := &MessageOutbound{}
	req, err := outbound.TransformRequestRaw(
		context.Background(),
		[]byte(`{"model":"claude-sonnet-5","max_tokens":64,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`),
		"deepseek-v4-flash-0731",
		"https://new.xkool.cfd/v1",
		"sk-test",
		nil,
	)
	if err != nil {
		t.Fatalf("TransformRequestRaw() error = %v", err)
	}
	body, err := readHTTPBody(req)
	if err != nil {
		t.Fatalf("read body error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("输出 JSON 损坏: %v\n输出: %s", err, body)
	}
	if _, ok := payload["thinking"]; ok {
		t.Fatalf("DeepSeek 目标应剥离 thinking.disabled, got: %s", body)
	}
	if payload["model"] != "deepseek-v4-flash-0731" {
		t.Fatalf("model 未重写: %#v", payload["model"])
	}
	if payload["max_tokens"] != float64(64) {
		t.Fatalf("max_tokens 被破坏: %#v", payload["max_tokens"])
	}
}

// TestTransformRequestRawKeepsDisabledThinkingNonDeepSeek 非 DeepSeek 目标保留 disabled。
func TestTransformRequestRawKeepsDisabledThinkingNonDeepSeek(t *testing.T) {
	outbound := &MessageOutbound{}
	req, err := outbound.TransformRequestRaw(
		context.Background(),
		[]byte(`{"model":"claude-sonnet-5","max_tokens":64,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`),
		"claude-sonnet-5",
		"https://api.anthropic.com/v1",
		"sk-test",
		nil,
	)
	if err != nil {
		t.Fatalf("TransformRequestRaw() error = %v", err)
	}
	body, err := readHTTPBody(req)
	if err != nil {
		t.Fatalf("read body error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("输出 JSON 损坏: %v\n输出: %s", err, body)
	}
	th, ok := payload["thinking"].(map[string]any)
	if !ok || th["type"] != "disabled" {
		t.Fatalf("非 DeepSeek 目标应保留 thinking.disabled, got: %s", body)
	}
}

// TestTransformRequestRawStripsDisabledThinkingLargeBody 大 body 场景的剥离(贴近真实 guardrail)。
func TestTransformRequestRawStripsDisabledThinkingLargeBody(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"model":"claude-sonnet-5","max_tokens":64,"thinking":{"type":"disabled"},"system":[{"type":"text","text":"`)
	sb.WriteString(strings.Repeat("system-prompt-content-", 6000))
	sb.WriteString(`"}],"messages":[{"role":"user","content":[{"type":"text","text":"`)
	sb.WriteString(strings.Repeat("user-content-", 6000))
	sb.WriteString(`"}]}],"metadata":{"session_id":"s1"}}`)

	outbound := &MessageOutbound{}
	req, err := outbound.TransformRequestRaw(
		context.Background(),
		[]byte(sb.String()),
		"deepseek-v4-flash-0731",
		"https://new.xkool.cfd/v1",
		"sk-test",
		nil,
	)
	if err != nil {
		t.Fatalf("TransformRequestRaw() error = %v", err)
	}
	body, err := readHTTPBody(req)
	if err != nil {
		t.Fatalf("read body error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("输出 JSON 损坏: %v (len=%d)", err, len(body))
	}
	if _, ok := payload["thinking"]; ok {
		t.Fatalf("大 body 也应剥离 thinking.disabled")
	}
	if payload["metadata"] == nil {
		t.Fatalf("metadata 被破坏")
	}
}

// buildHugeBody 构造 >128KB 的 Claude Code guardrail 形状 body。
func buildHugeBody(t *testing.T) []byte {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`{"model":"deepseek-v4-flash-0731","max_tokens":64,"system":[{"type":"text","text":"`)
	sb.WriteString(strings.Repeat("system-prompt-content-", 6000))
	sb.WriteString(`"}],"messages":[{"role":"user","content":[{"type":"text","text":"`)
	sb.WriteString(strings.Repeat("user-content-", 6000))
	sb.WriteString(`"}]}],"reasoning_effort":"minimal","metadata":{"user_id":"u1"}}`)

	raw := []byte(sb.String())
	if len(raw) < 128*1024 {
		t.Fatalf("test body too small: %d bytes", len(raw))
	}
	t.Logf("body size: %d bytes", len(raw))
	return raw
}

func readHTTPBody(req *http.Request) ([]byte, error) {
	if req == nil || req.Body == nil {
		return nil, nil
	}
	if req.GetBody != nil {
		r, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	}
	return io.ReadAll(req.Body)
}
