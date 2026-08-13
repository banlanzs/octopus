package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbpkg "github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/streams"
)

// setupTextRelayTest 建立独立 SQLite 测试库，并创建指向 mock 上游的渠道与分组。
// group.Name 即模型名（octopus 按模型名路由分组）。
func setupTextRelayTest(t *testing.T, channelType outbound.OutboundType, modelName, upstreamURL string) {
	setupTextRelayTestGroup(t, channelType, modelName, upstreamURL, false, 0)
}

func setupTextRelayTestGroup(t *testing.T, channelType outbound.OutboundType, modelName, upstreamURL string, retryEnabled bool, maxRetries int) {
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
		Name:         modelName,
		Mode:         model.GroupModeRoundRobin,
		RetryEnabled: retryEnabled,
		MaxRetries:   maxRetries,
		Items:        []model.GroupItem{{ChannelID: channel.ID, ModelName: modelName, Weight: 1}},
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

func TestTextHandlerRecordsRelayLog(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-metrics","object":"chat.completion","model":"gpt-4o-metrics","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`))
	}))
	defer upstream.Close()

	setupTextRelayTest(t, outbound.OutboundTypeOpenAIChat, "gpt-4o-metrics", upstream.URL)

	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"gpt-4o-metrics","messages":[{"role":"user","content":"hi"}]}`)

	TextHandler(llm.APIFormatOpenAIChatCompletion, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}

	logs, err := op.RelayLogList(context.Background(), nil, nil, nil, 1, 10)
	if err != nil {
		t.Fatalf("RelayLogList failed: %v", err)
	}
	found := false
	for _, l := range logs {
		if l.RequestModelName != "gpt-4o-metrics" {
			continue
		}
		found = true
		if !l.Success {
			t.Errorf("relay log success = false, want true")
		}
		if l.InputTokens != 10 || l.OutputTokens != 4 {
			t.Errorf("tokens = %d/%d, want 10/4", l.InputTokens, l.OutputTokens)
		}
	}
	if !found {
		t.Fatal("relay log for gpt-4o-metrics not found")
	}
}

