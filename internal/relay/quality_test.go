package relay

import (
	"testing"

	transformerModel "github.com/bestruirui/octopus/internal/transformer/model"
)

func toolLoopRequest(maxTokens int64) *transformerModel.InternalLLMRequest {
	return &transformerModel.InternalLLMRequest{
		Model:     "deepseek-v4-flash",
		MaxTokens: &maxTokens,
		Tools:     []transformerModel.Tool{{Type: "function", Function: transformerModel.Function{Name: "Read"}}},
		Messages: []transformerModel.Message{
			{Role: "user", Content: transformerModel.MessageContent{Content: loPtr("read the file")}},
			{Role: "assistant", ToolCalls: []transformerModel.ToolCall{{ID: "call_1", Type: "function", Function: transformerModel.FunctionCall{Name: "Read"}}}},
			{Role: "tool", ToolCallID: loPtr("call_1"), Content: transformerModel.MessageContent{Content: loPtr("file content")}},
		},
	}
}

func loPtr(s string) *string { return &s }

func TestIsQualityFailureResponse(t *testing.T) {
	cases := []struct {
		name          string
		req           *transformerModel.InternalLLMRequest
		outputTokens  int64
		want          bool
	}{
		{
			name:         "工具循环+输出过短 → 质量失败",
			req:          toolLoopRequest(64000),
			outputTokens: 28,
			want:         true,
		},
		{
			name:         "输出正常（≥100）→ 非质量失败",
			req:          toolLoopRequest(64000),
			outputTokens: 348,
			want:         false,
		},
		{
			name:         "guardrail（max_tokens=64）短输出 → 非质量失败",
			req:          toolLoopRequest(64),
			outputTokens: 8,
			want:         false,
		},
		{
			name:         "无 tools → 非质量失败",
			req: func() *transformerModel.InternalLLMRequest {
				r := toolLoopRequest(64000)
				r.Tools = nil
				return r
			}(),
			outputTokens: 10,
			want:         false,
		},
		{
			name: "无工具循环历史（纯对话）→ 非质量失败",
			req: func() *transformerModel.InternalLLMRequest {
				r := toolLoopRequest(64000)
				r.Messages = []transformerModel.Message{
					{Role: "user", Content: transformerModel.MessageContent{Content: loPtr("hi")}},
					{Role: "assistant", Content: transformerModel.MessageContent{Content: loPtr("hello")}},
				}
				return r
			}(),
			outputTokens: 5,
			want:         false,
		},
		{
			name:         "nil 请求 → 非质量失败",
			req:          nil,
			outputTokens: 10,
			want:         false,
		},
		{
			name: "role=tool 历史但无 tools 定义 → 非质量失败",
			req: func() *transformerModel.InternalLLMRequest {
				r := toolLoopRequest(64000)
				r.Tools = nil
				return r
			}(),
			outputTokens: 5,
			want:         false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isQualityFailureResponse(tc.req, tc.outputTokens)
			if got != tc.want {
				t.Fatalf("isQualityFailureResponse() = %v, want %v", got, tc.want)
			}
		})
	}
}
