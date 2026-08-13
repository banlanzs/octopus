package balancer

import (
	"math"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func TestRecordAutoSampleWindowStats(t *testing.T) {
	AutoRankReset()
	for i := 0; i < 3; i++ {
		RecordAutoSample(1, "gpt-4o", true, 500)
	}
	for i := 0; i < 2; i++ {
		RecordAutoSample(1, "gpt-4o", false, 0)
	}
	st := GetAutoRankStats(1, "gpt-4o")
	if st.Samples != 5 {
		t.Fatalf("expected 5 samples, got %d", st.Samples)
	}
	if st.Failures != 2 {
		t.Fatalf("expected 2 failures, got %d", st.Failures)
	}
	if st.SuccessRate < 0.59 || st.SuccessRate > 0.61 {
		t.Fatalf("expected success rate 0.6, got %v", st.SuccessRate)
	}
	if st.EWMALatencyMS != 500 {
		t.Fatalf("expected ewma latency 500, got %v", st.EWMALatencyMS)
	}
}

func TestRecordAutoSampleRingOverwrite(t *testing.T) {
	AutoRankReset()
	// 写满窗口（20 条失败），再写 1 条成功 → 最旧失败被覆盖
	for i := 0; i < AutoRankPhysicalCap; i++ {
		RecordAutoSample(2, "gpt-4o", false, 0)
	}
	RecordAutoSample(2, "gpt-4o", true, 100)
	st := GetAutoRankStats(2, "gpt-4o")
	if st.Samples != AutoRankPhysicalCap {
		t.Fatalf("expected %d samples, got %d", AutoRankPhysicalCap, st.Samples)
	}
	if st.Failures != AutoRankPhysicalCap-1 {
		t.Fatalf("expected %d failures, got %d", AutoRankPhysicalCap-1, st.Failures)
	}
}

func TestAutoRankStatsTimeWindowExpiry(t *testing.T) {
	AutoRankReset()
	e := getOrCreateAutoRank(autoRankKey(3, "gpt-4o"))
	e.mu.Lock()
	e.buf[0] = autoRankSample{at: time.Now().Add(-20 * time.Minute), success: true}
	e.buf[1] = autoRankSample{at: time.Now(), success: false}
	e.next = 2
	e.size = 2
	e.lastSeen = time.Now()
	e.mu.Unlock()

	st := GetAutoRankStats(3, "gpt-4o")
	if st.Samples != 1 {
		t.Fatalf("expected 1 valid sample after expiry, got %d", st.Samples)
	}
	if st.Failures != 1 {
		t.Fatalf("expected 1 failure, got %d", st.Failures)
	}
}

func TestScoreFromStats(t *testing.T) {
	fast := scoreFromStats(AutoRankStats{Samples: 10, SuccessRate: 1, EWMALatencyMS: 500})
	slow := scoreFromStats(AutoRankStats{Samples: 10, SuccessRate: 1, EWMALatencyMS: 3000})
	flaky := scoreFromStats(AutoRankStats{Samples: 10, SuccessRate: 0.5, EWMALatencyMS: 500})
	if fast <= slow {
		t.Fatalf("expected fast candidate to outrank slow: fast=%v slow=%v", fast, slow)
	}
	if flaky >= fast {
		t.Fatalf("expected flaky candidate to rank below stable: flaky=%v stable=%v", flaky, fast)
	}
}

func TestAutoRankLessTiers(t *testing.T) {
	no := AutoRankStats{}
	// minSamples 默认 5：5 条即“充足档”，4 条属“样本不足档”。
	enough := AutoRankStats{Samples: 5, SuccessRate: 0.9, EWMALatencyMS: 1000}
	fast := AutoRankStats{Samples: 6, SuccessRate: 1, EWMALatencyMS: 300}

	if autoRankLess(no, enough) {
		t.Fatal("expected no-sample candidate to rank after enough-sample candidate")
	}
	if !autoRankLess(enough, no) {
		t.Fatal("expected enough-sample candidate to rank before no-sample candidate")
	}
	if !autoRankLess(fast, enough) {
		t.Fatal("expected faster candidate to rank before slower one")
	}
	if autoRankLess(enough, fast) {
		t.Fatal("expected slower candidate not to rank before faster one")
	}
	if autoRankLess(fast, AutoRankStats{Samples: 6, SuccessRate: 1, EWMALatencyMS: 300}) {
		t.Fatal("expected equal candidates to keep original order")
	}
}

func TestAutoRankRestore(t *testing.T) {
	AutoRankReset()
	AutoRankRestore([]model.AutoRankSnapshot{
		{ChannelID: 6, ModelName: "gpt-4o", Samples: 10, Failures: 2, EWMALatencyMS: 800, LastSeenAt: time.Now()},
	})
	st := GetAutoRankStats(6, "gpt-4o")
	if st.Samples != 10 {
		t.Fatalf("expected 10 restored samples, got %d", st.Samples)
	}
	if st.Failures != 2 {
		t.Fatalf("expected 2 restored failures, got %d", st.Failures)
	}
	if st.EWMALatencyMS != 800 {
		t.Fatalf("expected restored ewma 800, got %v", st.EWMALatencyMS)
	}
}

func TestAutoRankRestoreSkipsExpiredSnapshot(t *testing.T) {
	AutoRankReset()
	AutoRankRestore([]model.AutoRankSnapshot{
		{GroupID: 3, ChannelID: 6, ModelName: "gpt-4o", Samples: 10, LastSeenAt: time.Now().Add(-2 * AutoRankTimeWindow)},
	})
	if st := GetAutoRankStatsForGroup(3, 6, "gpt-4o"); st.Samples != 0 {
		t.Fatalf("expected expired snapshot to stay discarded, got %+v", st)
	}
}

func TestAutoRankRestorePreservesFailureRatioWhenCapped(t *testing.T) {
	AutoRankReset()
	AutoRankRestore([]model.AutoRankSnapshot{
		{GroupID: 3, ChannelID: 6, ModelName: "gpt-4o", Samples: 40, Failures: 20, EWMALatencyMS: 800, LastSeenAt: time.Now()},
	})
	stats := GetAutoRankStatsForGroup(3, 6, "gpt-4o")
	if stats.Samples != AutoRankPhysicalCap || stats.Failures != AutoRankPhysicalCap/2 {
		t.Fatalf("expected capped window to preserve 50%% failure rate, got %+v", stats)
	}
}

func TestAutoRankAllStatsSkipsEmpty(t *testing.T) {
	AutoRankReset()
	RecordAutoSample(7, "gpt-4o", true, 100)
	_ = getOrCreateAutoRank(autoRankKey(8, "gpt-4o"))
	all := AutoRankAllStats()
	if len(all) != 1 {
		t.Fatalf("expected 1 stats entry, got %d", len(all))
	}
	if all[0].ChannelID != 7 || all[0].ModelName != "gpt-4o" {
		t.Fatalf("unexpected stats entry: %+v", all[0])
	}
}

func TestTargetFactorForRate(t *testing.T) {
	cases := []struct {
		rate float64
		want float64
	}{
		{0.95, 1.0}, // 健康
		{0.90, 1.0}, // 边界 >=0.9
		{0.85, 0.6}, // 轻微降级
		{0.70, 0.6}, // 边界 >=0.7
		{0.60, 0.4}, // 明显降级
		{0.50, 0.4}, // 边界 >=0.5
		{0.30, 0.3}, // 濒危
		{0.00, 0.3}, // 全失败
	}
	for _, c := range cases {
		if got := targetFactorForRate(c.rate); got != c.want {
			t.Fatalf("targetFactorForRate(%v) = %v, want %v", c.rate, got, c.want)
		}
	}
}

func TestChannelAggregateRate(t *testing.T) {
	agg := channelAggregate{}
	if r := agg.rate(); r != 1 {
		t.Fatalf("expected empty aggregate rate 1, got %v", r)
	}
	agg = channelAggregate{totalSamples: 10, totalFails: 2}
	if r := agg.rate(); r != 0.8 {
		t.Fatalf("expected rate 0.8, got %v", r)
	}
	agg = channelAggregate{totalSamples: 10, totalFails: 10}
	if r := agg.rate(); r != 0 {
		t.Fatalf("expected rate 0, got %v", r)
	}
}

func TestComputeChannelAggregates(t *testing.T) {
	AutoRankReset()
	for i := 0; i < 5; i++ {
		RecordAutoSample(1, "m1", true, 100)
	}
	for i := 0; i < 3; i++ {
		RecordAutoSample(1, "m2", false, 0)
	}
	items := []model.GroupItem{
		{ChannelID: 1, ModelName: "m1"},
		{ChannelID: 1, ModelName: "m2"},
		{ChannelID: 2, ModelName: "m3"},
	}
	aggs := computeChannelAggregates(items)
	a1 := aggs[1]
	if a1.models != 2 || a1.totalSamples != 8 || a1.totalFails != 3 || a1.failModels != 1 {
		t.Fatalf("unexpected channel 1 aggregate: %+v", a1)
	}
	a2 := aggs[2]
	if a2.models != 1 || a2.totalSamples != 0 || a2.failModels != 0 {
		t.Fatalf("unexpected channel 2 aggregate: %+v", a2)
	}
}

// 判据测试：单模型失败不惩罚 / 样本不足不惩罚 / 聚合健康不惩罚 / 多模型失败触发
func TestChannelAggregateFactorJudgment(t *testing.T) {
	AutoRankReset()

	// 单模型失败（failModels=1）：即使聚合成功率很低也不触发（模型级隔离）
	f := channelAggregateFactor(1, channelAggregate{models: 2, totalSamples: 20, totalFails: 10, failModels: 1})
	if f != 1.0 {
		t.Fatalf("expected single-model failure not to penalize channel, got %v", f)
	}

	// 样本不足（totalSamples=4 < 8）但多模型同时失败：置信度 4/8=0.5 →
	// 半额惩罚 target = 1-(1-0.3)*0.5 = 0.65（线性打折，平滑过渡）
	f = channelAggregateFactor(2, channelAggregate{models: 2, totalSamples: 4, totalFails: 4, failModels: 2})
	if math.Abs(f-0.65) > 1e-9 {
		t.Fatalf("expected under-sampled channel to get partial penalty 0.65, got %v", f)
	}

	// 聚合健康（rate >= 0.85）：不惩罚
	f = channelAggregateFactor(3, channelAggregate{models: 2, totalSamples: 20, totalFails: 1, failModels: 1})
	if f != 1.0 {
		t.Fatalf("expected healthy channel factor 1.0, got %v", f)
	}

	// 多模型同时失败：触发惩罚，首次调用即为目标系数 0.3（rate=0.2 < 0.5）
	f = channelAggregateFactor(4, channelAggregate{models: 2, totalSamples: 20, totalFails: 16, failModels: 2})
	if math.Abs(f-0.3) > 1e-9 {
		t.Fatalf("expected degraded channel factor 0.3, got %v", f)
	}

	// 多模型失败但聚合成功率仍在 [0.7,0.9)：目标 0.6
	f = channelAggregateFactor(5, channelAggregate{models: 3, totalSamples: 30, totalFails: 7, failModels: 3})
	if math.Abs(f-0.6) > 1e-9 {
		t.Fatalf("expected mildly degraded channel factor 0.6, got %v", f)
	}
}

// EWMA 平滑：目标稳定时收敛到目标；目标恢复时系数平滑回升
func TestChannelAggregateFactorEWMA(t *testing.T) {
	AutoRankReset()
	degraded := channelAggregate{models: 2, totalSamples: 20, totalFails: 16, failModels: 2}
	healthy := channelAggregate{models: 2, totalSamples: 20, totalFails: 1, failModels: 1}

	f := channelAggregateFactor(10, degraded)
	if math.Abs(f-0.3) > 1e-9 {
		t.Fatalf("expected first factor 0.3, got %v", f)
	}
	// 连续降级评估：收敛到 0.3
	for i := 0; i < 5; i++ {
		channelAggregateFactor(10, degraded)
	}
	if f = channelAggregateFactor(10, degraded); math.Abs(f-0.3) > 1e-9 {
		t.Fatalf("expected converged factor 0.3, got %v", f)
	}

	// 渠道恢复：系数平滑回升（0.7x+0.3 递推），不瞬跳
	prev := channelAggregateFactor(10, healthy)
	if prev >= 1.0 {
		t.Fatalf("expected recovery to start below 1.0, got %v", prev)
	}
	for i := 0; i < 10; i++ {
		cur := channelAggregateFactor(10, healthy)
		if cur <= prev {
			t.Fatalf("expected factor to rise monotonically during recovery: prev=%v cur=%v", prev, cur)
		}
		prev = cur
	}
	if prev >= 1.0 {
		t.Fatalf("expected factor to approach but not overshoot 1.0, got %v", prev)
	}
}

// 排序语义：被惩罚渠道的模型整体排后，但渠道内相对顺序保持
func TestAutoRankLessScoredChannelPenalty(t *testing.T) {
	AutoRankReset()
	// 渠道 A 健康（factor 1.0），渠道 B 被惩罚（factor 0.3）
	channelAggregateFactor(1, channelAggregate{models: 2, totalSamples: 20, totalFails: 1, failModels: 1})
	channelAggregateFactor(2, channelAggregate{models: 2, totalSamples: 20, totalFails: 16, failModels: 2})

	healthy := AutoRankStats{Samples: 10, SuccessRate: 0.9, EWMALatencyMS: 3000}     // 得分 87
	fastDegraded := AutoRankStats{Samples: 10, SuccessRate: 1.0, EWMALatencyMS: 100} // 得分 99.9，被渠道惩罚压制

	// 渠道惩罚后：被惩罚渠道的候选不应排在健康渠道候选之前（级联降级）
	if autoRankLessScored(2, fastDegraded, 1, healthy, 0) {
		t.Fatal("expected degraded channel candidate not to outrank healthy channel candidate")
	}
	// 健康渠道候选应排在惩罚渠道候选之前
	if !autoRankLessScored(1, healthy, 2, fastDegraded, 0) {
		t.Fatal("expected healthy channel candidate to outrank degraded channel candidate")
	}

	// 渠道内保序：同一惩罚渠道内，快的模型仍排在慢的之前
	slowDegraded := AutoRankStats{Samples: 10, SuccessRate: 1.0, EWMALatencyMS: 5000}
	if !autoRankLessScored(2, fastDegraded, 2, slowDegraded, 0) {
		t.Fatal("expected faster model to keep priority within same degraded channel")
	}
	if autoRankLessScored(2, slowDegraded, 2, fastDegraded, 0) {
		t.Fatal("expected slower model not to outrank faster model within same channel")
	}

	// 渠道惩罚不影响档位：无样本候选仍排最后（即使来自健康渠道）
	noSample := AutoRankStats{}
	if autoRankLessScored(1, noSample, 2, fastDegraded, 0) {
		t.Fatal("expected no-sample candidate to rank after sampled candidate regardless of penalty")
	}
}

// IsChannelDegraded 反映平滑系数状态
func TestIsChannelDegraded(t *testing.T) {
	AutoRankReset()
	channelAggregateFactor(1, channelAggregate{models: 2, totalSamples: 20, totalFails: 1, failModels: 1})
	channelAggregateFactor(2, channelAggregate{models: 2, totalSamples: 20, totalFails: 16, failModels: 2})
	if IsChannelDegraded(1) {
		t.Fatal("expected healthy channel not degraded")
	}
	if !IsChannelDegraded(2) {
		t.Fatal("expected degraded channel flagged")
	}
	// 无记录渠道视为健康
	if IsChannelDegraded(99) {
		t.Fatal("expected unknown channel treated as healthy")
	}
}

// 相对 TTFB 惩罚：只罚慢于中位数者，带慢速比上限与置信度打折
func TestTTFBPenalty(t *testing.T) {
	// median=1000ms，latency=2000ms（慢 2 倍），样本充足 → slow=1.0, penalty=20
	slow := AutoRankStats{Samples: 10, SuccessRate: 1, EWMALatencyMS: 2000}
	if p := ttfbPenalty(slow, 1000); p != 20 {
		t.Fatalf("expected penalty 20 for 2x slow, got %v", p)
	}
	// 样本不足（5 < 10）→ 置信度 0.5 → penalty 10
	if p := ttfbPenalty(AutoRankStats{Samples: 5, SuccessRate: 1, EWMALatencyMS: 2000}, 1000); p != 10 {
		t.Fatalf("expected half-confidence penalty 10, got %v", p)
	}
	// 快于中位数 → 不惩罚
	fast := AutoRankStats{Samples: 10, SuccessRate: 1, EWMALatencyMS: 500}
	if p := ttfbPenalty(fast, 1000); p != 0 {
		t.Fatalf("expected no penalty for faster candidate, got %v", p)
	}
	// 慢速比上限：慢 4 倍 → slow 截断到 2.0 → penalty 40
	if p := ttfbPenalty(AutoRankStats{Samples: 10, SuccessRate: 1, EWMALatencyMS: 4000}, 1000); p != 40 {
		t.Fatalf("expected capped penalty 40, got %v", p)
	}
	// 无样本/无效输入 → 0
	if p := ttfbPenalty(AutoRankStats{}, 1000); p != 0 {
		t.Fatalf("expected 0 penalty for empty stats, got %v", p)
	}
	if p := ttfbPenalty(slow, 0); p != 0 {
		t.Fatalf("expected 0 penalty when median unavailable, got %v", p)
	}
}

// 组内延迟中位数：少于 2 个有效样本时不启用相对惩罚
func TestGroupMedianLatencyMS(t *testing.T) {
	AutoRankReset()
	// 仅 1 个模型有延迟样本 → 返回 0
	RecordAutoSample(1, "m1", true, 800)
	items := []model.GroupItem{{ChannelID: 1, ModelName: "m1"}}
	if m := groupMedianLatencyMS(items); m != 0 {
		t.Fatalf("expected 0 median with single sample, got %v", m)
	}

	// 3 个模型：800 / 1200 / 2000 → 中位数 1200
	RecordAutoSample(2, "m2", true, 1200)
	RecordAutoSample(3, "m3", true, 2000)
	items = []model.GroupItem{
		{ChannelID: 1, ModelName: "m1"},
		{ChannelID: 2, ModelName: "m2"},
		{ChannelID: 3, ModelName: "m3"},
	}
	if m := groupMedianLatencyMS(items); m != 1200 {
		t.Fatalf("expected median 1200, got %v", m)
	}
}

// AutoRankHealthFor 只读摘要：样本/分数/降级/熔断状态正确聚合，
// 且不推进熔断状态机（Open 状态在冷却期内保持 Open）。
func TestAutoRankHealthFor(t *testing.T) {
	AutoRankReset()
	ReapBreakers(time.Now().Add(time.Hour), time.Minute)

	const ch, mdl = 7, "deepseek-v4-flash"
	// 无记录 → 零值，不降级、不熔断
	h := AutoRankHealthFor(ch, mdl)
	if h.Samples != 0 || h.Score != 0 || h.ChannelTripped || h.Degraded {
		t.Fatalf("expected empty health for unknown key, got %+v", h)
	}

	// 记录 3 次样本：2 成功 1 失败，延迟 1000ms
	for i := 0; i < 2; i++ {
		RecordAutoSample(ch, mdl, true, 1000)
	}
	RecordAutoSample(ch, mdl, false, 1000)
	h = AutoRankHealthFor(ch, mdl)
	if h.Samples != 3 || h.Failures != 1 {
		t.Fatalf("expected samples=3 failures=1, got %+v", h)
	}
	if math.Abs(h.SuccessRate-2.0/3.0) > 1e-9 {
		t.Fatalf("expected success rate 2/3, got %v", h.SuccessRate)
	}
	if h.EWMALatencyMS <= 0 || h.Score <= 0 {
		t.Fatalf("expected positive latency/score, got %+v", h)
	}

	// 渠道级熔断：连续失败达到阈值后 Open，健康摘要应带剩余冷却与次数
	for i := int64(0); i < channelThreshold(); i++ {
		RecordChannelFailure(ch, FailureHard)
	}
	h = AutoRankHealthFor(ch, mdl)
	if !h.ChannelTripped {
		t.Fatalf("expected channel tripped after threshold failures, got %+v", h)
	}
	if h.ChannelCooldownSec <= 0 {
		t.Fatalf("expected positive cooldown, got %+v", h)
	}
	if h.ChannelTripCount < 1 {
		t.Fatalf("expected trip count >= 1, got %+v", h)
	}
	// 只读：冷却期内状态不应被推进为 HalfOpen（无试探请求）
	tripped, _, _ := ChannelCircuitStatus(ch)
	if !tripped {
		t.Fatalf("read-only status must not advance Open -> HalfOpen")
	}
	AutoRankReset()
}

func TestAutoRankStatsAreIsolatedByGroup(t *testing.T) {
	AutoRankReset()
	RecordAutoSampleForGroup(11, 7, "gpt-5", true, 800, 250)

	group11 := GetAutoRankStatsForGroup(11, 7, "gpt-5")
	if group11.Samples != 1 || group11.EWMATTFBMS != 250 {
		t.Fatalf("expected group 11 sample, got %+v", group11)
	}
	if group12 := GetAutoRankStatsForGroup(12, 7, "gpt-5"); group12.Samples != 0 {
		t.Fatalf("expected group 12 to be isolated, got %+v", group12)
	}
}

func TestAutoScheduleDeterministicallyCoversColdCandidates(t *testing.T) {
	AutoRankReset()
	candidates := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 1, ModelName: "m1"}, AutoRankStats{}, 0),
		newAutoCandidate(model.GroupItem{ChannelID: 2, ModelName: "m2"}, AutoRankStats{}, 0),
		newAutoCandidate(model.GroupItem{ChannelID: 3, ModelName: "m3"}, AutoRankStats{}, 0),
	}
	seen := map[int]bool{}
	for i := 0; i < 11; i++ {
		ordered := scheduleAutoCandidates(21, candidates, testAutoScheduleConfig(0.2))
		seen[ordered[0].item.ChannelID] = true
	}
	if len(seen) != len(candidates) {
		t.Fatalf("expected bounded exploration to cover all candidates, saw channels %v", seen)
	}
}