func TestTextHandlerSameChannelRetry(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"temporary"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-retry","object":"chat.completion","model":"gpt-4o-retry","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	defer upstream.Close()

	setupTextRelayTestGroup(t, outbound.OutboundTypeOpenAIChat, "gpt-4o-retry", upstream.URL, true, 2)

	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"gpt-4o-retry","messages":[{"role":"user","content":"hi"}]}`)

	TextHandler(llm.APIFormatOpenAIChatCompletion, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after retry (body=%s)", recorder.Code, recorder.Body.String())
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("upstream attempts = %d, want 2", got)
	}
}

func TestTextHandlerAnthropicPassthrough(t *testing.T) {
	var receivedBody atomic.Value // string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer upstream.Close()

	// 内联 setup：group.Name=alias(客户端请求模型)，item.ModelName=claude-3-5-sonnet(上游模型)。
	if dbpkg.GetDB() != nil {
		_ = dbpkg.Close()
	}
	if err := dbpkg.InitDB("sqlite", filepath.Join(t.TempDir(), "pt.db"), false); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = dbpkg.Close() })
	ctx := context.Background()
	ch := &model.Channel{
		Name:     "pt-channel",
		Type:     outbound.OutboundTypeAnthropic,
		Enabled:  true,
		BaseUrls: []model.BaseUrl{{URL: upstream.URL}},
		Model:    "claude-3-5-sonnet",
		Keys:     []model.ChannelKey{{Enabled: true, ChannelKey: "test-key"}},
	}
	if err := op.ChannelCreate(ch, ctx); err != nil {
		t.Fatalf("ChannelCreate: %v", err)
	}
	if err := op.GroupCreate(&model.Group{
		Name:  "alias",
		Mode:  model.GroupModeRoundRobin,
		Items: []model.GroupItem{{ChannelID: ch.ID, ModelName: "claude-3-5-sonnet", Weight: 1}},
	}, ctx); err != nil {
		t.Fatalf("GroupCreate: %v", err)
	}

	// model 字段在最后，用于验证字节稳定（除 model 外其余字节不变）。
	clientBody := `{"max_tokens":100,"messages":[{"role":"user","content":"hi"}],"model":"alias"}`
	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/messages", clientBody)
	TextHandler(llm.APIFormatAnthropicMessage, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	got, _ := receivedBody.Load().(string)
	want := `{"max_tokens":100,"messages":[{"role":"user","content":"hi"}],"model":"claude-3-5-sonnet"}`
	if got != want {
		t.Fatalf("upstream body mismatch:\n got=%s\nwant=%s", got, want)
	}
}

func TestWriteAxonStreamFirstTokenTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	stream := &blockingStream{closed: make(chan struct{})}
	_, err := writeAxonStream(c, stream, nil, 100*time.Millisecond, 0)
	if !errors.Is(err, errAxonFirstTokenTimeout) {
		t.Fatalf("err = %v, want errAxonFirstTokenTimeout", err)
	}
}

// blockingStream 是一个永不产出事件、仅在 Close 后返回的流，用于模拟首 token 迟迟未达。
type blockingStream struct {
	closed chan struct{}
	once   sync.Once
}

func (s *blockingStream) Next() bool {
	<-s.closed
	return false
}

func (s *blockingStream) Current() *httpclient.StreamEvent { return nil }

func (s *blockingStream) Err() error { return nil }

func (s *blockingStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

var _ streams.Stream[*httpclient.StreamEvent] = (*blockingStream)(nil)

func TestTextHandlerAnthropicPassthroughParamOverride(t *testing.T) {
	var receivedBody atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`))
	}))
	defer upstream.Close()

	if dbpkg.GetDB() != nil {
		_ = dbpkg.Close()
	}
	if err := dbpkg.InitDB("sqlite", filepath.Join(t.TempDir(), "pt-po.db"), false); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = dbpkg.Close() })
	ctx := context.Background()
	po := `{"max_tokens":50}`
	ch := &model.Channel{
		Name:          "pt-po-channel",
		Type:          outbound.OutboundTypeAnthropic,
		Enabled:       true,
		BaseUrls:      []model.BaseUrl{{URL: upstream.URL}},
		Model:         "claude-3-5-sonnet",
		Keys:          []model.ChannelKey{{Enabled: true, ChannelKey: "test-key"}},
		ParamOverride: &po,
	}
	if err := op.ChannelCreate(ch, ctx); err != nil {
		t.Fatalf("ChannelCreate: %v", err)
	}
	if err := op.GroupCreate(&model.Group{
		Name:  "claude-3-5-sonnet",
		Mode:  model.GroupModeRoundRobin,
		Items: []model.GroupItem{{ChannelID: ch.ID, ModelName: "claude-3-5-sonnet", Weight: 1}},
	}, ctx); err != nil {
		t.Fatalf("GroupCreate: %v", err)
	}

	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/messages",
		`{"model":"claude-3-5-sonnet","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	TextHandler(llm.APIFormatAnthropicMessage, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	got, _ := receivedBody.Load().(string)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	if mt, _ := m["max_tokens"].(float64); mt != 50 {
		t.Fatalf("upstream max_tokens = %v, want 50 (param override not applied)", m["max_tokens"])
	}
}

func TestApplyVolcengineCompensation(t *testing.T) {
	outReq := &httpclient.Request{
		Body: []byte(`{"model":"doubao-seed-1-6-251015","input":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"metadata":{"k":"v"},"reasoning":{"effort":"medium"}}`),
	}
	llmReq := &llm.Request{Model: "doubao-seed-1-6-251015", ReasoningEffort: "medium"}

	applyVolcengineCompensation(outReq, llmReq)

	var m map[string]any
	if err := json.Unmarshal(outReq.Body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if thinking, ok := m["thinking"].(map[string]any); !ok || thinking["type"] != "enabled" {
		t.Fatalf("thinking = %v, want {type:enabled}", m["thinking"])
	}
	if _, ok := m["metadata"]; ok {
		t.Fatal("metadata should be removed")
	}
	if _, ok := m["reasoning"]; !ok {
		t.Fatal("reasoning should be kept for whitelisted model")
	}
	input := m["input"].([]any)
	last := input[len(input)-1].(map[string]any)
	if last["partial"] != true {
		t.Fatalf("last assistant partial = %v, want true", last["partial"])
	}
}

func TestApplyVolcengineCompensationNonWhitelistModel(t *testing.T) {
	outReq := &httpclient.Request{
		Body: []byte(`{"model":"other-model","input":"hi","reasoning":{"effort":"high"}}`),
	}
	llmReq := &llm.Request{Model: "other-model", ReasoningEffort: "high"}

	applyVolcengineCompensation(outReq, llmReq)

	var m map[string]any
	if err := json.Unmarshal(outReq.Body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["reasoning"]; ok {
		t.Fatal("reasoning should be removed for non-whitelisted model")
	}
	if thinking, ok := m["thinking"].(map[string]any); !ok || thinking["type"] != "enabled" {
		t.Fatalf("thinking = %v, want {type:enabled}", m["thinking"])
	}
}

func TestTextHandlerOpenAIResponsesRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_test","object":"response","model":"gpt-4o","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}}`))
	}))
	defer upstream.Close()

	setupTextRelayTest(t, outbound.OutboundTypeOpenAIResponse, "gpt-4o", upstream.URL)

	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/responses",
		`{"model":"gpt-4o","input":"hi"}`)

	TextHandler(llm.APIFormatOpenAIResponse, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, recorder.Body.String())
	}
	if resp["model"] != "gpt-4o" {
		t.Fatalf("model = %v, want gpt-4o", resp["model"])
	}
}

