package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
)

// setupTextRelayTest 建立独立 SQLite 测试库，并创建指向 mock 上游的渠道与分组。
// group.Name 即模型名（octopus 按模型名路由分组）。
func setupTextRelayTest(t *testing.T, channelType outbound.OutboundType, modelName, upstreamURL string) {
	t.Helper()
	if dbpkg.GetDB() != nil {
		_ = dbpkg.Close()
	}
	dbPath := filepath.Join(t.TempDir(), "textrelay.db")
	if err := dbpkg.InitDB("sqlite", dbPath, false); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(func() { _ = dbpkg.Close() })

	ctx := context.Background()
	channel := &model.Channel{
		Name:     "textrelay-" + modelName,
		Type:     channelType,
		Enabled:  true,
		BaseUrls: []model.BaseUrl{{URL: upstreamURL}},
		Model:    modelName,
		Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "test-key"}},
	}
	if err := op.ChannelCreate(channel, ctx); err != nil {
		t.Fatalf("ChannelCreate failed: %v", err)
	}

	group := &model.Group{
		Name:  modelName,
		Mode:  model.GroupModeRoundRobin,
		Items: []model.GroupItem{{ChannelID: channel.ID, ModelName: modelName, Weight: 1}},
	}
	if err := op.GroupCreate(group, ctx); err != nil {
		t.Fatalf("GroupCreate failed: %v", err)
	}
}

func newTextRelayGinContext(t *testing.T, method, path, body string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("api_key_id", 1)
	return recorder, c
}

func TestTextHandlerOpenAIChatRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 校验上游收到的请求体是 openai chat completion 格式
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("upstream failed to decode request: %v", err)
		}
		if req["model"] != "gpt-4o" {
			t.Errorf("upstream model = %v, want gpt-4o", req["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hello from upstream"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`))
	}))
	defer upstream.Close()

	setupTextRelayTest(t, outbound.OutboundTypeOpenAIChat, "gpt-4o", upstream.URL)

	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)

	TextHandler(llm.APIFormatOpenAIChatCompletion, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v (body=%s)", err, recorder.Body.String())
	}
	if resp["model"] != "gpt-4o" {
		t.Fatalf("response model = %v, want gpt-4o", resp["model"])
	}
	choices, ok := resp["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("unexpected choices: %v", resp["choices"])
	}
}

func TestTextHandlerAnthropicMessagesRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("upstream failed to decode request: %v", err)
		}
		if req["model"] != "claude-3-5-sonnet" {
			t.Errorf("upstream model = %v, want claude-3-5-sonnet", req["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[{"type":"text","text":"hello from upstream"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":4}}`))
	}))
	defer upstream.Close()

	setupTextRelayTest(t, outbound.OutboundTypeAnthropic, "claude-3-5-sonnet", upstream.URL)

	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/messages",
		`{"model":"claude-3-5-sonnet","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)

	TextHandler(llm.APIFormatAnthropicMessage, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v (body=%s)", err, recorder.Body.String())
	}
	if resp["model"] != "claude-3-5-sonnet" {
		t.Fatalf("response model = %v, want claude-3-5-sonnet", resp["model"])
	}
	content, ok := resp["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("unexpected content: %v", resp["content"])
	}
}

func TestTextHandlerRejectsStreaming(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // 不应被触达
	}))
	defer upstream.Close()

	setupTextRelayTest(t, outbound.OutboundTypeOpenAIChat, "gpt-4o", upstream.URL)

	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	TextHandler(llm.APIFormatOpenAIChatCompletion, c)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for streaming (body=%s)", recorder.Code, recorder.Body.String())
	}
}