func TestAutoScheduleSharesTrafficAcrossCompetitiveChannelsAndModels(t *testing.T) {
	AutoRankReset()
	candidates := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 1, ModelName: "m1"}, AutoRankStats{Samples: 20, SuccessRate: 1, SuccessConfidence: 0.96, EWMATTFBMS: 400}, 95.6),
		newAutoCandidate(model.GroupItem{ChannelID: 1, ModelName: "m2"}, AutoRankStats{Samples: 20, SuccessRate: 1, SuccessConfidence: 0.95, EWMATTFBMS: 420}, 95.0),
		newAutoCandidate(model.GroupItem{ChannelID: 2, ModelName: "m3"}, AutoRankStats{Samples: 20, SuccessRate: 1, SuccessConfidence: 0.95, EWMATTFBMS: 430}, 94.9),
	}
	counts := map[string]int{}
	channelCounts := map[int]int{}
	for i := 0; i < 120; i++ {
		ordered := scheduleAutoCandidates(22, candidates, testAutoScheduleConfig(0))
		first := ordered[0].item
		counts[first.ModelName]++
		channelCounts[first.ChannelID]++
	}
	for _, name := range []string{"m1", "m2", "m3"} {
		if counts[name] == 0 {
			t.Fatalf("expected competitive model %s to receive traffic, counts=%v", name, counts)
		}
	}
	if channelCounts[1] > 84 || channelCounts[2] == 0 {
		t.Fatalf("expected channel cap to prevent monopolization, channel counts=%v", channelCounts)
	}
}