func TestTextHandlerOpenAIEmbeddingRoundTrip(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","model":"text-embedding-3-small","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`))
	}))
	defer upstream.Close()

	setupTextRelayTest(t, outbound.OutboundTypeOpenAIEmbedding, "text-embedding-3-small", upstream.URL)

	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/embeddings",
		`{"model":"text-embedding-3-small","input":"hello"}`)

	TextHandler(llm.APIFormatOpenAIEmbedding, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, recorder.Body.String())
	}
	if resp["model"] != "text-embedding-3-small" {
		t.Fatalf("model = %v, want text-embedding-3-small", resp["model"])
	}
}

func TestTextHandlerCrossProtocolOpenAIToAnthropic(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("upstream decode: %v", err)
		}
		// 断言上游收到 anthropic 格式请求（含 max_tokens、messages）
		if _, ok := req["messages"]; !ok {
			t.Errorf("upstream request missing anthropic messages: %v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[{"type":"text","text":"hello from anthropic"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":4}}`))
	}))
	defer upstream.Close()

	setupTextRelayTest(t, outbound.OutboundTypeAnthropic, "claude-3-5-sonnet", upstream.URL)

	// 客户端发 openai chat 格式请求，期望收到 openai chat 格式响应。
	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}]}`)
	TextHandler(llm.APIFormatOpenAIChatCompletion, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, recorder.Body.String())
	}
	// openai chat 格式响应：choices[].message.content
	choices, ok := resp["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("expected openai chat choices, got %v", resp["choices"])
	}
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "hello from anthropic" {
		t.Fatalf("content = %v, want hello from anthropic", msg["content"])
	}
}

func TestTextHandlerMultiChannelFailover(t *testing.T) {
	// 渠道 A：返回 500；渠道 B：返回成功。
	failUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer failUpstream.Close()

	okUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"gpt-4o-fo","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	defer okUpstream.Close()

	if dbpkg.GetDB() != nil {
		_ = dbpkg.Close()
	}
	if err := dbpkg.InitDB("sqlite", filepath.Join(t.TempDir(), "fo.db"), false); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = dbpkg.Close() })
	ctx := context.Background()

	chA := &model.Channel{
		Name: "fo-fail", Type: outbound.OutboundTypeOpenAIChat, Enabled: true,
		BaseUrls: []model.BaseUrl{{URL: failUpstream.URL}}, Model: "gpt-4o-fo",
		Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "k"}},
	}
	chB := &model.Channel{
		Name: "fo-ok", Type: outbound.OutboundTypeOpenAIChat, Enabled: true,
		BaseUrls: []model.BaseUrl{{URL: okUpstream.URL}}, Model: "gpt-4o-fo",
		Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "k"}},
	}
	if err := op.ChannelCreate(chA, ctx); err != nil {
		t.Fatalf("ChannelCreate A: %v", err)
	}
	if err := op.ChannelCreate(chB, ctx); err != nil {
		t.Fatalf("ChannelCreate B: %v", err)
	}
	if err := op.GroupCreate(&model.Group{
		Name: "gpt-4o-fo", Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ChannelID: chA.ID, ModelName: "gpt-4o-fo", Weight: 1},
			{ChannelID: chB.ID, ModelName: "gpt-4o-fo", Weight: 1},
		},
	}, ctx); err != nil {
		t.Fatalf("GroupCreate: %v", err)
	}

	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"gpt-4o-fo","messages":[{"role":"user","content":"hi"}]}`)
	TextHandler(llm.APIFormatOpenAIChatCompletion, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after failover (body=%s)", recorder.Code, recorder.Body.String())
	}
}

