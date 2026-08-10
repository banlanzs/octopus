package model

import "testing"

// Issue #65: the choice rebuild loops iterated idx 0..len(map)-1 over maps
// keyed by event/choice index, silently dropping every choice stored at a
// non-contiguous index (e.g. tool calls at index >= 1) and — in
// InternalResponseFromStreamEvents — collapsing the whole aggregate to nil,
// which ChatInbound then dereferenced.

func TestInternalResponseFromStreamEventsKeepsNonContiguousChoiceIndices(t *testing.T) {
	events := []StreamEvent{
		{Kind: StreamEventKindTextDelta, ID: "resp_1", Model: "gpt-test", Index: 1, Delta: &StreamDelta{Text: "hello"}},
	}
	resp := InternalResponseFromStreamEvents(events)
	if resp == nil {
		t.Fatalf("expected non-nil response for events at Index=1, got nil")
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Index != 1 {
		t.Fatalf("expected single choice preserving Index=1, got %+v", resp.Choices)
	}
	if resp.Choices[0].Delta == nil || resp.Choices[0].Delta.Content.Content == nil || *resp.Choices[0].Delta.Content.Content != "hello" {
		t.Fatalf("expected text delta to survive, got %+v", resp.Choices[0].Delta)
	}
}

func TestInternalResponseFromStreamEventsStillNilForNoContent(t *testing.T) {
	tests := []struct {
		name   string
		events []StreamEvent
	}{
		{name: "empty slice", events: []StreamEvent{}},
		{name: "usage_delta with nil Usage", events: []StreamEvent{{Kind: StreamEventKindUsageDelta, Usage: nil}}},
		{name: "error event with nil Error", events: []StreamEvent{{Kind: StreamEventKindError, Error: nil}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if resp := InternalResponseFromStreamEvents(tt.events); resp != nil {
				t.Fatalf("expected nil response, got %+v", resp)
			}
		})
	}
}

// A chat chunk carrying a parallel tool call (tool_calls[].index=1 on choice 0)
// must produce events keyed by the choice index, not the tool call index —
// otherwise the round-trip rebuild re-homes the call into a phantom choice 1
// (or, before issue #65 was fixed, returns nil and panics ChatInbound).
func TestStreamEventsFromInternalResponseUsesChoiceIndexForToolCalls(t *testing.T) {
	chunk := &InternalLLMResponse{
		ID:     "chatcmpl-1",
		Object: "chat.completion.chunk",
		Model:  "gpt-test",
		Choices: []Choice{
			{
				Index: 0,
				Delta: &Message{
					ToolCalls: []ToolCall{
						{Index: 1, ID: "call_2", Type: "function", Function: FunctionCall{Name: "get_weather", Arguments: `{"city":"sf"}`}},
					},
				},
			},
		},
	}

	events := StreamEventsFromInternalResponse(chunk)
	if len(events) == 0 {
		t.Fatalf("expected events, got none")
	}
	for _, ev := range events {
		if ev.Kind == StreamEventKindToolCallDelta && ev.Index != 0 {
			t.Fatalf("tool call event must carry choice index 0, got Index=%d", ev.Index)
		}
	}

	rebuilt := InternalResponseFromStreamEvents(events)
	if rebuilt == nil {
		t.Fatalf("round-trip returned nil")
	}
	if len(rebuilt.Choices) != 1 || rebuilt.Choices[0].Index != 0 {
		t.Fatalf("expected single choice 0 after round-trip, got %+v", rebuilt.Choices)
	}
	toolCalls := rebuilt.Choices[0].Delta.ToolCalls
	if len(toolCalls) != 1 || toolCalls[0].Index != 1 || toolCalls[0].ID != "call_2" {
		t.Fatalf("expected tool call index=1 preserved on choice 0, got %+v", toolCalls)
	}
}

// Some OpenAI-compatible DeepSeek relays wrap a complete chat.completion
// object in an SSE data event instead of emitting chat.completion.chunk deltas.
// Treat choices[].message as a complete delta so an Anthropic client still gets
// content blocks; otherwise only finish_reason survives and Claude Code reports
// "Content block not found".
func TestStreamEventsFromInternalResponseProjectsCompleteMessage(t *testing.T) {
	content := "\n\n"
	finishReason := "tool_calls"
	resp := &InternalLLMResponse{
		ID:     "chatcmpl-83e7cdccbd63422a8d16c09aadc33f89",
		Object: "chat.completion",
		Model:  "accounts/fireworks/models/deepseek-v4-flash-0731",
		Choices: []Choice{{
			Index: 0,
			Message: &Message{
				Role:    "assistant",
				Content: MessageContent{Content: &content},
				ToolCalls: []ToolCall{{
					Index: 0,
					ID:    "call_3e9037e6d2b24abbb379ddab",
					Type:  "function",
					Function: FunctionCall{
						Name:      "Edit",
						Arguments: `{"file_path":"model.ts"}`,
					},
				}},
			},
			FinishReason: &finishReason,
		}},
	}

	events := StreamEventsFromInternalResponse(resp)
	var sawStart, sawText, sawTool, sawStop bool
	for _, event := range events {
		switch event.Kind {
		case StreamEventKindMessageStart:
			sawStart = event.Role == "assistant"
		case StreamEventKindTextDelta:
			sawText = event.Delta != nil && event.Delta.Text == "\n\n"
		case StreamEventKindToolCallDelta:
			sawTool = event.ToolCall != nil &&
				event.ToolCall.ID == "call_3e9037e6d2b24abbb379ddab" &&
				event.ToolCall.Function.Name == "Edit"
		case StreamEventKindMessageStop:
			sawStop = event.StopReason == FinishReasonToolCalls
		}
	}
	if !sawStart || !sawText || !sawTool || !sawStop {
		t.Fatalf("complete message was not projected into stream content events: %+v", events)
	}
}

func TestStreamAggregatorKeepsNonContiguousChoiceIndices(t *testing.T) {
	text := "hi"
	var agg StreamAggregator
	agg.Add(&InternalLLMResponse{
		ID:     "chatcmpl-1",
		Object: "chat.completion.chunk",
		Model:  "gpt-test",
		Choices: []Choice{
			{Index: 1, Delta: &Message{Content: MessageContent{Content: &text}}},
		},
	})
	resp := agg.BuildAndReset()
	if resp == nil {
		t.Fatalf("expected aggregated response, got nil")
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Index != 1 {
		t.Fatalf("expected choice at index 1 to survive aggregation, got %+v", resp.Choices)
	}
}

// DeepSeek V4（及部分中转站）的流式 chunk 以 content 块数组返回内容：
//   delta.content = [{"type":"thinking","thinking":"...","signature":"..."},
//                    {"type":"text","text":"..."}]
// 而 OpenAI 标准 Chat 流式用 delta.content 字符串 + delta.reasoning_content。
// StreamEventsFromInternalResponse 必须逐块投影数组格式，否则文本/思考被
// 整体丢弃，客户端收到无内容块的流并报 "Content block not found"
// （thinking 丢失还会导致后续请求无法回传而 400）。
func TestStreamEventsFromInternalResponseProjectsArrayContentBlocks(t *testing.T) {
	thinking := "thinking hard"
	sig := "sig-1"
	text := "<block>no"
	redacted := "redacted-data"
	resp := &InternalLLMResponse{
		ID:     "chatcmpl-1",
		Object: "chat.completion.chunk",
		Model:  "deepseek-v4-flash",
		Choices: []Choice{{
			Index: 0,
			Delta: &Message{
				Role: "assistant",
				Content: MessageContent{MultipleContent: []MessageContentPart{
					{Type: "thinking", Thinking: &thinking, Signature: &sig},
					{Type: "text", Text: &text},
					{Type: "redacted_thinking", RedactedThinking: &redacted},
				}},
			},
		}},
	}

	events := StreamEventsFromInternalResponse(resp)
	sawThinking, sawSignature, sawText, sawRedactedStart, sawRedactedStop := false, false, false, false, false
	for _, e := range events {
		switch e.Kind {
		case StreamEventKindThinkingDelta:
			if e.Delta != nil && e.Delta.Thinking == "thinking hard" && e.Delta.Signature == "sig-1" {
				sawThinking = true
			}
		case StreamEventKindSignatureDelta:
			if e.Delta != nil && e.Delta.Signature == "sig-1" {
				sawSignature = true
			}
		case StreamEventKindTextDelta:
			if e.Delta != nil && e.Delta.Text == "<block>no" {
				sawText = true
			}
		case StreamEventKindContentBlockStart:
			if e.ContentBlock != nil && e.ContentBlock.Type == "redacted_thinking" && e.ContentBlock.Data == "redacted-data" {
				sawRedactedStart = true
			}
		case StreamEventKindContentBlockStop:
			sawRedactedStop = true
		}
	}
	if !sawThinking {
		t.Fatalf("array thinking block not projected as thinking_delta: %+v", events)
	}
	if !sawText {
		t.Fatalf("array text block not projected as text_delta: %+v", events)
	}
	if !sawRedactedStart || !sawRedactedStop {
		t.Fatalf("array redacted_thinking block not projected as content block: %+v", events)
	}
	// thinking_delta 已带 signature，不应重复发 signature_delta（避免下游重复块）
	if sawSignature {
		t.Fatalf("signature emitted twice (thinking_delta already carries it): %+v", events)
	}
}