func TestAutoScheduleKeepsTargetsDuringExploration(t *testing.T) {
	AutoRankReset()
	candidates := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 1, ModelName: "m1"}, AutoRankStats{Samples: 20, SuccessRate: 1, SuccessConfidence: 0.95, EWMATTFBMS: 400}, 95),
		newAutoCandidate(model.GroupItem{ChannelID: 2, ModelName: "m2"}, AutoRankStats{Samples: 20, SuccessRate: 1, SuccessConfidence: 0.95, EWMATTFBMS: 400}, 95),
	}
	scheduleAutoCandidates(24, candidates, testAutoScheduleConfig(1))
	for _, candidate := range candidates {
		stats := GetAutoDispatchStats(24, candidate.item.ChannelID, candidate.item.ModelName)
		if stats.TargetShare <= 0 {
			t.Fatalf("expected target share during exploration, got %+v", stats)
		}
	}
}

func TestAutoScheduleKeepsPoorCandidateOutOfMainTraffic(t *testing.T) {
	AutoRankReset()
	candidates := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 1, ModelName: "healthy"}, AutoRankStats{Samples: 20, SuccessRate: 1, SuccessConfidence: 0.96, EWMATTFBMS: 400}, 95.6),
		newAutoCandidate(model.GroupItem{ChannelID: 2, ModelName: "poor"}, AutoRankStats{Samples: 20, SuccessRate: 0.7, SuccessConfidence: 0.6, EWMATTFBMS: 900}, 59.1),
	}
	for i := 0; i < 30; i++ {
		ordered := scheduleAutoCandidates(23, candidates, testAutoScheduleConfig(0))
		if ordered[0].item.ModelName != "healthy" {
			t.Fatalf("poor candidate unexpectedly received main traffic: %+v", ordered[0])
		}
	}
}

