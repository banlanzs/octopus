package axonadapter

import (
	"testing"

	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/looplj/axonhub/llm"
)

func TestOutboundTypeToAPIFormat(t *testing.T) {
	cases := []struct {
		in   outbound.OutboundType
		want llm.APIFormat
	}{
		{outbound.OutboundTypeOpenAIChat, llm.APIFormatOpenAIChatCompletion},
		{outbound.OutboundTypeOpenAIResponse, llm.APIFormatOpenAIResponse},
		{outbound.OutboundTypeAnthropic, llm.APIFormatAnthropicMessage},
		{outbound.OutboundTypeGemini, llm.APIFormatGeminiContents},
		{outbound.OutboundTypeVolcengine, ChannelTypeDoubao},
		{outbound.OutboundTypeOpenAIEmbedding, llm.APIFormatOpenAIEmbedding},
	}
	for _, c := range cases {
		got, ok := OutboundTypeToAPIFormat(c.in)
		if !ok {
			t.Fatalf("OutboundTypeToAPIFormat(%d) unexpectedly unsupported", c.in)
		}
		if got != c.want {
			t.Fatalf("OutboundTypeToAPIFormat(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOutboundTypeToAPIFormatUnsupported(t *testing.T) {
	if _, ok := OutboundTypeToAPIFormat(outbound.OutboundType(999)); ok {
		t.Fatal("expected unknown channel type to be unsupported")
	}
}

func TestNewOutboundChat(t *testing.T) {
	cases := []struct {
		channel llm.APIFormat
	}{
		{llm.APIFormatOpenAIChatCompletion},
		{llm.APIFormatOpenAIResponse},
		{llm.APIFormatAnthropicMessage},
		{llm.APIFormatGeminiContents},
		{ChannelTypeDoubao},
	}
	for _, c := range cases {
		o, err := NewOutbound(c.channel, llm.RequestTypeChat, "http://localhost/v1", "k")
		if err != nil {
			t.Fatalf("NewOutbound(chat, %s) error: %v", c.channel, err)
		}
		if o == nil {
			t.Fatalf("NewOutbound(chat, %s) returned nil", c.channel)
		}
	}
}

func TestNewOutboundEmbedding(t *testing.T) {
	cases := []struct {
		channel llm.APIFormat
	}{
		{llm.APIFormatOpenAIChatCompletion},
		{llm.APIFormatOpenAIResponse},
		{llm.APIFormatOpenAIEmbedding},
		{llm.APIFormatGeminiContents},
		{ChannelTypeDoubao},
	}
	for _, c := range cases {
		o, err := NewOutbound(c.channel, llm.RequestTypeEmbedding, "http://localhost/v1", "k")
		if err != nil {
			t.Fatalf("NewOutbound(embedding, %s) error: %v", c.channel, err)
		}
		if o == nil {
			t.Fatalf("NewOutbound(embedding, %s) returned nil", c.channel)
		}
	}
}

// TestSanitizeReasoningEffortForOutboundStripsOpenAIFamily 复现 KAPI 渠道的
// 400："'reasoning_effort' must be one of: 'low','medium','high','xhigh','max'"。
// 客户端发 thinking:{"type":"disabled"}，axonhub anthropic inbound 会写入
// ReasoningEffort="none"，OpenAI 系出站原样透传该内部哨兵导致上游拒绝。
func TestSanitizeReasoningEffortForOutboundStripsOpenAIFamily(t *testing.T) {
	for _, channelType := range []llm.APIFormat{
		llm.APIFormatOpenAIChatCompletion,
		llm.APIFormatOpenAIResponse,
		llm.APIFormatOpenAIEmbedding,
		ChannelTypeDoubao,
	} {
		req := &llm.Request{Model: "m", ReasoningEffort: "none"}
		got := SanitizeReasoningEffortForOutbound(req, channelType)
		if got.ReasoningEffort != "" {
			t.Fatalf("%s: ReasoningEffort = %q, want empty", channelType, got.ReasoningEffort)
		}
		if req.ReasoningEffort != "none" {
			t.Fatalf("%s: 入参被修改（%q），换协议重试会丢失禁用语义", channelType, req.ReasoningEffort)
		}
	}
}

// TestSanitizeReasoningEffortForOutboundKeepsConsumers anthropic 用哨兵还原
// thinking:{"type":"disabled"}，gemini 用它填 thinking_level:"none"（3.x 合法值）。
// 这两个协议必须保留哨兵，否则「禁用思考」的意图会丢失。
func TestSanitizeReasoningEffortForOutboundKeepsConsumers(t *testing.T) {
	for _, channelType := range []llm.APIFormat{
		llm.APIFormatAnthropicMessage,
		llm.APIFormatGeminiContents,
	} {
		req := &llm.Request{Model: "m", ReasoningEffort: "none"}
		if got := SanitizeReasoningEffortForOutbound(req, channelType); got.ReasoningEffort != "none" {
			t.Fatalf("%s: ReasoningEffort = %q, want none", channelType, got.ReasoningEffort)
		}
	}
}

// TestSanitizeReasoningEffortForOutboundPassthrough 真实 effort 值与空值不受影响，
// nil 请求不 panic。
func TestSanitizeReasoningEffortForOutboundPassthrough(t *testing.T) {
	for _, effort := range []string{"", "low", "medium", "high", "xhigh", "max"} {
		req := &llm.Request{Model: "m", ReasoningEffort: effort}
		got := SanitizeReasoningEffortForOutbound(req, llm.APIFormatOpenAIChatCompletion)
		if got.ReasoningEffort != effort {
			t.Fatalf("effort %q was altered to %q", effort, got.ReasoningEffort)
		}
		if got != req {
			t.Fatalf("effort %q: 未命中哨兵时应原样返回入参指针", effort)
		}
	}
	if got := SanitizeReasoningEffortForOutbound(nil, llm.APIFormatOpenAIChatCompletion); got != nil {
		t.Fatal("nil 请求应原样返回 nil")
	}
}

func TestNewOutboundIncompatible(t *testing.T) {
	// 不存在的渠道类型标识，chat 请求应报错。
	if _, err := NewOutbound("unknown/format", llm.RequestTypeChat, "http://localhost/v1", "k"); err == nil {
		t.Fatal("expected error for unknown chat channel type")
	}
	// 不支持的请求类型应报错。
	if _, err := NewOutbound(llm.APIFormatOpenAIChatCompletion, llm.RequestType("unknown"), "http://localhost/v1", "k"); err == nil {
		t.Fatal("expected error for unknown request type")
	}
}
