package balancer

import (
	"math"
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
	for _, code := range []int{0, 401, 403, 500, 503, 524, 529} {
		if !CountableFailure(code) {
			t.Fatalf("status %d must count as a channel failure", code)
		}
	}
}

// 有真实样本时探测必须彻底闭嘴：探测成功门槛远低于真实转发，
// 让它参与成功率就等于允许候选靠刷探测挤进竞技池。
func TestSuccessRateIgnoresProbesWhenRealSamplesExist(t *testing.T) {
	AutoRankReset()
	RecordAutoSampleForGroup(5, 14, "gpt-4o", true, 500, 300)
	RecordAutoSampleForGroup(5, 14, "gpt-4o", true, 500, 300)
	RecordAutoSampleForGroup(5, 14, "gpt-4o", false, 0, 0)
	for i := 0; i < 5; i++ {
		RecordAutoProbeSampleForGroup(5, 14, "gpt-4o", true)
	}

	st := GetAutoRankStatsForGroup(5, 14, "gpt-4o")
	if st.Samples != 8 || st.ProbeSamples != 5 {
		t.Fatalf("expected 8 samples / 5 probes, got %d/%d", st.Samples, st.ProbeSamples)
	}
	if want := 2.0 / 3.0; math.Abs(st.SuccessRate-want) > 1e-9 {
		t.Fatalf("success rate must come from real samples only: got %v, want %v", st.SuccessRate, want)
	}
	if max := wilsonLowerBound(2, 3); st.SuccessConfidence > max+1e-9 {
		t.Fatalf("probes inflated the confidence lower bound: got %v, max %v", st.SuccessConfidence, max)
	}
}

// 没有真实样本时才轮到探测说话——这正是它的本职：让零流量候选暴露故障。
func TestSuccessRateFallsBackToProbesWithoutRealSamples(t *testing.T) {
	AutoRankReset()
	RecordAutoProbeSampleForGroup(6, 15, "gpt-4o", true)
	RecordAutoProbeSampleForGroup(6, 15, "gpt-4o", false)

	st := GetAutoRankStatsForGroup(6, 15, "gpt-4o")
	if st.SuccessRate != 0.5 {
		t.Fatalf("expected probe-derived success rate 0.5, got %v", st.SuccessRate)
	}
	if st.SuccessConfidence <= 0 {
		t.Fatalf("expected non-zero confidence derived from probe samples")
	}
}

func TestProbeOnlyDeadRequiresNoRealSamples(t *testing.T) {
	if !(AutoRankStats{Samples: 3, ProbeSamples: 3, Failures: 3}).ProbeOnlyDead() {
		t.Fatalf("all-failed probe-only stats must be probe-dead")
	}
	if (AutoRankStats{Samples: 3, ProbeSamples: 3, Failures: 2}).ProbeOnlyDead() {
		t.Fatalf("partially successful probes must not be probe-dead")
	}
	// 出现任何真实样本，判据立即失效——真实数据永远优先于探测。
	if (AutoRankStats{Samples: 4, ProbeSamples: 3, Failures: 4}).ProbeOnlyDead() {
		t.Fatalf("stats carrying real samples must never be probe-dead")
	}
	if (AutoRankStats{}).ProbeOnlyDead() {
		t.Fatalf("empty stats must not be probe-dead")
	}
}

// 探测确认挂掉的候选不该再被探索选中，否则真实用户请求只是去重复一次已知失败。
func TestExploreCandidatesExcludesProbeDead(t *testing.T) {
	AutoRankReset()
	dead := newAutoCandidate(
		model.GroupItem{ChannelID: 21, ModelName: "dead"},
		AutoRankStats{Samples: 3, ProbeSamples: 3, Failures: 3}, 0)
	alive := newAutoCandidate(
		model.GroupItem{ChannelID: 22, ModelName: "alive"},
		AutoRankStats{Samples: 2, ProbeSamples: 2}, 100)

	pool := exploreCandidates([]autoCandidate{dead, alive}, nil)
	if len(pool) != 1 || pool[0].item.ModelName != "alive" {
		t.Fatalf("expected only the alive candidate to be explorable, got %d entries", len(pool))
	}
	if empty := exploreCandidates([]autoCandidate{dead}, nil); len(empty) != 0 {
		t.Fatalf("expected empty pool when every candidate is probe-dead, got %d", len(empty))
	}
}

// 没有值得探索的目标时，探索额度必须留到下次，而不是白扣一次。
func TestExploreCreditPreservedWhenNothingWorthExploring(t *testing.T) {
	cfg := autoScheduleConfig{
		ExploreRatio:           1,
		MinSamples:             3,
		LatencyRatio:           1.5,
		ChannelMaxShare:        0.7,
		ModelMaxShare:          0.8,
		SoftmaxTemp:            5,
		HealthThreshold:        0.85,
		FeedbackUpdateInterval: 10,
	}

	AutoRankReset()
	dead := newAutoCandidate(
		model.GroupItem{ChannelID: 31, ModelName: "dead"},
		AutoRankStats{Samples: 3, ProbeSamples: 3, Failures: 3}, 0)
	scheduleAutoCandidates(101, []autoCandidate{dead}, cfg)
	if credit := autoScheduleExploreCredit(101); credit < 1 {
		t.Fatalf("explore credit must be preserved when nothing is explorable, got %v", credit)
	}

	AutoRankReset()
	fresh := newAutoCandidate(model.GroupItem{ChannelID: 32, ModelName: "fresh"}, AutoRankStats{}, 0)
	scheduleAutoCandidates(102, []autoCandidate{fresh}, cfg)
	if credit := autoScheduleExploreCredit(102); credit >= 1 {
		t.Fatalf("explore credit must be consumed on a real exploration, got %v", credit)
	}
}

func autoScheduleExploreCredit(groupID int) float64 {
	s := getOrCreateAutoSchedule(groupID)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exploreCredit
}