func TestAutoScheduleUsesStableTieBreakers(t *testing.T) {
	AutoRankReset()
	stats := AutoRankStats{Samples: 20, SuccessRate: 1, SuccessConfidence: 0.95, EWMATTFBMS: 400, LastSeenAt: time.Now()}
	candidates := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 2, ModelName: "m2"}, stats, 95),
		newAutoCandidate(model.GroupItem{ChannelID: 1, ModelName: "m1"}, stats, 95),
	}

	for groupID := 1000; groupID < 1100; groupID++ {
		scheduleAutoCandidates(groupID, candidates, testAutoScheduleConfig(0))
		scheduleAutoCandidates(groupID, candidates, testAutoScheduleConfig(0))
		ordered := scheduleAutoCandidates(groupID, candidates, testAutoScheduleConfig(0))
		if got := ordered[0].item.ChannelID; got != 1 {
			t.Fatalf("expected stable lower-channel tie break, got channel %d", got)
		}
	}

	sameChannel := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 3, ModelName: "m2"}, stats, 95),
		newAutoCandidate(model.GroupItem{ChannelID: 3, ModelName: "m1"}, stats, 95),
	}
	for groupID := 1100; groupID < 1200; groupID++ {
		scheduleAutoCandidates(groupID, sameChannel, testAutoScheduleConfig(0))
		scheduleAutoCandidates(groupID, sameChannel, testAutoScheduleConfig(0))
		ordered := scheduleAutoCandidates(groupID, sameChannel, testAutoScheduleConfig(0))
		if got := ordered[0].item.ModelName; got != "m1" {
			t.Fatalf("expected stable model-name tie break, got model %s", got)
		}
	}
}