func TestDetectRouteMismatchTargetAxon(t *testing.T) {
	cases := []struct {
		name   string
		format llm.APIFormat
		err    string
		want   model.SiteModelRouteType
		ok     bool
	}{
		{"anthropic messages", llm.APIFormatOpenAIChatCompletion, "upstream /messages rejected", model.SiteModelRouteTypeAnthropic, true},
		{"anthropic version", llm.APIFormatOpenAIChatCompletion, "anthropic-version unsupported", model.SiteModelRouteTypeAnthropic, true},
		{"responses", llm.APIFormatOpenAIChatCompletion, "use /responses api", model.SiteModelRouteTypeOpenAIResponse, true},
		{"stream mismatch chat", llm.APIFormatOpenAIChatCompletion, "text/event-stream mismatch", model.SiteModelRouteTypeOpenAIResponse, true},
		{"stream mismatch non-chat", llm.APIFormatAnthropicMessage, "text/event-stream mismatch", "", false},
		{"no signal", llm.APIFormatOpenAIChatCompletion, "generic error", "", false},
	}
	for _, c := range cases {
		got, ok := detectRouteMismatchTargetAxon(c.format, errors.New(c.err))
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestTextHandlerAnthropicPassthroughStream(t *testing.T) {
	var receivedBody atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody.Store(string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[]}}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	// 内联 setup：group.Name=alias(客户端)，item.ModelName=claude-3-5-sonnet(上游)。
	if dbpkg.GetDB() != nil {
		_ = dbpkg.Close()
	}
	if err := dbpkg.InitDB("sqlite", filepath.Join(t.TempDir(), "pt-stream.db"), false); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = dbpkg.Close() })
	ctx := context.Background()
	ch := &model.Channel{
		Name: "pt-stream-channel", Type: outbound.OutboundTypeAnthropic, Enabled: true,
		BaseUrls: []model.BaseUrl{{URL: upstream.URL}}, Model: "claude-3-5-sonnet",
		Keys: []model.ChannelKey{{Enabled: true, ChannelKey: "test-key"}},
	}
	if err := op.ChannelCreate(ch, ctx); err != nil {
		t.Fatalf("ChannelCreate: %v", err)
	}
	if err := op.GroupCreate(&model.Group{
		Name:  "alias",
		Mode:  model.GroupModeRoundRobin,
		Items: []model.GroupItem{{ChannelID: ch.ID, ModelName: "claude-3-5-sonnet", Weight: 1}},
	}, ctx); err != nil {
		t.Fatalf("GroupCreate: %v", err)
	}

	// model 在最后，用于验证字节稳定（除 model 外其余字节不变）。
	clientBody := `{"max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}],"model":"alias"}`
	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/messages", clientBody)
	TextHandler(llm.APIFormatAnthropicMessage, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	// 上游字节稳定 + model 改写。
	got, _ := receivedBody.Load().(string)
	want := `{"max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}],"model":"claude-3-5-sonnet"}`
	if got != want {
		t.Fatalf("upstream body mismatch:\n got=%s\nwant=%s", got, want)
	}
	// 客户端收到透传的 SSE 字节。
	if !strings.Contains(recorder.Body.String(), "message_stop") {
		t.Fatalf("client stream missing message_stop: %q", recorder.Body.String())
	}
}

func TestTextHandlerOpenAIChatStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunks := []string{
			`{"id":"c1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`,
			`{"id":"c1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	setupTextRelayTest(t, outbound.OutboundTypeOpenAIChat, "gpt-4o", upstream.URL)

	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	TextHandler(llm.APIFormatOpenAIChatCompletion, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "hello") || !strings.Contains(body, "world") {
		t.Fatalf("stream body missing content: %q", body)
	}
}

func TestTextHandlerAnthropicMessagesStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		events := []struct {
			name string
			data string
		}{
			{"message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet","content":[]}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`},
			{"message_stop", `{"type":"message_stop"}`},
		}
		for _, ev := range events {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, ev.data)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer upstream.Close()

	setupTextRelayTest(t, outbound.OutboundTypeAnthropic, "claude-3-5-sonnet", upstream.URL)

	recorder, c := newTextRelayGinContext(t, http.MethodPost, "/v1/messages",
		`{"model":"claude-3-5-sonnet","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	TextHandler(llm.APIFormatAnthropicMessage, c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "hello") || !strings.Contains(body, "message_stop") {
		t.Fatalf("stream body missing content: %q", body)
	}
}
