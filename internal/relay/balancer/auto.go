package balancer

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
)

// AutoRankPhysicalCap 单个 (channel, model) 性能窗口的物理容量（编译期常量）。
// 窗口按时间窗滑动，容量不足时最旧样本被覆盖。
const AutoRankPhysicalCap = 20

// AutoRankTimeWindow 窗口样本过期时长（真实流量学习），与 POR 窗口一致。
const AutoRankTimeWindow = 10 * time.Minute

// Auto 自动排序：按实时性能维护质量排名，并在近似健康候选间公平分配首选流量。
// 失败降级由外层迭代器（relay.go 的 for iter.Next()）负责，与本模式正交。
// 总开关 auto_rank_enabled 关闭时回退为原始顺序（不排序、不学习）。
//
// 数据来源以被动学习（真实请求）为主：冷启动/低流量候选通过确定性有界探索
// （auto_rank_explore_ratio）在真实请求中按比例被优先尝试，从而积累样本参与排序。
// 主动探测默认关闭——中转站通常对无业务意图的极小请求（ping + 1 token）风控；
// 用户显式开启（全局 auto_rank_probe_enabled + 渠道 probe_enabled）后，探测样本
// 只补成功率，不写延迟、不推进排序档位（见 AutoRankStats.RealSamples）。
type Auto struct {
	GroupID int
}

func (b *Auto) Candidates(items []model.GroupItem) []model.GroupItem {
	n := len(items)
	if n == 0 {
		return nil
	}
	result := make([]model.GroupItem, n)
	copy(result, items)
	if !autoRankEnabled() {
		return result
	}
	statsByKey := make(map[string]AutoRankStats, n)
	for _, item := range result {
		statsByKey[autoCandidateKey(item.ChannelID, item.ModelName)] = GetAutoRankStatsForGroup(b.GroupID, item.ChannelID, item.ModelName)
	}
	// 渠道聚合修正（纯果驱动）：先按组内渠道聚合模型窗口，更新各渠道的
	// 平滑系数（副作用），再按"模型得分 × 渠道系数 − 相对 TTFB 惩罚"排序。
	aggregates := computeChannelAggregatesForGroup(b.GroupID, result)
	for channelID, agg := range aggregates {
		channelAggregateFactorForGroup(b.GroupID, channelID, agg)
	}
	medianMS := groupMedianLatencyMSForGroup(b.GroupID, result)
	ranked := make([]autoCandidate, 0, n)
	for _, item := range result {
		stats := statsByKey[autoCandidateKey(item.ChannelID, item.ModelName)]
		ranked = append(ranked, newAutoCandidate(item, stats, effectiveScoreForGroup(b.GroupID, item.ChannelID, stats, medianMS)))
	}
	ordered := scheduleAutoCandidates(b.GroupID, ranked, defaultAutoScheduleConfig())
	for i := range ordered {
		result[i] = ordered[i].item
	}
	return result
}

func autoRankEnabled() bool {
	enabled, err := op.SettingGetBool(model.SettingKeyAutoRankEnabled)
	return err == nil && enabled
}

// autoRankExploreRatio 返回探索比例（0~1）。读取 auto_rank_explore_ratio（百分比）。
func autoRankExploreRatio() float64 {
	pct, err := op.SettingGetInt(model.SettingKeyAutoRankExploreRatio)
	if err != nil {
		return 0.1
	}
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		pct = 100
	}
	return float64(pct) / 100.0
}

// autoRankSuccessGap 返回竞技池成功率差距门槛（0~1）。读取 auto_rank_success_gap（百分比）。
func autoRankSuccessGap() float64 {
	pct, err := op.SettingGetInt(model.SettingKeyAutoRankSuccessGap)
	if err != nil {
		return defaultAutoSuccessGap
	}
	return clampPct(pct, defaultAutoSuccessGap)
}

// autoRankLatencyRatio 返回竞技池延迟倍率门槛（≥1）。读取 auto_rank_latency_ratio（百分比）。
func autoRankLatencyRatio() float64 {
	pct, err := op.SettingGetInt(model.SettingKeyAutoRankLatencyRatio)
	if err != nil {
		return defaultAutoLatencyRatio
	}
	if pct < 100 {
		return defaultAutoLatencyRatio
	}
	return float64(pct) / 100.0
}

// autoRankHealthThreshold 返回竞技池绝对健康度阈值（Wilson 下界，0~1）。
// 读取 auto_rank_health_threshold（百分比）。0 表示禁用绝对通道（退化纯相对差距判据）。
func autoRankHealthThreshold() float64 {
	pct, err := op.SettingGetInt(model.SettingKeyAutoRankHealthThreshold)
	if err != nil {
		return 0.85
	}
	if pct < 0 {
		return 0.85
	}
	if pct > 100 {
		pct = 100
	}
	return float64(pct) / 100.0
}

// autoRankChannelMaxShare 返回单渠道目标份额上限（0~1）。读取 auto_rank_channel_max_share（百分比）。
func autoRankChannelMaxShare() float64 {
	pct, err := op.SettingGetInt(model.SettingKeyAutoRankChannelMaxShare)
	if err != nil {
		return defaultAutoChannelMaxShare
	}
	return clampPct(pct, defaultAutoChannelMaxShare)
}

// autoRankModelMaxShare 返回单渠道内单模型目标份额上限（0~1）。读取 auto_rank_model_max_share（百分比）。
func autoRankModelMaxShare() float64 {
	pct, err := op.SettingGetInt(model.SettingKeyAutoRankModelMaxShare)
	if err != nil {
		return defaultAutoModelMaxShare
	}
	return clampPct(pct, defaultAutoModelMaxShare)
}

