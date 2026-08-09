package relay

import (
	"testing"

	"github.com/bestruirui/octopus/internal/model"
)

func TestIsAutoRankCountableFailure(t *testing.T) {
	countable := []int{0, 401, 402, 403, 405, 500, 502, 503, 504, 520, 521, 524, 597, 598, 599}
	noise := []int{400, 404, 408, 409, 415, 422, 429, 499, 596}

	for _, code := range countable {
		if !isAutoRankCountableFailure(code) {
			t.Fatalf("status %d should be counted as AutoRank failure (channel/key quality)", code)
		}
	}
	for _, code := range noise {
		if isAutoRankCountableFailure(code) {
			t.Fatalf("status %d is client noise / rate limit / quota and must NOT count as AutoRank failure", code)
		}
	}
	// 2xx 成功路径由调用方走 result.Success 分支，本函数只处理失败样本
	if isAutoRankCountableFailure(200) {
		t.Fatal("2xx should never be treated as failure")
	}
}

func TestHasRealAttempt(t *testing.T) {
	attempts := func(statuses ...model.AttemptStatus) []model.ChannelAttempt {
		out := make([]model.ChannelAttempt, 0, len(statuses))
		for _, s := range statuses {
			out = append(out, model.ChannelAttempt{Status: s})
		}
		return out
	}
	// 全 skip → 无真实尝试（触发兜底）
	if hasRealAttempt(attempts(model.AttemptSkipped, model.AttemptCircuitBreak)) {
		t.Fatal("expected all-skip attempts to report no real attempt")
	}
	// 任一真实尝试 → 有真实尝试（不触发兜底）
	if !hasRealAttempt(attempts(model.AttemptSkipped, model.AttemptFailed)) {
		t.Fatal("expected real attempt to be detected")
	}
	if !hasRealAttempt(attempts(model.AttemptSuccess)) {
		t.Fatal("expected success to be a real attempt")
	}
	// 空记录 → 无真实尝试
	if hasRealAttempt(nil) {
		t.Fatal("expected empty attempts to report no real attempt")
	}
}
