package relay

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/transformer/model"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/looplj/axonhub/llm"
)

// clearProtocolCapabilityCache 清空协议学习缓存，避免测试间串扰。
func clearProtocolCapabilityCache(t *testing.T) {
	t.Helper()
	protocolCapabilityCache.Range(func(key, _ any) bool {
		protocolCapabilityCache.Delete(key)
		return true
	})
}

func TestProtocolCandidatesNonAuto(t *testing.T) {
	got := protocolCandidates(outbound.OutboundTypeAnthropic, clientProtocolAnthropic, 1, "http://x")
	if len(got) != 1 || got[0] != outbound.OutboundTypeAnthropic {
		t.Fatalf("non-auto candidates = %v, want [Anthropic]", got)
	}
}

func TestProtocolCandidatesAutoChatFamily(t *testing.T) {
	clearProtocolCapabilityCache(t)

	// anthropic 客户端：客户端协议恒在首位，兜底链补齐其余 chat 协议
	got := protocolCandidates(outbound.OutboundTypeAuto, clientProtocolAnthropic, 2, "http://x")
	want := []outbound.OutboundType{
		outbound.OutboundTypeAnthropic,
		outbound.OutboundTypeOpenAIChat,
		outbound.OutboundTypeOpenAIResponse,
		outbound.OutboundTypeGemini,
	}
	if len(got) != len(want) {
		t.Fatalf("anthropic candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("anthropic candidates = %v, want %v", got, want)
		}
	}

	// openai chat 客户端同理
	got = protocolCandidates(outbound.OutboundTypeAuto, clientProtocolOpenAIChat, 2, "http://x")
	want = []outbound.OutboundType{
		outbound.OutboundTypeOpenAIChat,
		outbound.OutboundTypeAnthropic,
		outbound.OutboundTypeOpenAIResponse,
		outbound.OutboundTypeGemini,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("openai-chat candidates = %v, want %v", got, want)
		}
	}
}

func TestProtocolCandidatesAutoResponsesFamily(t *testing.T) {
	clearProtocolCapabilityCache(t)
	got := protocolCandidates(outbound.OutboundTypeAuto, clientProtocolOpenAIResponse, 3, "http://x")
	want := []outbound.OutboundType{outbound.OutboundTypeOpenAIResponse}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("responses candidates = %v, want %v", got, want)
	}
}

func TestProtocolCandidatesAutoEmbeddingFamily(t *testing.T) {
	clearProtocolCapabilityCache(t)
	got := protocolCandidates(outbound.OutboundTypeAuto, clientProtocolOpenAIEmbedding, 4, "http://x")
	want := []outbound.OutboundType{outbound.OutboundTypeOpenAIEmbedding}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("embedding candidates = %v, want %v", got, want)
	}
}

func TestProtocolCandidatesAutoUnknownClientProtocol(t *testing.T) {
	clearProtocolCapabilityCache(t)
	if got := protocolCandidates(outbound.OutboundTypeAuto, "unknown-proto", 5, "http://x"); got != nil {
		t.Fatalf("unknown client protocol candidates = %v, want nil", got)
	}
}

func TestProtocolCandidatesAutoCacheLearned(t *testing.T) {
	clearProtocolCapabilityCache(t)
	rememberProtocolCapability(6, "http://x", clientProtocolAnthropic, outbound.OutboundTypeOpenAIChat)
	defer clearProtocolCapabilityByChannel(6)

	// 缓存命中 learned：跳过客户端协议首探，直接使用学到的协议
	got := protocolCandidates(outbound.OutboundTypeAuto, clientProtocolAnthropic, 6, "http://x")
	want := []outbound.OutboundType{outbound.OutboundTypeOpenAIChat}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("learned candidates = %v, want %v", got, want)
	}

	// 不同 baseURL 独立学习
	if got := protocolCandidates(outbound.OutboundTypeAuto, clientProtocolAnthropic, 6, "http://y"); len(got) != 4 {
		t.Fatalf("different baseURL candidates = %v, want full chain (4)", got)
	}
}

func TestProtocolCandidatesAutoCacheUnsupported(t *testing.T) {
	clearProtocolCapabilityCache(t)
	rememberProtocolUnsupported(7, "http://x", clientProtocolAnthropic)
	defer clearProtocolCapabilityByChannel(7)

	if got := protocolCandidates(outbound.OutboundTypeAuto, clientProtocolAnthropic, 7, "http://x"); got != nil {
		t.Fatalf("unsupported candidates = %v, want nil (skip channel)", got)
	}
}

func TestProtocolCandidatesAutoCacheUnsupportedExpiry(t *testing.T) {
	clearProtocolCapabilityCache(t)
	// 直接写入过期条目：TTL 到期后允许重新探测
	protocolCapabilityCache.Store(protocolCapKey(8, "http://x", clientProtocolAnthropic),
		protocolCapabilityEntry{learned: protocolUnsupported, recordedAt: time.Now().Add(-protocolUnsupportedTTL - time.Minute)})
	defer clearProtocolCapabilityByChannel(8)

	got := protocolCandidates(outbound.OutboundTypeAuto, clientProtocolAnthropic, 8, "http://x")
	if len(got) != 4 {
		t.Fatalf("expired unsupported candidates = %v, want full chain (4)", got)
	}
}