func TestAutoRankReapRemovesIdleScheduleState(t *testing.T) {
	AutoRankReset()
	candidates := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 1, ModelName: "m1"}, AutoRankStats{}, 0),
		newAutoCandidate(model.GroupItem{ChannelID: 2, ModelName: "m2"}, AutoRankStats{}, 0),
	}
	scheduleAutoCandidates(99, candidates, testAutoScheduleConfig(0.2))
	if _, ok := globalAutoSchedule.Load(99); !ok {
		t.Fatal("expected schedule state to exist before reap")
	}

	AutoRankReap(time.Now().Add(time.Hour), 30*time.Minute)
	if _, ok := globalAutoSchedule.Load(99); ok {
		t.Fatal("expected idle schedule state to be reaped")
	}
}

func TestAutoScheduleReconcilesRemovedCandidates(t *testing.T) {
	AutoRankReset()
	candidates := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 1, ModelName: "m1"}, AutoRankStats{Samples: 20, SuccessConfidence: 0.95}, 95),
		newAutoCandidate(model.GroupItem{ChannelID: 2, ModelName: "m2"}, AutoRankStats{Samples: 20, SuccessConfidence: 0.95}, 95),
	}
	scheduleAutoCandidates(100, candidates, testAutoScheduleConfig(0))
	RecordAutoDispatch(100, 1, "m1")
	RecordAutoDispatch(100, 2, "m2")

	scheduleAutoCandidates(100, candidates[:1], testAutoScheduleConfig(0))
	remaining := GetAutoDispatchStats(100, 1, "m1")
	if remaining.Rank != 1 || remaining.TargetShare != 1 || remaining.ActualShare != 1 {
		t.Fatalf("expected single remaining candidate to own the schedule, got %+v", remaining)
	}
	if removed := GetAutoDispatchStats(100, 2, "m2"); removed != (AutoDispatchStats{}) {
		t.Fatalf("expected removed candidate state to be cleared, got %+v", removed)
	}
}

// A 方案：绝对健康度达标的候选经绝对通道进竞技池，即使相对差距不达标
func TestAutoScheduleAdmitsAbsoluteHealthCandidates(t *testing.T) {
	AutoRankReset()
	mk := func(ch int, name string, st AutoRankStats, score float64) autoCandidate {
		c := newAutoCandidate(model.GroupItem{ChannelID: ch, ModelName: name}, st, score)
		c.tier = autoCandidateTier(st, 3)
		return c
	}
	candidates := []autoCandidate{
		// A: best（conf 0.92, lat 400）——相对通道也必进
		mk(1, "m1", AutoRankStats{Samples: 20, SuccessConfidence: 0.92, EWMATTFBMS: 400}, 91.6),
		// B: conf=0.85 恰好达绝对阈值，但 latency 2000 > 400×1.5=600 → 相对通道不进
		mk(2, "m2", AutoRankStats{Samples: 20, SuccessConfidence: 0.85, EWMATTFBMS: 2000}, 84.5),
		// C: conf=0.86 ≥ 0.85 → 绝对通道进；latency 500 ≤ 600 → 相对通道也进
		mk(3, "m3", AutoRankStats{Samples: 20, SuccessConfidence: 0.86, EWMATTFBMS: 500}, 85.6),
		// D: conf=0.84 < 0.85 且 gap=0.08>0.02 → 双通道都不进
		mk(4, "m4", AutoRankStats{Samples: 20, SuccessConfidence: 0.84, EWMATTFBMS: 450}, 83.6),
	}
	cfg := testAutoScheduleConfig(0)
	cfg.SuccessGap = 0.02
	cfg.LatencyRatio = 1.5
	cfg.HealthThreshold = 0.85

	competitive := competitiveAutoCandidates(candidates, cfg)
	if len(competitive) != 3 {
		t.Fatalf("expected 3 competitive candidates (A/B/C), got %d: %+v", len(competitive), competitive)
	}
	seen := map[string]bool{}
	for _, c := range competitive {
		seen[c.item.ModelName] = true
	}
	for _, name := range []string{"m1", "m2", "m3"} {
		if !seen[name] {
			t.Fatalf("expected %s in competitive pool, got %v", name, seen)
		}
	}
	if seen["m4"] {
		t.Fatalf("expected m4 (below absolute threshold and relative gap) to stay out, got %v", seen)
	}
}

