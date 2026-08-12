package balancer

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

// 探测样本只补成功率：真实样本的延迟统计不被 max_tokens=1 的极小请求稀释。
func TestRecordAutoProbeSampleDoesNotTouchLatency(t *testing.T) {
	AutoRankReset()
	RecordAutoSampleForGroup(1, 10, "gpt-4o", true, 800, 400)
	RecordAutoProbeSampleForGroup(1, 10, "gpt-4o", true)
	RecordAutoProbeSampleForGroup(1, 10, "gpt-4o", true)

	st := GetAutoRankStatsForGroup(1, 10, "gpt-4o")
	if st.Samples != 3 {
		t.Fatalf("expected 3 samples, got %d", st.Samples)
	}
	if st.ProbeSamples != 2 {
		t.Fatalf("expected 2 probe samples, got %d", st.ProbeSamples)
	}
	if st.RealSamples() != 1 {
		t.Fatalf("expected 1 real sample, got %d", st.RealSamples())
	}
	if st.EWMALatencyMS != 800 {
		t.Fatalf("probe must not touch latency ewma: got %v, want 800", st.EWMALatencyMS)
	}
	if st.EWMATTFBMS != 400 {
		t.Fatalf("probe must not touch ttfb ewma: got %v, want 400", st.EWMATTFBMS)
	}
}

// 零流量候选靠探测暴露故障：成功率如实归零，但档位不因探测而变化。
func TestProbeFailuresSurfaceOutageWithoutAdvancingTier(t *testing.T) {
	AutoRankReset()
	for i := 0; i < 5; i++ {
		RecordAutoProbeSampleForGroup(2, 11, "gpt-4o", false)
	}

	st := GetAutoRankStatsForGroup(2, 11, "gpt-4o")
	if st.Samples != 5 || st.Failures != 5 {
		t.Fatalf("expected 5 samples / 5 failures, got %d/%d", st.Samples, st.Failures)
	}
	if st.SuccessRate != 0 {
		t.Fatalf("expected zero success rate, got %v", st.SuccessRate)
	}
	if tier := autoCandidateTier(st, 3); tier != 0 {
		t.Fatalf("probe-only candidate must stay at tier 0, got %d", tier)
	}
}

// 档位只认真实样本：否则纯探测候选会带着"零延迟"进竞技池，抢走真实快速候选的份额。
func TestAutoCandidateTierUsesRealSamplesOnly(t *testing.T) {
	if tier := autoCandidateTier(AutoRankStats{Samples: 6, ProbeSamples: 4}, 3); tier != 1 {
		t.Fatalf("expected tier 1 for 2 real samples, got %d", tier)
	}
	if tier := autoCandidateTier(AutoRankStats{Samples: 6, ProbeSamples: 3}, 3); tier != 2 {
		t.Fatalf("expected tier 2 for 3 real samples, got %d", tier)
	}
	if tier := autoCandidateTier(AutoRankStats{Samples: 6, ProbeSamples: 6}, 3); tier != 0 {
		t.Fatalf("expected tier 0 for probe-only stats, got %d", tier)
	}
}

// 重启恢复必须保留 probe 标记，否则探测样本会摇身变成真实样本推进档位。
func TestAutoRankRestoreKeepsProbeSamples(t *testing.T) {
	AutoRankReset()
	AutoRankRestore([]model.AutoRankSnapshot{{
		GroupID:       4,
		ChannelID:     13,
		ModelName:     "gpt-4o",
		Samples:       6,
		ProbeSamples:  4,
		Failures:      1,
		EWMALatencyMS: 700,
		LastSeenAt:    time.Now(),
	}})

	st := GetAutoRankStatsForGroup(4, 13, "gpt-4o")
	if st.Samples != 6 || st.ProbeSamples != 4 {
		t.Fatalf("expected 6 samples / 4 probes, got %d/%d", st.Samples, st.ProbeSamples)
	}
	if st.RealSamples() != 2 {
		t.Fatalf("expected 2 real samples after restore, got %d", st.RealSamples())
	}
}

// 站点以 400/404/429 拒绝探测请求，说明它不接受无业务意图的测活，
// 不代表渠道不健康——这类结果不能计入健康窗口。
func TestCountableFailureExcludesClientRejections(t *testing.T) {
	for _, code := range []int{400, 404, 408, 429, 499, 596} {
		if CountableFailure(code) {
			t.Fatalf("status %d must not count as a channel failure", code)
		}
	}
	for _, code := range []int{0, 401, 403, 500, 503, 524} {
		if !CountableFailure(code) {
			t.Fatalf("status %d must count as a channel failure", code)
		}
	}
}
