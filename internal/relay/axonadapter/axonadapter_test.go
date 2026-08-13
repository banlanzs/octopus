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
