package relay

import (
	"context"
	"testing"

	inboundAnthropic "github.com/bestruirui/octopus/internal/transformer/inbound/anthropic"
	transformerModel "github.com/bestruirui/octopus/internal/transformer/model"
)

// TestNewRelayMetricsExtractsReasoningEffort verifies that NewRelayMetrics
// captures the request's reasoning effort so saveLog can persist it to the
// relay log. This is the data-source for the log page's effort badge.
func TestNewRelayMetricsExtractsReasoningEffort(t *testing.T) {
	req := &transformerModel.InternalLLMRequest{
		Model:           "deepseek-chat",
		ReasoningEffort: "high",
	}
	m := NewRelayMetrics(1, req.Model, nil, req)
	if m.ReasoningEffort != "high" {
		t.Fatalf("expected ReasoningEffort=high, got %q", m.ReasoningEffort)
	}
}

// TestNewRelayMetricsEmptyEffortWhenNotSet verifies no crash and empty effort
// when the request carries no reasoning config.
func TestNewRelayMetricsEmptyEffortWhenNotSet(t *testing.T) {
	req := &transformerModel.InternalLLMRequest{Model: "deepseek-chat"}
	m := NewRelayMetrics(1, req.Model, nil, req)
	if m.ReasoningEffort != "" {
		t.Fatalf("expected empty ReasoningEffort, got %q", m.ReasoningEffort)
	}
}

// TestNewRelayMetricsNilRequest verifies nil request safety.
func TestNewRelayMetricsNilRequest(t *testing.T) {
	m := NewRelayMetrics(1, "deepseek-chat", nil, nil)
	if m.ReasoningEffort != "" {
		t.Fatalf("expected empty ReasoningEffort for nil request, got %q", m.ReasoningEffort)
	}
}

// TestClaudeCodeAdaptiveThinkingSetsReasoningEffort simulates the full path:
// Claude Code sends an Anthropic request with adaptive thinking, the inbound
// transformer extracts reasoning_effort, and NewRelayMetrics captures it for
// the relay log.
func TestClaudeCodeAdaptiveThinkingSetsReasoningEffort(t *testing.T) {
	anthropicBody := `{
		"model": "deepseek-chat",
		"max_tokens": 1024,
		"thinking": {"type": "adaptive", "budget_tokens": 2048},
		"output_config": {"effort": "high"},
		"messages": [{"role": "user", "content": "hello"}]
	}`

	inbound := &inboundAnthropic.MessagesInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(anthropicBody))
	if err != nil {
		t.Fatalf("anthropic inbound failed: %v", err)
	}
	if internalReq.ReasoningEffort != "high" {
		t.Fatalf("expected reasoning_effort=high from adaptive thinking, got %q", internalReq.ReasoningEffort)
	}

	m := NewRelayMetrics(1, internalReq.Model, nil, internalReq)
	if m.ReasoningEffort != "high" {
		t.Fatalf("expected metrics.ReasoningEffort=high, got %q", m.ReasoningEffort)
	}
}

// TestClaudeCodeEnabledThinkingBudgetSetsReasoningEffort verifies the
// budget_tokens form (thinking.type=enabled) also yields a non-empty effort.
func TestClaudeCodeEnabledThinkingBudgetSetsReasoningEffort(t *testing.T) {
	anthropicBody := `{
		"model": "deepseek-chat",
		"max_tokens": 1024,
		"thinking": {"type": "enabled", "budget_tokens": 2048},
		"messages": [{"role": "user", "content": "hello"}]
	}`

	inbound := &inboundAnthropic.MessagesInbound{}
	internalReq, err := inbound.TransformRequest(context.Background(), []byte(anthropicBody))
	if err != nil {
		t.Fatalf("anthropic inbound failed: %v", err)
	}
	if internalReq.ReasoningEffort == "" {
		t.Fatal("expected non-empty reasoning_effort from enabled thinking with budget")
	}

	m := NewRelayMetrics(1, internalReq.Model, nil, internalReq)
	if m.ReasoningEffort == "" {
		t.Fatal("expected metrics.ReasoningEffort to be non-empty")
	}
}