// A 方案：HealthThreshold=0 禁用绝对通道，退化为纯相对差距判据
func TestAutoScheduleHealthThresholdZeroDisablesAbsoluteChannel(t *testing.T) {
	AutoRankReset()
	mk := func(ch int, name string, st AutoRankStats, score float64) autoCandidate {
		c := newAutoCandidate(model.GroupItem{ChannelID: ch, ModelName: name}, st, score)
		c.tier = autoCandidateTier(st, 3)
		return c
	}
	candidates := []autoCandidate{
		mk(1, "m1", AutoRankStats{Samples: 20, SuccessConfidence: 0.92, EWMATTFBMS: 400}, 91.6),
		// conf=0.86 ≥ 0.85 但 gap=0.06>0.02：绝对通道开时进池，禁用后出池
		mk(3, "m3", AutoRankStats{Samples: 20, SuccessConfidence: 0.86, EWMATTFBMS: 500}, 85.6),
	}
	cfg := testAutoScheduleConfig(0)
	cfg.SuccessGap = 0.02
	cfg.HealthThreshold = 0

	competitive := competitiveAutoCandidates(candidates, cfg)
	if len(competitive) != 1 {
		t.Fatalf("expected only best candidate with absolute channel disabled, got %d: %+v", len(competitive), competitive)
	}
}

// B 方案：候选实际分配持续超额（dispatched 份额远超 targetShare）时，
// feedbackPenalty 降到 <1，debt 增大，其他候选开始收到流量
func TestAutoScheduleFeedbackPenaltyCorrectsMonopoly(t *testing.T) {
	AutoRankReset()
	const gid = 200
	stats := AutoRankStats{Samples: 20, SuccessRate: 1, SuccessConfidence: 0.95, EWMATTFBMS: 400, LastSeenAt: time.Now()}
	candidates := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 1, ModelName: "m1"}, stats, 95),
		newAutoCandidate(model.GroupItem{ChannelID: 2, ModelName: "m2"}, stats, 95),
	}
	cfg := testAutoScheduleConfig(0)
	cfg.FeedbackEwma = 0.5
	cfg.FeedbackTolerance = 0.05

	// m1 垄断：先 20 次 dispatch（触发第一轮 EWMA，ewma=0.5 未超额），
	// 再 10 次（触发第二轮，ewma=0.75 → excess=0.2 → penalty=0.94）
	for i := 0; i < 20; i++ {
		RecordAutoDispatch(gid, 1, "m1")
	}
	scheduleAutoCandidates(gid, candidates, cfg)
	for i := 0; i < 10; i++ {
		RecordAutoDispatch(gid, 1, "m1")
	}
	scheduleAutoCandidates(gid, candidates, cfg)

	s := getOrCreateAutoSchedule(gid)
	s.mu.Lock()
	penalty := s.candidates["1:m1"].feedbackPenalty
	s.mu.Unlock()
	if penalty >= 1.0 {
		t.Fatalf("expected monopoly candidate penalty < 1.0, got %v", penalty)
	}

	// 之后 m2 应开始收到主流量
	counts := map[string]int{}
	for i := 0; i < 60; i++ {
		ordered := scheduleAutoCandidates(gid, candidates, cfg)
		counts[ordered[0].item.ModelName]++
	}
	if counts["m2"] == 0 {
		t.Fatalf("expected m2 to receive traffic after penalty, counts=%v", counts)
	}
}

// B 方案：feedbackPenalty 触达 feedbackMin 地板（不会把候选完全斩断）
func TestAutoScheduleFeedbackPenaltyFloor(t *testing.T) {
	AutoRankReset()
	const gid = 300
	s := getOrCreateAutoSchedule(gid)
	s.mu.Lock()
	s.candidates["1:m1"] = &autoDispatchState{feedbackPenalty: 1.0, targetShare: 0.2, activeDispatched: 99}
	s.candidates["2:m2"] = &autoDispatchState{feedbackPenalty: 1.0, targetShare: 0.8, activeDispatched: 1}
	s.totalActiveDispatched = 100
	s.mu.Unlock()

	cfg := testAutoScheduleConfig(0)
	cfg.FeedbackEwma = 0.9
	cfg.FeedbackTolerance = 0
	cfg.FeedbackPenalty = 1.0

	updateAutoRankFeedbackEWMA(s, cfg)

	s.mu.Lock()
	penalty := s.candidates["1:m1"].feedbackPenalty
	s.mu.Unlock()
	if penalty < feedbackMin-1e-9 || penalty > feedbackMin+1e-9 {
		t.Fatalf("expected penalty clamped to floor %v, got %v", feedbackMin, penalty)
	}
}