func TestProtocolCandidatesAutoCacheClearedByChannel(t *testing.T) {
	clearProtocolCapabilityCache(t)
	rememberProtocolCapability(9, "http://x", clientProtocolAnthropic, outbound.OutboundTypeOpenAIChat)
	clearProtocolCapabilityByChannel(9)

	if got := protocolCandidates(outbound.OutboundTypeAuto, clientProtocolAnthropic, 9, "http://x"); len(got) != 4 {
		t.Fatalf("after clear candidates = %v, want full chain (4)", got)
	}
}
func TestClientProtocolFromAxonFormat(t *testing.T) {
	cases := []struct {
		format llm.APIFormat
		want   string
		ok     bool
	}{
		{llm.APIFormatAnthropicMessage, clientProtocolAnthropic, true},
		{llm.APIFormatOpenAIChatCompletion, clientProtocolOpenAIChat, true},
		{llm.APIFormatOpenAIResponse, clientProtocolOpenAIResponse, true},
		{llm.APIFormatOpenAIEmbedding, clientProtocolOpenAIEmbedding, true},
		{llm.APIFormat("unknown"), "", false},
	}
	for _, c := range cases {
		got, ok := clientProtocolFromAxonFormat(c.format)
		if ok != c.ok || got != c.want {
			t.Fatalf("clientProtocolFromAxonFormat(%q) = %q,%v want %q,%v", c.format, got, ok, c.want, c.ok)
		}
	}
}

func TestClientProtocolFromAPIFormat(t *testing.T) {
	cases := []struct {
		format model.APIFormat
		want   string
		ok     bool
	}{
		{model.APIFormatAnthropicMessage, clientProtocolAnthropic, true},
		{model.APIFormatOpenAIChatCompletion, clientProtocolOpenAIChat, true},
		{model.APIFormatOpenAIResponse, clientProtocolOpenAIResponse, true},
		{model.APIFormatOpenAIEmbedding, clientProtocolOpenAIEmbedding, true},
		{model.APIFormat("unknown"), "", false},
	}
	for _, c := range cases {
		got, ok := clientProtocolFromAPIFormat(c.format)
		if ok != c.ok || got != c.want {
			t.Fatalf("clientProtocolFromAPIFormat(%q) = %q,%v want %q,%v", c.format, got, ok, c.want, c.ok)
		}
	}
}
func TestShouldFallbackProtocol(t *testing.T) {
	cases := []struct {
		name    string
		code    int
		body    string
		written bool
		want    bool
	}{
		{"400 unwritten", 400, "", false, true},
		{"400 written", 400, "", true, false},
		{"403 business", 403, "{\"error\":{\"message\":\"forbidden\"}}", false, false},
		{"403 cloudflare block", 403, "<html>Attention Required! | Cloudflare</html>", false, true},
		{"403 cf-ray", 403, "cf-ray: 12345", false, true},
		{"404 model unavailable openai", 404, "{\"error\":{\"message\":\"The model 'gpt-4' does not exist\"}}", false, false},
		{"404 model unavailable code", 404, "{\"error\":{\"code\":\"model_not_found\"}}", false, false},
		{"404 model unavailable anthropic", 404, "{\"type\":\"error\",\"error\":{\"type\":\"not_found_error\",\"message\":\"model: claude-x\"}}", false, false},
		{"404 endpoint missing", 404, "Not Found", false, true},
		{"405", 405, "", false, true},
		{"429", 429, "", false, false},
		{"500", 500, "", false, false},
		{"502", 502, "", false, false},
		{"401", 401, "", false, false},
		{"connection error", 0, "", false, false},
	}
	for _, c := range cases {
		if got := ShouldFallbackProtocol(c.code, c.body, c.written); got != c.want {
			t.Fatalf("%s: ShouldFallbackProtocol(%d, %q, %v) = %v, want %v", c.name, c.code, c.body, c.written, got, c.want)
		}
	}
}
func TestIsModelUnavailableResponse(t *testing.T) {
	if !isModelUnavailableResponse("{\"error\":{\"code\":\"model_not_found\"}}") {
		t.Fatal("model_not_found should be detected")
	}
	if !isModelUnavailableResponse("The model 'gpt-4' does not exist or you do not have access") {
		t.Fatal("'does not exist' should be detected")
	}
	if !isModelUnavailableResponse("{\"type\":\"error\",\"error\":{\"type\":\"not_found_error\"}}") {
		t.Fatal("anthropic not_found_error should be detected")
	}
	if isModelUnavailableResponse("Not Found") {
		t.Fatal("plain Not Found should NOT be treated as model unavailable")
	}
	if isModelUnavailableResponse("") {
		t.Fatal("empty body should NOT be treated as model unavailable")
	}
}

func TestIsCloudflareBlockPage(t *testing.T) {
	if !isCloudflareBlockPage("<html><title>Attention Required! | Cloudflare</title>") {
		t.Fatal("cloudflare attention page should be detected")
	}
	if !isCloudflareBlockPage("cf-ray: abc123") {
		t.Fatal("cf-ray header text should be detected")
	}
	if isCloudflareBlockPage("{\"error\":{\"message\":\"forbidden\"}}") {
		t.Fatal("business 403 should NOT be detected as cloudflare")
	}
}

func TestExtractUpstreamStatusCode(t *testing.T) {
	if got := extractUpstreamStatusCode(fmt.Errorf("upstream error: 404: Not Found")); got != 404 {
		t.Fatalf("text parse = %d, want 404", got)
	}
	if got := extractUpstreamStatusCode(fmt.Errorf("upstream error: 500: boom")); got != 500 {
		t.Fatalf("text parse = %d, want 500", got)
	}
	if got := extractUpstreamStatusCode(errors.New("connection refused")); got != 0 {
		t.Fatalf("plain error = %d, want 0", got)
	}
	if got := extractUpstreamStatusCode(fmt.Errorf("upstream error: abc: x")); got != 0 {
		t.Fatalf("non-numeric status = %d, want 0", got)
	}
}