// autoRankSoftmaxTemp 返回公平调度 softmax 温度（×10 存储）。读取 auto_rank_softmax_temp。
func autoRankSoftmaxTemp() float64 {
	v, err := op.SettingGetInt(model.SettingKeyAutoRankSoftmaxTemp)
	if err != nil {
		return autoSoftmaxTemperature
	}
	if v < 10 {
		return autoSoftmaxTemperature
	}
	return float64(v) / 10.0
}

// autoRankFeedbackEnabled 返回实际分配反馈纠偏开关。
func autoRankFeedbackEnabled() bool {
	enabled, err := op.SettingGetBool(model.SettingKeyAutoRankFeedbackEnabled)
	return err == nil && enabled
}

// autoRankFeedbackEwma 返回 actualShare EWMA 新样本权重（0~1）。读取 auto_rank_feedback_ewma（百分比）。
func autoRankFeedbackEwma() float64 {
	pct, err := op.SettingGetInt(model.SettingKeyAutoRankFeedbackEwma)
	if err != nil {
		return 0.3
	}
	if pct < 1 || pct > 99 {
		return 0.3
	}
	return float64(pct) / 100.0
}

// autoRankFeedbackTolerance 返回 actualShare 超额容忍度（0~1）。读取 auto_rank_feedback_tolerance（百分比）。
func autoRankFeedbackTolerance() float64 {
	pct, err := op.SettingGetInt(model.SettingKeyAutoRankFeedbackTolerance)
	if err != nil {
		return 0.1
	}
	return clampPct(pct, 0.1)
}

// autoRankFeedbackPenalty 返回超额降权强度（0~1/单位超额）。读取 auto_rank_feedback_penalty（百分比）。
func autoRankFeedbackPenalty() float64 {
	pct, err := op.SettingGetInt(model.SettingKeyAutoRankFeedbackPenalty)
	if err != nil {
		return 0.3
	}
	return clampPct(pct, 0.3)
}

// clampPct 把百分比整数钳制到 [0,100] 并转 0~1 浮点。
func clampPct(pct int, def float64) float64 {
	if pct < 0 {
		return def
	}
	if pct > 100 {
		pct = 100
	}
	return float64(pct) / 100.0
}

type autoRankSample struct {
	at      time.Time
	success bool
	// probe 标记该样本来自主动探测（max_tokens=1 的极小请求）而非真实转发。
	// 探测样本只计入成功率窗口，不参与延迟 EWMA、不推进"样本是否充足"判定。
	probe  bool
	durMS  int64
	ttfbMS int64
}

// autoRankEntry 单个 (groupID, channelID, modelName) 的性能学习窗口。
// 数据面（relay）在每次候选最终结果点调用 RecordAutoSample；控制面（task）周期
// 调用 GetAutoRankStatsForGroup 读统计落库，并在开启主动探测时用
// RecordAutoProbeSampleForGroup 补样本。纯内存、重启清空，
// 持久化恢复由 AutoRankRestore 从 AutoRankSnapshot 重建近似窗口。
type autoRankEntry struct {
	mu       sync.Mutex
	buf      [AutoRankPhysicalCap]autoRankSample
	next     int
	size     int
	lastSeen time.Time // 最近一次样本时间，用于内存回收
}

var globalAutoRank sync.Map // key: "groupID/channelID:modelName" -> *autoRankEntry

func autoRankKey(channelID int, modelName string) string {
	return fmt.Sprintf("%d:%s", channelID, modelName)
}

func autoRankGroupKey(groupID, channelID int, modelName string) string {
	if groupID <= 0 {
		return autoRankKey(channelID, modelName)
	}
	return fmt.Sprintf("%d/%d:%s", groupID, channelID, modelName)
}

func getOrCreateAutoRank(key string) *autoRankEntry {
	if v, ok := globalAutoRank.Load(key); ok {
		return v.(*autoRankEntry)
	}
	e := &autoRankEntry{}
	actual, _ := globalAutoRank.LoadOrStore(key, e)
	return actual.(*autoRankEntry)
}

func parseAutoRankKey(key string) (channelID int, modelName string) {
	prefix, rest, found := strings.Cut(key, ":")
	if !found {
		return 0, ""
	}
	for _, c := range prefix {
		if c < '0' || c > '9' {
			return 0, ""
		}
	}
	fmt.Sscanf(prefix, "%d", &channelID)
	return channelID, rest
}

func parseAutoRankGroupKey(key string) (groupID, channelID int, modelName string) {
	groupPrefix, candidateKey, found := strings.Cut(key, "/")
	if !found {
		channelID, modelName = parseAutoRankKey(key)
		return 0, channelID, modelName
	}
	if _, err := fmt.Sscanf(groupPrefix, "%d", &groupID); err != nil {
		return 0, 0, ""
	}
	channelID, modelName = parseAutoRankKey(candidateKey)
	return groupID, channelID, modelName
}

// AutoRankStats 候选性能摘要，供排序打分与落库快照使用。
type AutoRankStats struct {
	Samples int
	// ProbeSamples Samples 中来自主动探测的条数（Samples 的子集）。
	ProbeSamples      int
	Failures          int
	SuccessRate       float64
	SuccessConfidence float64
	EWMALatencyMS     float64
	EWMATTFBMS        float64
	LastSeenAt        time.Time
}

