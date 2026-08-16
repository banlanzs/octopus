package channeltest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
)

func channelForTest(baseURL string, channelType outbound.OutboundType) *dbmodel.Channel {
	return &dbmodel.Channel{
		Type:     channelType,
		BaseUrls: []dbmodel.BaseUrl{{URL: baseURL}},
		Keys:     []dbmodel.ChannelKey{{ID: 1, Enabled: true, ChannelKey: "sk-test"}},
		Model:    "test-model",
	}
}

func TestRunOpenAIChatSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "chatcmpl-1",
			"object": "chat.completion",
			"model":  "test-model",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "OK"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer server.Close()

	result := Run(context.Background(), channelForTest(server.URL+"/v1", outbound.OutboundTypeOpenAIChat))
	if !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}
	if result.Output != "OK" {
		t.Fatalf("expected output OK, got %q", result.Output)
	}
	if result.Usage == nil || result.Usage.TotalTokens != 2 {
		t.Fatalf("unexpected usage: %+v", result.Usage)
	}
}

func TestRunDetectsHTTP200ErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"message":"upstream still failed"}}`))
	}))
	defer server.Close()

	result := Run(context.Background(), channelForTest(server.URL+"/v1", outbound.OutboundTypeOpenAIChat))
	if result.Success {
		t.Fatalf("expected failure, got %+v", result)
	}
	if result.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", result.StatusCode)
	}
	if !strings.Contains(result.Error, "upstream still failed") {
		t.Fatalf("expected error to contain upstream message, got %q", result.Error)
	}
}

func TestRunAutoFallsBackToAnthropic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/chat/completions":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
		case "/messages":
			_, _ = w.Write([]byte(`{
				"id":"msg_1","type":"message","role":"assistant","model":"test-model",
				"content":[{"type":"text","text":"anthropic ok"}],
				"stop_reason":"end_turn","stop_sequence":null,
				"usage":{"input_tokens":2,"output_tokens":1}
			}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	result := Run(context.Background(), channelForTest(server.URL, outbound.OutboundTypeAuto))
	if !result.Success {
		t.Fatalf("expected auto fallback success, got %+v", result)
	}
	if result.Protocol != "anthropic" {
		t.Fatalf("expected anthropic protocol, got %q", result.Protocol)
	}
	if !strings.Contains(result.Output, "anthropic ok") {
		t.Fatalf("expected anthropic output, got %q", result.Output)
	}
}
