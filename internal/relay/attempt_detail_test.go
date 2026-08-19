package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/looplj/axonhub/llm"
)

// findFailedAttempt 从审计日志里取出指定渠道的失败尝试记录。
func findFailedAttempt(t *testing.T, ctx context.Context, channelName string) dbmodel.ChannelAttempt {
	t.Helper()
	logs, err := op.RelayLogList(ctx, nil, nil, nil, 1, 20)
	if err != nil {
		t.Fatalf("RelayLogList failed: %v", err)
	}
	for i := range logs {
		for _, a := range logs[i].Attempts {
			if a.ChannelName == channelName && a.Status == dbmodel.AttemptFailed {
				return a
			}
		}
	}
	t.Fatalf("failed attempt for channel %q not found (%d logs)", channelName, len(logs))
	return dbmodel.ChannelAttempt{}
}

// newFailingUpstream 构造一个固定返回 400 + 指定错误体的上游。
func newFailingUpstream(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
}

// createAxonDetailChannel 建渠道 + 同名分组，模型名与分组名一致便于直接请求。
func createAxonDetailChannel(t *testing.T, ctx context.Context, name string, chType outbound.OutboundType, url string) {
	t.Helper()
	channel := &dbmodel.Channel{
		Name: name, Type: chType, Enabled: true,
		BaseUrls: []dbmodel.BaseUrl{{URL: url}}, Model: name + "-model",
		Keys: []dbmodel.ChannelKey{{Enabled: true, ChannelKey: "super-secret-key"}},
	}
	if err := op.ChannelCreate(channel, ctx); err != nil {
		t.Fatalf("ChannelCreate failed: %v", err)
	}
	if err := op.GroupCreate(&dbmodel.Group{
		Name: name + "-model", Mode: dbmodel.GroupModeFailover,
		Items: []dbmodel.GroupItem{{ChannelID: channel.ID, ModelName: name + "-model", Weight: 1}},
	}, ctx); err != nil {
		t.Fatalf("GroupCreate failed: %v", err)
	}
}

// TestTextHandlerFailedAttemptRecordsDetailStandardPath 覆盖 axon 标准路径
// （跨协议：anthropic 客户端 → openai chat 渠道，即 KAPI 案例中协议切换后的形态）。
// 此前该路径完全不调用记录逻辑，relay_log_failed_detail_enabled 对已迁移的
// 主力文本路径实际失效，前端只能回落到日志级字段，失败详情具有误导性。
func TestTextHandlerFailedAttemptRecordsDetailStandardPath(t *testing.T) {
	ctx := setupRelayTestDB(t)

	upstream := newFailingUpstream(`{"error":{"message":"'reasoning_effort' must be one of: 'low', 'medium', 'high'"}}`)
	defer upstream.Close()
	createAxonDetailChannel(t, ctx, "axon-detail-std", outbound.OutboundTypeOpenAIChat, upstream.URL)

	_, c := newTextRelayGinContext(t, http.MethodPost, "/v1/messages",
		`{"model":"axon-detail-std-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	TextHandler(llm.APIFormatAnthropicMessage, c)

	attempt := findFailedAttempt(t, ctx, "axon-detail-std")
	if attempt.RequestBody == "" {
		t.Fatal("expected request_body recorded for failed axon attempt")
	}
	if !strings.Contains(attempt.RequestBody, "axon-detail-std-model") {
		t.Fatalf("request_body should be the outbound payload, got: %s", attempt.RequestBody)
	}
	if !strings.Contains(attempt.ResponseBody, "reasoning_effort") {
		t.Fatalf("expected upstream error body recorded, got: %q", attempt.ResponseBody)
	}
	if attempt.OutboundHeaders == "" {
		t.Fatal("expected outbound_headers recorded")
	}
	if strings.Contains(attempt.OutboundHeaders, "super-secret-key") {
		t.Fatalf("outbound_headers must be redacted, got: %s", attempt.OutboundHeaders)
	}
}

// TestTextHandlerFailedAttemptRecordsDetailAnthropicPassthrough 覆盖 anthropic
// 同格式直通路径（KAPI 案例中的第一次尝试形态）。直通发送的是客户端原始字节，
// 出站体即入站体，响应体需在函数内部读到时记录。
func TestTextHandlerFailedAttemptRecordsDetailAnthropicPassthrough(t *testing.T) {
	ctx := setupRelayTestDB(t)

	upstream := newFailingUpstream(`{"type":"error","error":{"message":"passthrough upstream boom"}}`)
	defer upstream.Close()
	createAxonDetailChannel(t, ctx, "axon-detail-pt", outbound.OutboundTypeAnthropic, upstream.URL)

	_, c := newTextRelayGinContext(t, http.MethodPost, "/v1/messages",
		`{"model":"axon-detail-pt-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	TextHandler(llm.APIFormatAnthropicMessage, c)

	attempt := findFailedAttempt(t, ctx, "axon-detail-pt")
	if attempt.RequestBody == "" {
		t.Fatal("expected request_body recorded for failed passthrough attempt")
	}
	if !strings.Contains(attempt.ResponseBody, "passthrough upstream boom") {
		t.Fatalf("expected upstream error body recorded, got: %q", attempt.ResponseBody)
	}
}

// TestTextHandlerFailedDetailDisabledSkipsRecording 开关关闭时 axon 路径同样
// 不落盘，与自研路径口径一致。
func TestTextHandlerFailedDetailDisabledSkipsRecording(t *testing.T) {
	ctx := setupRelayTestDB(t)
	if err := op.SettingSetString(dbmodel.SettingKeyRelayLogFailedDetailEnabled, "false"); err != nil {
		t.Fatalf("SettingSetString failed: %v", err)
	}

	upstream := newFailingUpstream(`{"error":{"message":"boom"}}`)
	defer upstream.Close()
	createAxonDetailChannel(t, ctx, "axon-detail-off", outbound.OutboundTypeOpenAIChat, upstream.URL)

	_, c := newTextRelayGinContext(t, http.MethodPost, "/v1/messages",
		`{"model":"axon-detail-off-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	TextHandler(llm.APIFormatAnthropicMessage, c)

	attempt := findFailedAttempt(t, ctx, "axon-detail-off")
	if attempt.RequestBody != "" || attempt.ResponseBody != "" || attempt.OutboundHeaders != "" {
		t.Fatalf("switch off should skip recording, got req=%q resp=%q headers=%q",
			attempt.RequestBody, attempt.ResponseBody, attempt.OutboundHeaders)
	}
}