// B 方案：feedbackEnabled=false 时 penalty 保持 1.0，行为与原 debt 一致
func TestAutoScheduleFeedbackDisabledDoesNothing(t *testing.T) {
	AutoRankReset()
	const gid = 400
	stats := AutoRankStats{Samples: 20, SuccessRate: 1, SuccessConfidence: 0.95, EWMATTFBMS: 400, LastSeenAt: time.Now()}
	candidates := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 1, ModelName: "m1"}, stats, 95),
		newAutoCandidate(model.GroupItem{ChannelID: 2, ModelName: "m2"}, stats, 95),
	}
	cfg := testAutoScheduleConfig(0)
	cfg.FeedbackEnabled = false

	for i := 0; i < 30; i++ {
		RecordAutoDispatch(gid, 1, "m1")
	}
	scheduleAutoCandidates(gid, candidates, cfg)

	s := getOrCreateAutoSchedule(gid)
	s.mu.Lock()
	penalty := s.candidates["1:m1"].feedbackPenalty
	s.mu.Unlock()
	if penalty != 1.0 {
		t.Fatalf("expected penalty stay 1.0 when feedback disabled, got %v", penalty)
	}
}

// A+B 协同：低于绝对阈值且相对差距不达标的候选不进主流量，即使 B 开启
func TestAutoScheduleCombinedHealthAndFeedback(t *testing.T) {
	AutoRankReset()
	const gid = 500
	healthy := AutoRankStats{Samples: 20, SuccessRate: 1, SuccessConfidence: 0.95, EWMATTFBMS: 400, LastSeenAt: time.Now()}
	below := AutoRankStats{Samples: 20, SuccessRate: 1, SuccessConfidence: 0.84, EWMATTFBMS: 450, LastSeenAt: time.Now()}
	candidates := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 1, ModelName: "h1"}, healthy, 94.6),
		newAutoCandidate(model.GroupItem{ChannelID: 2, ModelName: "h2"}, healthy, 94.6),
		newAutoCandidate(model.GroupItem{ChannelID: 3, ModelName: "below"}, below, 83.6),
	}
	cfg := testAutoScheduleConfig(0) // exploreRatio=0：主流量只走 fair/quality

	// 先给 below 一些 dispatch（模拟探索期积累的转发），确认 feedback 不会把它拉进主流量
	for i := 0; i < 15; i++ {
		RecordAutoDispatch(gid, 3, "below")
	}
	for i := 0; i < 100; i++ {
		ordered := scheduleAutoCandidates(gid, candidates, cfg)
		if ordered[0].item.ModelName == "below" {
			t.Fatalf("below-threshold candidate unexpectedly became primary: %+v", ordered[0])
		}
	}
}

func testAutoScheduleConfig(exploreRatio float64) autoScheduleConfig {
	return autoScheduleConfig{
		ExploreRatio:           exploreRatio,
		MinSamples:             3,
		SuccessGap:             0.02,
		LatencyRatio:           1.5,
		ChannelMaxShare:        0.7,
		ModelMaxShare:          0.8,
		SoftmaxTemp:            5.0,
		HealthThreshold:        0.85,
		FeedbackEnabled:        true,
		FeedbackEwma:           0.3,
		FeedbackTolerance:      0.1,
		FeedbackPenalty:        0.3,
		FeedbackUpdateInterval: 10,
	}
}

// 探索优化：探索选择跳过渠道级熔断候选，避免探索机会被 relay 层
// SkipCircuitBreak 跳过（不发请求、不积累样本）。
func TestAutoScheduleExploreSkipsCircuitTripped(t *testing.T) {
	Reset()
	AutoRankReset()
	const gid = 600
	candidates := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 1, ModelName: "tripped"}, AutoRankStats{}, 0),
		newAutoCandidate(model.GroupItem{ChannelID: 2, ModelName: "healthy"}, AutoRankStats{}, 0),
	}
	// 触发 channel 1 渠道级熔断
	for i := int64(0); i < channelThreshold(); i++ {
		RecordChannelFailure(1, FailureHard)
	}
	cfg := testAutoScheduleConfig(1.0) // 100% 探索：每次调度必进探索分支

	seen := map[string]int{}
	for i := 0; i < 10; i++ {
		ordered := scheduleAutoCandidates(gid, candidates, cfg)
		seen[ordered[0].item.ModelName]++
	}
	if seen["tripped"] > 0 {
		t.Fatalf("探索不应选择熔断候选, got: %v", seen)
	}
	if seen["healthy"] == 0 {
		t.Fatalf("探索应选择健康候选, got: %v", seen)
	}
}

// 探索优化：due 池候选全部熔断时回退原池（保持原行为，不 panic）。
func TestAutoScheduleExploreAllTrippedFallsBack(t *testing.T) {
	Reset()
	AutoRankReset()
	const gid = 601
	candidates := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 1, ModelName: "a"}, AutoRankStats{}, 0),
		newAutoCandidate(model.GroupItem{ChannelID: 2, ModelName: "b"}, AutoRankStats{}, 0),
	}
	for i := int64(0); i < channelThreshold(); i++ {
		RecordChannelFailure(1, FailureHard)
		RecordChannelFailure(2, FailureHard)
	}
	cfg := testAutoScheduleConfig(1.0)

	ordered := scheduleAutoCandidates(gid, candidates, cfg)
	if len(ordered) == 0 || ordered[0].item.ChannelID == 0 {
		t.Fatalf("全熔断时应回退原池仍返回主候选, got: %+v", ordered)
	}
	// 全熔断时探索池为空、竞技池为空 → 回退到排序后的首位候选（channel 1）
	if ordered[0].item.ChannelID != 1 {
		t.Fatalf("回退池应保持原顺序, got: %+v", ordered[0])
	}
}

// 优化 1：SampleTrail 精确重建 —— 时间分布/失败位置/逐条延迟与原始一致。
func TestAutoRankRestorePrecise(t *testing.T) {
	AutoRankReset()
	now := time.Now()
	// 从旧到新：✓500ms / ✓800ms / ✗ / ✓600ms，age 单调递减
	trail := `[{"age_ms":9000,"ok":true,"d":500,"t":200},{"age_ms":6000,"ok":true,"d":800,"t":300},{"age_ms":3000,"ok":false,"d":0,"t":0},{"age_ms":0,"ok":true,"d":600,"t":250}]`
	AutoRankRestore([]model.AutoRankSnapshot{
		{GroupID: 8, ChannelID: 80, ModelName: "gpt-4o", Samples: 4, Failures: 1, SampleTrail: trail, LastSeenAt: now},
	})
	st := GetAutoRankStatsForGroup(8, 80, "gpt-4o")
	if st.Samples != 4 || st.Failures != 1 {
		t.Fatalf("expected precise restore 4 samples 1 failure, got %+v", st)
	}
	// EWMA 由逐条延迟重建（非全量均值）：500 → 0.7*500+0.3*800=590 → 0.7*590+0.3*600=593
	if st.EWMALatencyMS < 592 || st.EWMALatencyMS > 594 {
		t.Fatalf("expected EWMA rebuilt from per-sample latency ≈593, got %v", st.EWMALatencyMS)
	}
	// TTFB EWMA：200 → 230 → 236
	if st.EWMATTFBMS < 235 || st.EWMATTFBMS > 237 {
		t.Fatalf("expected TTFB EWMA ≈236, got %v", st.EWMATTFBMS)
	}
}