// RealSamples 返回真实转发样本数（窗口样本扣除主动探测）。
//
// 所有"样本是否足够"的判定（排序档位 tier、探索池 due）都必须走这里：
// 探测请求没有有效延迟观测（durMS=0），若让它把候选推进竞技池，该候选会
// 因为"零延迟"在 effectiveScore 上虚高，抢走本该属于真实快速候选的份额。
// 而成功率类判定（SuccessRate/SuccessConfidence/渠道聚合）用 Samples 全量，
// 这正是主动探测的价值所在——没有流量时也能发现候选已经挂了。
func (s AutoRankStats) RealSamples() int {
	if n := s.Samples - s.ProbeSamples; n > 0 {
		return n
	}
	return 0
}

// ProbeOnlyDead 报告候选是否"只有探测样本，且探测全部失败"。
//
// 这是主动探测唯一有资格单独下的结论：没有任何真实转发数据，而每一次探测都
// 失败——此时把真实用户请求送过去只是重复一次已知的失败。调度侧据此把候选从
// 探索池剔除（软避让，仍留在 failover 链末尾兜底），不做熔断：探测样本量小
// （默认单轮 10 个、同候选冷却 10 分钟），不足以支撑影响面更大的硬开关。
//
// 一旦有任何真实样本，判据立即失效——真实数据永远优先于探测。
func (s AutoRankStats) ProbeOnlyDead() bool {
	return s.RealSamples() == 0 && s.ProbeSamples > 0 && s.Failures >= s.ProbeSamples
}

// GetAutoRankStats 读取 (channel, model) 的性能统计（无记录返回零值）。
func GetAutoRankStats(channelID int, modelName string) AutoRankStats {
	return GetAutoRankStatsForGroup(0, channelID, modelName)
}

func GetAutoRankStatsForGroup(groupID, channelID int, modelName string) AutoRankStats {
	key := autoRankGroupKey(groupID, channelID, modelName)
	v, ok := globalAutoRank.Load(key)
	if !ok {
		return AutoRankStats{}
	}
	e := v.(*autoRankEntry)
	return e.stats(time.Now())
}

// AutoRankHealthFor 返回 (channel, model) 的 AutoRank 摘要与渠道级熔断状态，
// 供管理面板在分组模型列表中展示健康度与冷却时长。只读：不写入样本、
// 不推进熔断状态机（Open → HalfOpen 等）。
func AutoRankHealthFor(channelID int, modelName string) model.LLMAutoRankHealth {
	return AutoRankHealthForGroup(0, channelID, modelName)
}

func AutoRankHealthForGroup(groupID, channelID int, modelName string) model.LLMAutoRankHealth {
	st := GetAutoRankStatsForGroup(groupID, channelID, modelName)
	dispatch := GetAutoDispatchStats(groupID, channelID, modelName)
	effective := effectiveScoreForGroup(groupID, channelID, st, 0)
	h := model.LLMAutoRankHealth{
		Samples:           st.Samples,
		ProbeSamples:      st.ProbeSamples,
		ProbeDead:         st.ProbeOnlyDead(),
		Failures:          st.Failures,
		SuccessRate:       st.SuccessRate,
		SuccessConfidence: st.SuccessConfidence,
		EWMALatencyMS:     st.EWMALatencyMS,
		EWMATTFBMS:        st.EWMATTFBMS,
		Score:             scoreFromStats(st),
		EffectiveScore:    effective,
		Rank:              dispatch.Rank,
		Tier:              dispatch.Tier,
		TargetShare:       dispatch.TargetShare,
		ActualShare:       dispatch.ActualShare,
		LastSampleAt:      st.LastSeenAt,
		LastDispatchedAt:  dispatch.LastDispatched,
		SelectionReason:   dispatch.Reason,
		Degraded:          IsChannelDegradedForGroup(groupID, channelID),
		TrailSummary:      autoRankTrailForGroup(groupID, channelID, modelName),
	}
	tripped, remaining, tripCount := ChannelCircuitStatus(channelID)
	h.ChannelTripped = tripped
	h.ChannelTripCount = int64(tripCount)
	if tripped {
		h.ChannelCooldownSec = int64(remaining.Seconds())
	}
	return h
}

// AutoRankHealthForGroupItems 返回带组内相对 TTFB 修正的候选健康摘要。
func AutoRankHealthForGroupItems(groupID, channelID int, modelName string, items []model.GroupItem) model.LLMAutoRankHealth {
	h := AutoRankHealthForGroup(groupID, channelID, modelName)
	st := GetAutoRankStatsForGroup(groupID, channelID, modelName)
	h.EffectiveScore = effectiveScoreForGroup(groupID, channelID, st, groupMedianLatencyMSForGroup(groupID, items))
	return h
}

func (e *autoRankEntry) stats(now time.Time) AutoRankStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.statsLocked(now)
}

