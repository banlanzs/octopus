package balancer

import (
	"math"
	"math/rand/v2"
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
	enough := AutoRankStats{Samples: 5, SuccessRate: 0.9, EWMALatencyMS: 1000}
	fast := AutoRankStats{Samples: 4, SuccessRate: 1, EWMALatencyMS: 300}

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
	if autoRankLess(fast, AutoRankStats{Samples: 4, SuccessRate: 1, EWMALatencyMS: 300}) {
		t.Fatal("expected equal candidates to keep original order")
	}
}

func TestPickUnderSampled(t *testing.T) {
	AutoRankReset()
	// c1 样本充足；c2/c3 无样本（最需探索）
	for i := 0; i < 5; i++ {
		RecordAutoSample(1, "gpt-4o", true, 100)
	}

	items := []model.GroupItem{
		{ChannelID: 1, ModelName: "gpt-4o"},
		{ChannelID: 2, ModelName: "gpt-4o"},
		{ChannelID: 3, ModelName: "gpt-4o"},
	}
	// 确定性随机源：多个欠采样候选应被随机轮换，而非固定返回第一个。
	seen := map[int]bool{}
	for seed := uint64(0); seed < 50; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed*7+1))
		idx := pickUnderSampled(items, 3, rng.IntN)
		if idx != 1 && idx != 2 {
			t.Fatalf("expected under-sampled index 1 or 2, got %d", idx)
		}
		seen[idx] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected both under-sampled candidates to be picked across seeds, got %v", seen)
	}

	// 全部充分采样 → -1
	AutoRankReset()
	for i := 0; i < 5; i++ {
		RecordAutoSample(4, "gpt-4o", true, 100)
	}
	items2 := []model.GroupItem{{ChannelID: 4, ModelName: "gpt-4o"}}
	if idx := pickUnderSampled(items2, 3, rand.IntN); idx != -1 {
		t.Fatalf("expected no under-sampled candidate, got %d", idx)
	}
}

func TestAutoRankRestore(t *testing.T) {
	AutoRankReset()
	AutoRankRestore([]model.AutoRankSnapshot{
		{ChannelID: 6, ModelName: "gpt-4o", Samples: 10, Failures: 2, EWMALatencyMS: 800},
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
		{0.95, 1.0},  // 健康
		{0.90, 1.0},  // 边界 >=0.9
		{0.85, 0.6},  // 轻微降级
		{0.70, 0.6},  // 边界 >=0.7
		{0.60, 0.4},  // 明显降级
		{0.50, 0.4},  // 边界 >=0.5
		{0.30, 0.3},  // 濒危
		{0.00, 0.3},  // 全失败
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

	healthy := AutoRankStats{Samples: 10, SuccessRate: 0.9, EWMALatencyMS: 3000} // 得分 87
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