// 优化 1：trail 缺失/损坏/乱序时回退近似重建（旧版行为），不 panic、不丢数据。
func TestAutoRankRestoreFallsBackToApprox(t *testing.T) {
	// 空 trail（旧版快照行）
	AutoRankReset()
	AutoRankRestore([]model.AutoRankSnapshot{
		{ChannelID: 81, ModelName: "gpt-4o", Samples: 10, Failures: 2, EWMALatencyMS: 800, LastSeenAt: time.Now()},
	})
	if st := GetAutoRankStats(81, "gpt-4o"); st.Samples != 10 || st.Failures != 2 || st.EWMALatencyMS != 800 {
		t.Fatalf("expected approx fallback for empty trail, got %+v", st)
	}

	// 非法 JSON
	AutoRankReset()
	AutoRankRestore([]model.AutoRankSnapshot{
		{ChannelID: 82, ModelName: "gpt-4o", Samples: 5, Failures: 1, SampleTrail: "{not-json", EWMALatencyMS: 700, LastSeenAt: time.Now()},
	})
	if st := GetAutoRankStats(82, "gpt-4o"); st.Samples != 5 || st.Failures != 1 || st.EWMALatencyMS != 700 {
		t.Fatalf("expected approx fallback for corrupt trail, got %+v", st)
	}

	// age 乱序（新在前旧在后）
	AutoRankReset()
	bad := `[{"age_ms":0,"ok":true,"d":100},{"age_ms":5000,"ok":false,"d":0}]`
	AutoRankRestore([]model.AutoRankSnapshot{
		{ChannelID: 83, ModelName: "gpt-4o", Samples: 2, Failures: 1, SampleTrail: bad, EWMALatencyMS: 400, LastSeenAt: time.Now()},
	})
	if st := GetAutoRankStats(83, "gpt-4o"); st.Samples != 2 || st.Failures != 1 {
		t.Fatalf("expected approx fallback for unordered trail, got %+v", st)
	}
}

// 优化 3：探索欠采样优先 —— 0 样本候选持续优先于 1 样本候选。
func TestExplorePrefersUnderSampled(t *testing.T) {
	Reset() // 清渠道熔断状态：探索剔除逻辑会查 breaker，避免前序测试残留污染
	AutoRankReset()
	const gid = 700
	candidates := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 1, ModelName: "m1"}, AutoRankStats{Samples: 1, SuccessRate: 1, SuccessConfidence: 1}, 99),
		newAutoCandidate(model.GroupItem{ChannelID: 2, ModelName: "m2"}, AutoRankStats{}, 0),
	}
	cfg := testAutoScheduleConfig(1.0) // 100% 探索
	seen := map[string]int{}
	for i := 0; i < 10; i++ {
		ordered := scheduleAutoCandidates(gid, candidates, cfg)
		seen[ordered[0].item.ModelName]++
	}
	if seen["m1"] > 0 {
		t.Fatalf("探索应优先欠采样的 m2(0 样本), 却选了 m1: %v", seen)
	}
	if seen["m2"] == 0 {
		t.Fatalf("m2 应持续被探索, got: %v", seen)
	}
}

// 优化 6：单候选快路径仍正确记账（rank/targetShare/顺序稳定）。
func TestScheduleSingleCandidateFastPath(t *testing.T) {
	AutoRankReset()
	candidates := []autoCandidate{
		newAutoCandidate(model.GroupItem{ChannelID: 5, ModelName: "m1"}, AutoRankStats{Samples: 20, SuccessRate: 1, SuccessConfidence: 0.95}, 95),
	}
	ordered := scheduleAutoCandidates(800, candidates, testAutoScheduleConfig(0))
	if len(ordered) != 1 || ordered[0].item.ChannelID != 5 {
		t.Fatalf("expected single candidate passthrough, got %+v", ordered)
	}
	stats := GetAutoDispatchStats(800, 5, "m1")
	if stats.Rank != 1 || stats.TargetShare != 1.0 {
		t.Fatalf("expected rank=1 targetShare=1.0 on fast path, got %+v", stats)
	}
	for i := 0; i < 5; i++ {
		if o := scheduleAutoCandidates(800, candidates, testAutoScheduleConfig(0)); len(o) != 1 {
			t.Fatalf("fast path unstable: %+v", o)
		}
	}
}

// 优化 5：min_samples 无设置记录时回退默认 5。
func TestAutoRankMinSamplesDefault(t *testing.T) {
	if got := autoRankMinSamples(); got != 5 {
		t.Fatalf("expected fallback min samples 5, got %d", got)
	}
}

// 可观测性：TrailSummary 时间线（✓/✗/p）从旧到新，HealthFor 透传。
func TestAutoRankTrailSummary(t *testing.T) {
	AutoRankReset()
	RecordAutoSample(9, "m1", true, 100)
	RecordAutoSample(9, "m1", false, 0)
	RecordAutoProbeSampleForGroup(0, 9, "m1", true)
	RecordAutoSample(9, "m1", true, 120)
	sum := autoRankTrailForGroup(0, 9, "m1")
	if sum != "✓✗p✓" {
		t.Fatalf("expected trail summary ✓✗p✓, got %q", sum)
	}
	if h := AutoRankHealthFor(9, "m1"); h.TrailSummary != sum {
		t.Fatalf("expected health trail summary %q, got %q", sum, h.TrailSummary)
	}
}
