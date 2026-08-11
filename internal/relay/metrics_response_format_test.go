package relay

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/transformer/inbound"
	transformerModel "github.com/bestruirui/octopus/internal/transformer/model"
)

func anthropicTestResponse() *transformerModel.InternalLLMResponse {
	content := "hello from claude"
	return &transformerModel.InternalLLMResponse{
		ID:     "msg_01",
		Object: "chat.completion",
		Model:  "claude-3-5-sonnet",
		Choices: []transformerModel.Choice{
			{
				Message: &transformerModel.Message{
					Role:    "assistant",
					Content: transformerModel.MessageContent{Content: &content},
				},
			},
		},
	}
}

// TestResponseContentForLogUsesInboundProtocolFormat 验证配置了 Anthropic
// 入站适配器时，日志响应体为 Anthropic Message 格式而非 OpenAI 格式。
func TestResponseContentForLogUsesInboundProtocolFormat(t *testing.T) {
	metrics := NewRelayMetrics(1, "claude-3-5-sonnet", nil, &transformerModel.InternalLLMRequest{Model: "claude-3-5-sonnet"})
	metrics.InboundAdapter = inbound.Get(inbound.InboundTypeAnthropic)
	metrics.SetInternalResponse(anthropicTestResponse(), "claude-3-5-sonnet", 0)

	got := metrics.responseContentForLog(context.Background())
	if got == "" {
		t.Fatal("expected non-empty response content")
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("response content is not valid JSON: %v", err)
	}
	if parsed["type"] != "message" {
		t.Fatalf("expected anthropic message format (type=message), got %#v", parsed["type"])
	}
	if strings.Contains(got, "\"choices\"") {
		t.Fatalf("expected anthropic format without choices field, got: %s", got)
	}
}

// TestResponseContentForLogFallbackWithoutInboundAdapter 验证无入站适配器时
// 回退为内部 OpenAI 格式（原行为）。
func TestResponseContentForLogFallbackWithoutInboundAdapter(t *testing.T) {
	metrics := NewRelayMetrics(1, "claude-3-5-sonnet", nil, &transformerModel.InternalLLMRequest{Model: "claude-3-5-sonnet"})
	metrics.SetInternalResponse(anthropicTestResponse(), "claude-3-5-sonnet", 0)

	got := metrics.responseContentForLog(context.Background())
	if got == "" {
		t.Fatal("expected non-empty response content")
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("response content is not valid JSON: %v", err)
	}
	if parsed["object"] != "chat.completion" {
		t.Fatalf("expected fallback internal openai format, got %#v", parsed["object"])
	}
	if _, ok := parsed["choices"]; !ok {
		t.Fatalf("expected choices field in fallback format, got: %s", got)
	}
}

// TestResponseContentForLogOpenAIChatKeepsFormat 验证 OpenAI Chat 入站时
// 响应体仍为 chat.completion 格式（与改造前一致）。
func TestResponseContentForLogOpenAIChatKeepsFormat(t *testing.T) {
	metrics := NewRelayMetrics(1, "gpt-4o", nil, &transformerModel.InternalLLMRequest{Model: "gpt-4o"})
	metrics.InboundAdapter = inbound.Get(inbound.InboundTypeOpenAIChat)
	metrics.SetInternalResponse(anthropicTestResponse(), "gpt-4o", 0)

	got := metrics.responseContentForLog(context.Background())
	if got == "" {
		t.Fatal("expected non-empty response content")
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("response content is not valid JSON: %v", err)
	}
	if parsed["object"] != "chat.completion" {
		t.Fatalf("expected openai chat.completion format, got %#v", parsed["object"])
	}
}

// TestResponseContentForLogNilResponse 验证无响应时返回空字符串。
func TestResponseContentForLogNilResponse(t *testing.T) {
	metrics := NewRelayMetrics(1, "gpt-4o", nil, &transformerModel.InternalLLMRequest{Model: "gpt-4o"})
	metrics.InboundAdapter = inbound.Get(inbound.InboundTypeAnthropic)
	if got := metrics.responseContentForLog(context.Background()); got != "" {
		t.Fatalf("expected empty response content for nil response, got %q", got)
	}
}

// failingInboundAdapter 的 TransformResponse 恒返回错误，用于验证回退分支。
type failingInboundAdapter struct{}

func (failingInboundAdapter) TransformRequest(ctx context.Context, body []byte) (*transformerModel.InternalLLMRequest, error) {
	return nil, nil
}

func (failingInboundAdapter) TransformResponse(ctx context.Context, response *transformerModel.InternalLLMResponse) ([]byte, error) {
	return nil, errors.New("transform failed")
}

func (failingInboundAdapter) TransformStream(ctx context.Context, stream *transformerModel.InternalLLMResponse) ([]byte, error) {
	return nil, nil
}

func (failingInboundAdapter) GetInternalResponse(ctx context.Context) (*transformerModel.InternalLLMResponse, error) {
	return nil, nil
}

// TestResponseContentForLogFallsBackOnTransformError 验证入站协议转换失败时
// 回退为内部 OpenAI 格式，而非丢弃响应内容。
func TestResponseContentForLogFallsBackOnTransformError(t *testing.T) {
	metrics := NewRelayMetrics(1, "gpt-4o", nil, &transformerModel.InternalLLMRequest{Model: "gpt-4o"})
	metrics.InboundAdapter = failingInboundAdapter{}
	metrics.SetInternalResponse(anthropicTestResponse(), "gpt-4o", 0)

	got := metrics.responseContentForLog(context.Background())
	if got == "" {
		t.Fatal("expected fallback response content on transform error")
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("response content is not valid JSON: %v", err)
	}
	if parsed["object"] != "chat.completion" {
		t.Fatalf("expected fallback internal openai format, got %#v", parsed["object"])
	}
}