func (e *autoRankEntry) statsLocked(now time.Time) AutoRankStats {
	cutoff := now.Add(-AutoRankTimeWindow)
	var samples, probeSamples, failures, probeFailures int
	var ewmaLatency, ewmaTTFB float64
	latencyInit := false
	ttfbInit := false
	// 缓冲从 next 往前数 size 条，按从旧到新的顺序遍历
	for i := 0; i < e.size; i++ {
		s := e.buf[(e.next-e.size+i+AutoRankPhysicalCap)%AutoRankPhysicalCap]
		if s.at.After(cutoff) {
			samples++
			if s.probe {
				probeSamples++
			}
			if !s.success {
				failures++
				if s.probe {
					probeFailures++
				}
				continue
			}
			// 探测样本耗时不代表真实转发性能（max_tokens=1），不入延迟 EWMA。
			if s.probe {
				continue
			}
			if s.durMS > 0 {
				if !latencyInit {
					ewmaLatency = float64(s.durMS)
					latencyInit = true
				} else {
					ewmaLatency = 0.7*ewmaLatency + 0.3*float64(s.durMS)
				}
			}
			if s.ttfbMS > 0 {
				if !ttfbInit {
					ewmaTTFB = float64(s.ttfbMS)
					ttfbInit = true
				} else {
					ewmaTTFB = 0.7*ewmaTTFB + 0.3*float64(s.ttfbMS)
				}
			}
		}
	}
	st := AutoRankStats{
		Samples:       samples,
		ProbeSamples:  probeSamples,
		Failures:      failures,
		EWMALatencyMS: ewmaLatency,
		EWMATTFBMS:    ewmaTTFB,
		LastSeenAt:    e.lastSeen,
	}
	// 成功率优先用真实样本：探测是 max_tokens=1 的极小请求，成功门槛远低于真实
	// 转发，混进来会把成功率与 Wilson 置信下界一起刷高，让候选靠探测而不是靠真实
	// 表现挤进竞技池拿份额。真实样本为 0 时才回退探测样本——那正是探测的本职：
	// 让零流量候选也能暴露故障。渠道聚合另走 Samples/Failures 全量口径，探测失败
	// 依旧能触发渠道级降级。
	realSamples := samples - probeSamples
	switch {
	case realSamples > 0:
		realFailures := failures - probeFailures
		st.SuccessRate = 1 - float64(realFailures)/float64(realSamples)
		st.SuccessConfidence = wilsonLowerBound(realSamples-realFailures, realSamples)
	case probeSamples > 0:
		st.SuccessRate = 1 - float64(probeFailures)/float64(probeSamples)
		st.SuccessConfidence = wilsonLowerBound(probeSamples-probeFailures, probeSamples)
	}
	return st
}

func wilsonLowerBound(successes, samples int) float64 {
	if samples <= 0 {
		return 0
	}
	const z = 1.2815515655446004 // one-sided 90% confidence
	n := float64(samples)
	p := float64(successes) / n
	z2 := z * z
	center := p + z2/(2*n)
	margin := z * math.Sqrt((p*(1-p)+z2/(4*n))/n)
	return math.Max(0, (center-margin)/(1+z2/n))
}

func autoRankMinSamples() int {
	minSamples, _ := op.SettingGetInt(model.SettingKeyAutoRankMinSamples)
	if minSamples <= 0 {
		minSamples = 5
	}
	return minSamples
}

// scoreFromStats 把性能统计换算为排序得分：成功率置信下界*100 - 延迟(秒)。
// 成功率占比高（满分 100）优先于延迟，保证"稳"优先于"快"。
func scoreFromStats(st AutoRankStats) float64 {
	rate := st.SuccessConfidence
	if rate <= 0 && st.SuccessRate > 0 {
		rate = st.SuccessRate
	}
	return rate*100 - autoRankLatencyMS(st)/1000.0
}

func autoRankLatencyMS(st AutoRankStats) float64 {
	if st.EWMATTFBMS > 0 {
		return st.EWMATTFBMS
	}
	return st.EWMALatencyMS
}

// autoRankLess 返回 a 是否应排在 b 之前（稳定三档比较）：
//   - 档0 无有效样本：冷启动，排最后；
//   - 档1 样本不足 minSamples：置信度低，排在有足够样本的候选之后；
//   - 档2 有足够样本：按得分降序（成功率优先，其次延迟）。
//
// 档位一律按真实转发样本（RealSamples）划分，主动探测不能顶替真实样本。
func autoRankLess(a, b AutoRankStats) bool {
	minSamples := autoRankMinSamples()
	aReal, bReal := a.RealSamples(), b.RealSamples()
	aNo, bNo := aReal == 0, bReal == 0
	aLow := !aNo && aReal < minSamples
	bLow := !bNo && bReal < minSamples
	switch {
	case aNo:
		return false
	case bNo:
		return true
	case aLow && bLow, !aLow && !bLow:
		return scoreFromStats(a) > scoreFromStats(b)
	case aLow:
		return false
	default: // bLow
		return true
	}
}

// ---------------------------------------------------------------------------
// 渠道级聚合健康修正（纯果驱动，非独立信号源）
//
// 语义：渠道整体健康度不是独立存在的"因"，而是"该渠道在组内各模型窗口
// 同时恶化"这一事实的统计投影（果）。因此本修正不注入任何新数据面：
// 排序时实时聚合组内各模型的 AutoRankStats，仅当满足判据
//   (a) 渠道样本总量 ≥ minSamples
//   (b) 窗口内有失败样本的模型数 ≥ minModels（≥2，单模型失败不触发）
//   (c) 聚合成功率 < degradeRate
// 时，把该渠道所有模型的有效得分乘以一个 <1 的系数（分档 + EWMA 平滑）。
//
// 效果：
//   - 单模型失败 → 不触发判据 → 同渠道其他模型完全不受影响（模型级隔离）；
//   - 渠道整体劣化 → 各模型窗口同时恶化 → 判据命中 → 全渠道统一系数
//     （渠道内相对顺序不变：同渠道最好的模型仍排最前）；
//   - 渠道恢复 → 聚合回升 → 系数平滑回到 1.0，自动复原。
// 系数只作用于排序（不写入模型窗口），学习窗口始终记录真实性能。
// ---------------------------------------------------------------------------

