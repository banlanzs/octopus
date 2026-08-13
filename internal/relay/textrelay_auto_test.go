package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/looplj/axonhub/llm"
)

const autoAnthropicResp = "{\"id\":\"msg_auto1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"auto-claude\",\"content\":[{\"type\":\"text\",\"text\":\"hello auto\"}],\"stop_reason\":\"end_turn\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}"

const autoOpenAIChatResp = "{\"id\":\"chatcmpl-auto\",\"object\":\"chat.completion\",\"model\":\"auto-gpt\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"hello auto\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}"

// TestTextHandlerAutoAnthropicPassthrough：auto 渠道 + anthropic 兼容上游，
// anthropic 入站 → 客户端协议（Anthropic）直接透传成功（X-API-Key 直通头）。
func TestTextHandlerAutoAnthropicPassthrough(t *testing.T) {
	clearProtocolCapabilityCache(t)
	balancer.Reset()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			t.Errorf("passthrough path = %s, want /messages", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Errorf("passthrough X-API-Key = %q, want test-key", r.Header.Get("X-API-Key"))
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("upstream failed to decode request: %v", err)
		}
		if req["model"] != "auto-claude" {
			t.Errorf("upstream model = %v, want auto-claude", req["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(autoAnthropicResp))
	}))
	defer upstream.Close()

	setupTextRelayTest(t, outbound.OutboundTypeAuto, "auto-claude", upstream.URL)

	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/messages",
		"{\"model\":\"auto-claude\",\"max_tokens\":100,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")

	TextHandler(llm.APIFormatAnthropicMessage, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v (body=%s)", err, recorder.Body.String())
	}
	if resp["type"] != "message" {
		t.Fatalf("response type = %v, want message", resp["type"])
	}
	if resp["model"] != "auto-claude" {
		t.Fatalf("response model = %v, want auto-claude", resp["model"])
	}
}

// TestTextHandlerAutoOpenAIChat：auto 渠道 + openai 上游，chat 入站 → OpenAI Chat 协议成功。
func TestTextHandlerAutoOpenAIChat(t *testing.T) {
	clearProtocolCapabilityCache(t)
	balancer.Reset()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("path = %s, want /chat/completions", r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("upstream failed to decode request: %v", err)
		}
		if req["model"] != "auto-gpt" {
			t.Errorf("upstream model = %v, want auto-gpt", req["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(autoOpenAIChatResp))
	}))
	defer upstream.Close()

	setupTextRelayTest(t, outbound.OutboundTypeAuto, "auto-gpt", upstream.URL)

	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/chat/completions",
		"{\"model\":\"auto-gpt\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")

	TextHandler(llm.APIFormatOpenAIChatCompletion, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v (body=%s)", err, recorder.Body.String())
	}
	if resp["model"] != "auto-gpt" {
		t.Fatalf("response model = %v, want auto-gpt", resp["model"])
	}
}

// TestTextHandlerAutoFallback404ToOpenAIChat：auto 渠道 + openai-only 上游
// （/messages 返回 404 端点不存在），anthropic 入站 → 自动换 OpenAI Chat 协议
// 转换重试成功；第二次请求命中学习缓存，不再打 /messages。
func TestTextHandlerAutoFallback404ToOpenAIChat(t *testing.T) {
	clearProtocolCapabilityCache(t)
	balancer.Reset()

	var messagesHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			messagesHits.Add(1)
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("Not Found"))
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(autoOpenAIChatResp))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	setupTextRelayTest(t, outbound.OutboundTypeAuto, "auto-fb", upstream.URL)

	// 第一次请求：/messages 404 → 协议能力缺失 → 换 OpenAI Chat 转换成功
	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/messages",
		"{\"model\":\"auto-fb\",\"max_tokens\":100,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")
	TextHandler(llm.APIFormatAnthropicMessage, c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	if messagesHits.Load() != 1 {
		t.Fatalf("messages hits = %d, want 1", messagesHits.Load())
	}

	// 第二次请求：学习缓存命中（learned=OpenAIChat），跳过客户端协议首探
	recorder2, c2 := newTextRelayGinContext(t, http.MethodPost, "/v1/messages",
		"{\"model\":\"auto-fb\",\"max_tokens\":100,\"messages\":[{\"role\":\"user\",\"content\":\"hi again\"}]}")
	TextHandler(llm.APIFormatAnthropicMessage, c2)
	if recorder2.Code != http.StatusOK {
		t.Fatalf("second request status = %d, want 200 (body=%s)", recorder2.Code, recorder2.Body.String())
	}
	if messagesHits.Load() != 1 {
		t.Fatalf("messages hits after cache = %d, want 1 (cache should skip /messages)", messagesHits.Load())
	}

	// 学习缓存内容校验
	channel, err := op.ChannelGetByName("textrelay-auto-fb", t.Context())
	if err != nil {
		t.Fatalf("ChannelGetByName failed: %v", err)
	}
	baseURL := channel.GetBaseUrl()
	learned, unsupported, hit := lookupProtocolCapability(channel.ID, baseURL, clientProtocolAnthropic)
	if !hit || unsupported || learned != outbound.OutboundTypeOpenAIChat {
		t.Fatalf("capability cache = learned=%v unsupported=%v hit=%v, want learned=OpenAIChat hit=true", learned, unsupported, hit)
	}
}

// TestTextHandlerAutoAllCapabilityMissingNoBreaker：上游对所有协议都返回 404
// （端点不存在）→ 全部候选能力缺失 → 记「不支持」哨兵；能力协商事件不触发
// Key 级熔断（IsTripped 为 false）。
func TestTextHandlerAutoAllCapabilityMissingNoBreaker(t *testing.T) {
	clearProtocolCapabilityCache(t)
	balancer.Reset()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not Found"))
	}))
	defer upstream.Close()

	setupTextRelayTest(t, outbound.OutboundTypeAuto, "auto-dead", upstream.URL)

	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/messages",
		"{\"model\":\"auto-dead\",\"max_tokens\":100,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")
	TextHandler(llm.APIFormatAnthropicMessage, c)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body=%s)", recorder.Code, recorder.Body.String())
	}

	channel, err := op.ChannelGetByName("textrelay-auto-dead", t.Context())
	if err != nil {
		t.Fatalf("ChannelGetByName failed: %v", err)
	}
	keyID := 0
	if len(channel.Keys) > 0 {
		keyID = channel.Keys[0].ID
	}
	// 能力协商事件不触发 Key 级熔断
	if tripped, _ := balancer.IsTripped(channel.ID, keyID, "auto-dead"); tripped {
		t.Fatal("circuit breaker tripped by capability negotiation events, want closed")
	}
	// 「不支持」哨兵已记录
	_, unsupported, hit := lookupProtocolCapability(channel.ID, channel.GetBaseUrl(), clientProtocolAnthropic)
	if !hit || !unsupported {
		t.Fatalf("capability cache unsupported=%v hit=%v, want unsupported=true hit=true", unsupported, hit)
	}
}