// channelAggregate 一次排序请求中某渠道的组内聚合统计。
type channelAggregate struct {
	models       int // 组内该渠道的模型数
	totalSamples int // 窗口样本总数（组内模型之和）
	totalFails   int // 窗口失败总数
	failModels   int // 窗口内有失败样本的模型数
}

// rate 返回聚合成功率（样本加权）；无样本视为健康（1）。
func (a channelAggregate) rate() float64 {
	if a.totalSamples <= 0 {
		return 1
	}
	return 1 - float64(a.totalFails)/float64(a.totalSamples)
}

// channelFactorEntry 渠道聚合系数的平滑状态。仅由排序请求侧更新（纯果），
// 无独立数据源；lastSeen 供内存回收。
type channelFactorEntry struct {
	mu       sync.Mutex
	factor   float64
	inited   bool
	lastSeen time.Time
}

type channelFactorKey struct {
	groupID   int
	channelID int
}

var globalChannelFactor sync.Map // key: channelFactorKey -> *channelFactorEntry

func channelFactorEnabled() bool {
	enabled, err := op.SettingGetBool(model.SettingKeyAutoRankChannelFactorEnabled)
	if err != nil {
		return true
	}
	return enabled
}

func channelMinSamples() int {
	v, err := op.SettingGetInt(model.SettingKeyAutoRankChannelMinSamples)
	if err != nil || v < 1 {
		return 8
	}
	return v
}

func channelMinModels() int {
	v, err := op.SettingGetInt(model.SettingKeyAutoRankChannelMinModels)
	if err != nil || v < 1 {
		return 2
	}
	return v
}

func channelDegradeRate() float64 {
	pct, err := op.SettingGetInt(model.SettingKeyAutoRankChannelDegradeRate)
	if err != nil {
		return 0.85
	}
	if pct < 1 {
		return 0.85
	}
	if pct > 100 {
		pct = 100
	}
	return float64(pct) / 100.0
}

// targetFactorForRate 聚合成功率 -> 目标系数（分段）。
func targetFactorForRate(rate float64) float64 {
	switch {
	case rate >= 0.9:
		return 1.0
	case rate >= 0.7:
		return 0.6
	case rate >= 0.5:
		return 0.4
	default:
		return 0.3
	}
}

// computeChannelAggregates 按渠道聚合组内模型的窗口统计（组内视角）。
func computeChannelAggregates(items []model.GroupItem) map[int]channelAggregate {
	return computeChannelAggregatesForGroup(0, items)
}

func computeChannelAggregatesForGroup(groupID int, items []model.GroupItem) map[int]channelAggregate {
	aggs := make(map[int]channelAggregate, 8)
	for _, item := range items {
		a := aggs[item.ChannelID]
		a.models++
		st := GetAutoRankStatsForGroup(groupID, item.ChannelID, item.ModelName)
		if st.Samples > 0 {
			a.totalSamples += st.Samples
			a.totalFails += st.Failures
			if st.Failures > 0 {
				a.failModels++
			}
		}
		aggs[item.ChannelID] = a
	}
	return aggs
}

// channelAggregateFactor 更新并返回某渠道的平滑系数（0.3~1.0）。
// 判据：多模型同时失败（failModels >= minModels）且聚合成功率低于阈值；
// 惩罚力度按样本置信度线性打折（ccLoad 式 min(1, samples/minSamples)），
// 样本不足时部分生效而非全有全无，冷启动平滑；不满足判据时目标为 1.0。
func channelAggregateFactor(channelID int, agg channelAggregate) float64 {
	return channelAggregateFactorForGroup(0, channelID, agg)
}

func channelAggregateFactorForGroup(groupID, channelID int, agg channelAggregate) float64 {
	target := 1.0
	if channelFactorEnabled() && agg.failModels >= channelMinModels() {
		if rate := agg.rate(); rate < channelDegradeRate() {
			base := targetFactorForRate(rate)
			// 置信度线性打折：样本达到 minSamples 全额惩罚；不足按比例打折
			confidence := 1.0
			if minSamples := channelMinSamples(); minSamples > 0 {
				confidence = math.Min(1.0, float64(agg.totalSamples)/float64(minSamples))
			}
			target = 1.0 - (1.0-base)*confidence
		}
	}

	key := channelFactorKey{groupID: groupID, channelID: channelID}
	v, _ := globalChannelFactor.LoadOrStore(key, &channelFactorEntry{})
	e := v.(*channelFactorEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.inited {
		e.factor = target
		e.inited = true
	} else {
		e.factor = 0.7*e.factor + 0.3*target
	}
	e.lastSeen = time.Now()
	return e.factor
}

// currentChannelFactor 只读当前平滑系数（不更新 lastSeen），供 sticky 决策。
func currentChannelFactor(channelID int) float64 {
	return currentChannelFactorForGroup(0, channelID)
}

func currentChannelFactorForGroup(groupID, channelID int) float64 {
	if v, ok := globalChannelFactor.Load(channelFactorKey{groupID: groupID, channelID: channelID}); ok {
		e := v.(*channelFactorEntry)
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.factor
	}
	return 1.0
}

// IsChannelDegraded 报告渠道是否处于聚合惩罚状态（系数 <1），
// 供迭代器/会话粘性决策：被惩罚渠道不应被强制提前。
func IsChannelDegraded(channelID int) bool {
	return IsChannelDegradedForGroup(0, channelID)
}

func IsChannelDegradedForGroup(groupID, channelID int) bool {
	return currentChannelFactorForGroup(groupID, channelID) < 0.99
}

// ---------------------------------------------------------------------------
// 相对 TTFB 惩罚（ccLoad 式）：组内候选 EWMA 延迟中位数为基准，
// 慢于中位数的模型按 (latency/median − 1) 上限 S_max 扣除得分，
// 惩罚带置信度打折。只罚慢不奖快，适应"整体都慢/都快"的环境。
// ---------------------------------------------------------------------------

func ttfbEnabled() bool {
	enabled, err := op.SettingGetBool(model.SettingKeyAutoRankTTFBEnabled)
	if err != nil {
		return false
	}
	return enabled
}

func ttfbWeight() int {
	v, err := op.SettingGetInt(model.SettingKeyAutoRankTTFBWeight)
	if err != nil || v < 0 {
		return 20
	}
	return v
}

func ttfbMaxSlowRatio() float64 {
	pct, err := op.SettingGetInt(model.SettingKeyAutoRankTTFBMaxSlowRatio)
	if err != nil || pct < 0 {
		return 2.0
	}
	return float64(pct) / 100.0
}

func ttfbMinConfidentSample() int {
	v, err := op.SettingGetInt(model.SettingKeyAutoRankTTFBMinConfidentSample)
	if err != nil || v < 1 {
		return 10
	}
	return v
}

// groupMedianLatencyMS 计算组内候选的有效 EWMA 延迟中位数（毫秒）。
// 少于 2 个有效样本（延迟 >0）返回 0，表示不启用相对 TTFB 惩罚。
// 只认真实转发样本：探测不产生延迟观测，不该参与中位数基准。
func groupMedianLatencyMS(items []model.GroupItem) float64 {
	return groupMedianLatencyMSForGroup(0, items)
}

func groupMedianLatencyMSForGroup(groupID int, items []model.GroupItem) float64 {
	var vals []float64
	for _, item := range items {
		st := GetAutoRankStatsForGroup(groupID, item.ChannelID, item.ModelName)
		if latency := autoRankLatencyMS(st); latency > 0 && st.RealSamples() > 0 {
			vals = append(vals, latency)
		}
	}
	n := len(vals)
	if n < 2 {
		return 0
	}
	sort.Float64s(vals)
	if n%2 == 1 {
		return vals[n/2]
	}
	return (vals[n/2-1] + vals[n/2]) / 2
}

// ttfbPenalty 相对延迟惩罚：slow = clamp(latency/median − 1, 0, S_max)，
// 惩罚 = slow × weight × confidence（样本置信度线性打折）。
// 置信度按真实转发样本折算：这里衡量的是延迟观测的可信度，而探测不产生延迟观测。
func ttfbPenalty(st AutoRankStats, medianMS float64) float64 {
	latencyMS := autoRankLatencyMS(st)
	realSamples := st.RealSamples()
	if medianMS <= 0 || latencyMS <= 0 || realSamples <= 0 {
		return 0
	}
	sRatio := latencyMS / medianMS
	slow := sRatio - 1.0
	if slow < 0 {
		slow = 0
	}
	if maxSlow := ttfbMaxSlowRatio(); maxSlow > 0 && slow > maxSlow {
		slow = maxSlow
	}
	confidence := 1.0
	if minConf := ttfbMinConfidentSample(); minConf > 0 {
		confidence = math.Min(1.0, float64(realSamples)/float64(minConf))
	}
	return slow * float64(ttfbWeight()) * confidence
}

// effectiveScore 模型在组内排序的有效得分：
// 自身性能得分 × 渠道聚合系数 − 相对 TTFB 惩罚（medianMS<=0 时不启用）。
// 同一渠道所有模型乘以相同系数 → 渠道内相对顺序不变（模型级隔离）。
func effectiveScore(channelID int, st AutoRankStats, medianMS float64) float64 {
	return effectiveScoreForGroup(0, channelID, st, medianMS)
}

func effectiveScoreForGroup(groupID, channelID int, st AutoRankStats, medianMS float64) float64 {
	score := scoreFromStats(st) * currentChannelFactorForGroup(groupID, channelID)
	if medianMS > 0 && ttfbEnabled() {
		score -= ttfbPenalty(st, medianMS)
	}
	return score
}

// autoRankLessScored 带渠道聚合系数与相对 TTFB 惩罚的排序比较：
// 档位逻辑同 autoRankLess，档2（样本充足）内比较 effectiveScore。
func autoRankLessScored(aC int, a AutoRankStats, bC int, b AutoRankStats, medianMS float64) bool {
	minSamples := autoRankMinSamples()
	aReal, bReal := a.RealSamples(), b.RealSamples()
	aNo, bNo := aReal == 0, bReal == 0
	aLow := !aNo && aReal < minSamples
	bLow := !bNo && bReal < minSamples
	switch {
	case aNo:
		return false
	case bNo:
		return true
	case aLow && bLow, !aLow && !bLow:
		return effectiveScore(aC, a, medianMS) > effectiveScore(bC, b, medianMS)
	case aLow:
		return false
	default: // bLow
		return true
	}
}

// CountableFailure 报告某个 HTTP 状态码对应的失败是否计入 AutoRank 健康窗口。
// 统计口径对齐 ccLoad：只统计能反映渠道/Key 质量的结果，排除客户端噪音——
//   - 客户端误用（404/415/422 等非 401/402/403 的 4xx）、客户端取消(499)、408：不计
//   - 限流(429)：不计（交给熔断 Soft 分支与重试语义，避免个别坏 Key 拉低渠道）
//   - 配额类(596)：不计（不反映渠道质量）
//
// 纳入：连接错误(0)、Key 级认证(401/402/403/405)、渠道级 5xx(500/502/503/504)、
// Anthropic 过载(529)、Cloudflare(520/521/524)、流式异常(597/598/599)。
//
// 数据面（relay 转发）与控制面（主动探测任务）共用此表：探测被站点以 400
// 拒绝（不接受无业务意图请求）时不该被记为渠道不健康。
func CountableFailure(statusCode int) bool {
	switch statusCode {
	case 0, 401, 402, 403, 405, 500, 502, 503, 504, 520, 521, 524, 529, 597, 598, 599:
		return true
	default:
		return false
	}
}

// RecordAutoSample 数据面采集：记录一次候选最终结果。
// 成功时 durationMS 为转发耗时，用于 EWMA 延迟更新；失败时仅计入窗口失败数。
func RecordAutoSample(channelID int, modelName string, success bool, durationMS int64) {
	RecordAutoSampleForGroup(0, channelID, modelName, success, durationMS, durationMS)
}

func RecordAutoSampleForGroup(groupID, channelID int, modelName string, success bool, durationMS, ttfbMS int64) {
	recordAutoSample(groupID, channelID, modelName, success, durationMS, ttfbMS, false)
}

// RecordAutoProbeSampleForGroup 控制面采集：记录一次主动探测结果。
//
// 探测请求是 max_tokens=1 的极小请求，耗时与真实转发不可比，因此不写延迟
// （durMS/ttfbMS 传 0），只补充成功率窗口——目的是让"没有流量的候选"也能
// 暴露已经不可用的事实，而不是让它靠"零延迟"爬到排序前面（见 RealSamples）。
func RecordAutoProbeSampleForGroup(groupID, channelID int, modelName string, success bool) {
	recordAutoSample(groupID, channelID, modelName, success, 0, 0, true)
}

func recordAutoSample(groupID, channelID int, modelName string, success bool, durationMS, ttfbMS int64, probe bool) {
	key := autoRankGroupKey(groupID, channelID, modelName)
	e := getOrCreateAutoRank(key)
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.buf[e.next] = autoRankSample{at: now, success: success, probe: probe, durMS: durationMS, ttfbMS: ttfbMS}
	e.next = (e.next + 1) % AutoRankPhysicalCap
	if e.size < AutoRankPhysicalCap {
		e.size++
	}
	e.lastSeen = now
}

// AutoRankKeyedStats 带键的性能统计，供控制面落库。
type AutoRankKeyedStats struct {
	GroupID   int
	ChannelID int
	ModelName string
	Stats     AutoRankStats
	// Trail 窗口样本时间序列 JSON（从旧到新），供快照落库后精确恢复。
	Trail string
}

// AutoRankAllStats 遍历返回全部有有效样本的候选统计。
func AutoRankAllStats() []AutoRankKeyedStats {
	out := make([]AutoRankKeyedStats, 0)
	globalAutoRank.Range(func(key, value any) bool {
		k, ok := key.(string)
		if !ok {
			return true
		}
		e := value.(*autoRankEntry)
		st := e.stats(time.Now())
		if st.Samples == 0 {
			return true
		}
		groupID, channelID, modelName := parseAutoRankGroupKey(k)
		out = append(out, AutoRankKeyedStats{GroupID: groupID, ChannelID: channelID, ModelName: modelName, Stats: st, Trail: e.sampleTrailJSON()})
		return true
	})
	return out
}

// AutoRankRestore 从持久化快照重建内存窗口（近似：假定最近 failures 条为失败）。
// 恢复的样本随后会被真实流量逐条覆盖，约 AutoRankPhysicalCap 条后完全回归真实。
func AutoRankRestore(snaps []model.AutoRankSnapshot) {
	now := time.Now()
	for _, s := range snaps {
		if s.Samples <= 0 {
			continue
		}
		lastSeen := s.LastSeenAt
		if lastSeen.IsZero() {
			lastSeen = s.UpdatedAt
		}
		if lastSeen.IsZero() || now.Sub(lastSeen) >= AutoRankTimeWindow {
			continue
		}
		key := autoRankGroupKey(s.GroupID, s.ChannelID, s.ModelName)
		e := getOrCreateAutoRank(key)
		e.mu.Lock()
		// 优先精确重建：SampleTrail 含窗口样本时间序列（时间分布/失败位置/延迟），
		// 逐条还原；缺失或损坏时回退近似重建（假定最近 failures 条为失败）。
		if !restoreAutoRankPrecise(e, s, lastSeen) {
			restoreAutoRankApprox(e, s, lastSeen)
		}
		e.mu.Unlock()
	}
}

// autoRankTrailItem SampleTrail 中单条样本的记录。
// AgeMS 为该样本距 LastSeenAt（最新样本时刻）的毫秒偏移。
type autoRankTrailItem struct {
	AgeMS  int64 `json:"age_ms"`
	OK     bool  `json:"ok"`
	Probe  bool  `json:"p"`
	DurMS  int64 `json:"d"`
	TTFBMS int64 `json:"t"`
}

// sampleTrail 导出窗口内样本时间序列（从旧到新，age 单调递减），供快照落库。
func (e *autoRankEntry) sampleTrail() []autoRankTrailItem {
	e.mu.Lock()
	defer e.mu.Unlock()
	trail := make([]autoRankTrailItem, 0, e.size)
	lastSeen := e.lastSeen
	for i := 0; i < e.size; i++ {
		s := e.buf[(e.next-e.size+i+AutoRankPhysicalCap)%AutoRankPhysicalCap]
		age := lastSeen.Sub(s.at).Milliseconds()
		if age < 0 {
			age = 0
		}
		trail = append(trail, autoRankTrailItem{AgeMS: age, OK: s.success, Probe: s.probe, DurMS: s.durMS, TTFBMS: s.ttfbMS})
	}
	return trail
}

// sampleTrailJSON SampleTrail 的 JSON 编码；空窗口返回 ""。
func (e *autoRankEntry) sampleTrailJSON() string {
	trail := e.sampleTrail()
	if len(trail) == 0 {
		return ""
	}
	b, err := json.Marshal(trail)
	if err != nil {
		return ""
	}
	return string(b)
}

// restoreAutoRankPrecise 用 SampleTrail 精确重建窗口。样本时间按 age_ms 反推
// （at = lastSeen - age），保留时间分布、失败位置与逐条延迟；EWMA 在读取时由
// statsLocked 从真实样本重新计算。数据缺失/损坏/越界时返回 false 放弃。
func restoreAutoRankPrecise(e *autoRankEntry, s model.AutoRankSnapshot, lastSeen time.Time) bool {
	if s.SampleTrail == "" {
		return false
	}
	var trail []autoRankTrailItem
	if err := json.Unmarshal([]byte(s.SampleTrail), &trail); err != nil {
		return false
	}
	if len(trail) == 0 || len(trail) > AutoRankPhysicalCap {
		return false
	}
	for i, item := range trail {
		if item.AgeMS < 0 {
			return false
		}
		if i > 0 && trail[i-1].AgeMS < item.AgeMS {
			return false // age 需单调不增（从旧到新），乱序视为损坏
		}
		at := lastSeen.Add(-time.Duration(item.AgeMS) * time.Millisecond)
		if at.After(lastSeen) {
			return false
		}
		e.buf[i] = autoRankSample{at: at, success: item.OK, probe: item.Probe, durMS: item.DurMS, ttfbMS: item.TTFBMS}
	}
	e.next = len(trail) % AutoRankPhysicalCap
	e.size = len(trail)
	e.lastSeen = lastSeen
	return true
}

// restoreAutoRankApprox 近似重建（旧版行为）：假定最近 failures 条为失败，
// 时间戳统一为 lastSeen，延迟用 EWMA 均值。恢复的样本随后被真实流量逐条覆盖。
func restoreAutoRankApprox(e *autoRankEntry, s model.AutoRankSnapshot, lastSeen time.Time) {
	size := s.Samples
	if size > AutoRankPhysicalCap {
		size = AutoRankPhysicalCap
	}
	fails := s.Failures
	if fails > s.Samples {
		fails = s.Samples
	}
	probes := s.ProbeSamples
	if probes > s.Samples {
		probes = s.Samples
	}
	if size < s.Samples {
		fails = int(math.Round(float64(fails) * float64(size) / float64(s.Samples)))
		probes = int(math.Round(float64(probes) * float64(size) / float64(s.Samples)))
	}
	for i := 0; i < size; i++ {
		e.buf[i] = autoRankSample{
			at:      lastSeen,
			success: i < size-fails,
			probe:   i < probes,
			durMS:   int64(s.EWMALatencyMS),
			ttfbMS:  int64(s.EWMATTFBMS),
		}
	}
	e.next = size % AutoRankPhysicalCap
	e.size = size
	e.lastSeen = lastSeen
}

// autoRankTrailForGroup 生成窗口样本时间线摘要（✓ 成功 / ✗ 失败 / p 探测，
// 从旧到新），供管理面板直观展示排序依据；无样本返回空串。
func autoRankTrailForGroup(groupID, channelID int, modelName string) string {
	key := autoRankGroupKey(groupID, channelID, modelName)
	v, ok := globalAutoRank.Load(key)
	if !ok {
		return ""
	}
	trail := v.(*autoRankEntry).sampleTrail()
	if len(trail) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.Grow(len(trail))
	for _, item := range trail {
		if item.Probe {
			sb.WriteString("p")
		} else if item.OK {
			sb.WriteString("✓")
		} else {
			sb.WriteString("✗")
		}
	}
	return sb.String()
}

// AutoRankReap 回收 lastSeen 早于 now-ttl 的窗口（已删除渠道/长期无流量）。
// 同时回收渠道聚合系数的陈旧状态（纯内存，重启即清）。
func AutoRankReap(now time.Time, ttl time.Duration) int {
	if ttl <= 0 {
		return 0
	}
	cutoff := now.Add(-ttl)
	reaped := 0
	globalAutoRank.Range(func(key, value any) bool {
		e := value.(*autoRankEntry)
		e.mu.Lock()
		if e.lastSeen.Before(cutoff) {
			globalAutoRank.Delete(key)
			reaped++
		}
		e.mu.Unlock()
		return true
	})
	globalChannelFactor.Range(func(key, value any) bool {
		e := value.(*channelFactorEntry)
		e.mu.Lock()
		if e.lastSeen.Before(cutoff) {
			globalChannelFactor.Delete(key)
			reaped++
		}
		e.mu.Unlock()
		return true
	})
	globalAutoSchedule.Range(func(key, value any) bool {
		s := value.(*autoGroupScheduleState)
		s.mu.Lock()
		if s.lastSeen.Before(cutoff) {
			globalAutoSchedule.Delete(key)
			reaped++
		}
		s.mu.Unlock()
		return true
	})
	return reaped
}

// AutoRankReset 清空内存排序状态（测试用）。
func AutoRankReset() {
	globalAutoRank = sync.Map{}
	globalChannelFactor = sync.Map{}
	globalAutoSchedule = sync.Map{}
}
