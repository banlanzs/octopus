package balancer

import (
	"fmt"
	"math"
	"math/rand/v2"
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

// Auto 自动排序：候选按实时性能得分（延迟+成功率综合）降序排列。
// 失败降级由外层迭代器（relay.go 的 for iter.Next()）负责，与本模式正交。
// 总开关 auto_rank_enabled 关闭时回退为原始顺序（不排序、不学习）。
//
// 数据来源为纯被动学习（真实请求）：为避免中转站对无业务意图的极小探测请求
// （如 ping + 1 token）风控，不发起任何主动探测；冷启动/低流量候选通过
// epsilon 探索（auto_rank_explore_ratio）在真实请求中按比例被优先尝试，
// 从而积累样本参与排序。
type Auto struct{}

func (b *Auto) Candidates(items []model.GroupItem) []model.GroupItem {
	n := len(items)
	if n == 0 {
		return nil
	}
	enabled, err := op.SettingGetBool(model.SettingKeyAutoRankEnabled)
	if err != nil || !enabled {
		return items
	}
	result := make([]model.GroupItem, n)
	copy(result, items)
	// 渠道聚合修正（纯果驱动）：先按组内渠道聚合模型窗口，更新各渠道的
	// 平滑系数（副作用），再按"模型得分 × 渠道系数 − 相对 TTFB 惩罚"排序。
	aggregates := computeChannelAggregates(result)
	for channelID, agg := range aggregates {
		channelAggregateFactor(channelID, agg)
	}
	medianMS := groupMedianLatencyMS(result)
	// 得分相同（如均无有效样本）时保持原配置顺序，避免无谓的排序抖动。
	sort.SliceStable(result, func(i, j int) bool {
		return autoRankLessScored(
			result[i].ChannelID, GetAutoRankStats(result[i].ChannelID, result[i].ModelName),
			result[j].ChannelID, GetAutoRankStats(result[j].ChannelID, result[j].ModelName),
			medianMS,
		)
	})
	// epsilon 探索：以探索比例把"样本不足"的候选提前尝试，保证冷启动候选
	// 能通过真实业务请求积累样本。所有流量均为真实请求，无额外 API 成本。
	autoRankMaybeExplore(result)
	return result
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

// autoRankMaybeExplore 以探索比例把欠采样候选（或随机候选）提前到首位。
// 仅调整候选顺序，不产生任何网络请求。
func autoRankMaybeExplore(candidates []model.GroupItem) {
	ratio := autoRankExploreRatio()
	if ratio <= 0 {
		return
	}
	if rand.Float64() >= ratio {
		return
	}
	n := len(candidates)
	if n <= 1 {
		return
	}
	minSamples := autoRankMinSamples()
	if idx := findUnderSampled(candidates, minSamples); idx > 0 {
		// 优先探索样本不足的候选（冷启动/低流量渠道）
		item := candidates[idx]
		copy(candidates[1:idx+1], candidates[0:idx])
		candidates[0] = item
		return
	}
	// 全部已充分采样：随机探索一个非首位候选，维持对性能漂移的感知
	j := 1 + rand.IntN(n-1)
	item := candidates[j]
	copy(candidates[1:j+1], candidates[0:j])
	candidates[0] = item
}

// findUnderSampled 返回第一个有效样本数低于 minSamples 的候选下标（含无样本）；无则 -1。
func findUnderSampled(candidates []model.GroupItem, minSamples int) int {
	for i, item := range candidates {
		if st := GetAutoRankStats(item.ChannelID, item.ModelName); st.Samples < minSamples {
			return i
		}
	}
	return -1
}

type autoRankSample struct {
	at      time.Time
	success bool
	durMS   int64
}

// autoRankEntry 单个 (channelID, modelName) 的性能学习窗口。
// 数据面（relay）在每次候选最终结果点调用 RecordAutoSample；控制面（task）周期
// 调用 AutoRankStats/AutoRankNeedsProbe 获取统计与探测决策。纯内存、重启清空，
// 持久化恢复由 AutoRankRestore 从 AutoRankSnapshot 重建近似窗口。
type autoRankEntry struct {
	mu          sync.Mutex
	buf         [AutoRankPhysicalCap]autoRankSample
	next        int
	size        int
	lastSeen    time.Time // 最近一次样本时间，用于内存回收
	ewmaLatency float64   // 成功样本的 EWMA 延迟（毫秒）
	ewmaInit    bool
}

var globalAutoRank sync.Map // key: "channelID:modelName" -> *autoRankEntry

func autoRankKey(channelID int, modelName string) string {
	return fmt.Sprintf("%d:%s", channelID, modelName)
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

// AutoRankStats 候选性能摘要，供排序打分与落库快照使用。
type AutoRankStats struct {
	Samples       int
	Failures      int
	SuccessRate   float64
	EWMALatencyMS float64
	LastSeenAt    time.Time
}

// GetAutoRankStats 读取 (channel, model) 的性能统计（无记录返回零值）。
func GetAutoRankStats(channelID int, modelName string) AutoRankStats {
	key := autoRankKey(channelID, modelName)
	v, ok := globalAutoRank.Load(key)
	if !ok {
		return AutoRankStats{}
	}
	e := v.(*autoRankEntry)
	return e.stats(time.Now())
}

func (e *autoRankEntry) stats(now time.Time) AutoRankStats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.statsLocked(now)
}

func (e *autoRankEntry) statsLocked(now time.Time) AutoRankStats {
	cutoff := now.Add(-AutoRankTimeWindow)
	var samples, failures int
	// 缓冲从 next 往前数 size 条，按从旧到新的顺序遍历
	for i := 0; i < e.size; i++ {
		s := e.buf[(e.next-e.size+i+AutoRankPhysicalCap)%AutoRankPhysicalCap]
		if s.at.After(cutoff) {
			samples++
			if !s.success {
				failures++
			}
		}
	}
	st := AutoRankStats{
		Samples:       samples,
		Failures:      failures,
		EWMALatencyMS: e.ewmaLatency,
		LastSeenAt:    e.lastSeen,
	}
	if samples > 0 {
		st.SuccessRate = 1 - float64(failures)/float64(samples)
	}
	return st
}

func autoRankMinSamples() int {
	minSamples, _ := op.SettingGetInt(model.SettingKeyAutoRankMinSamples)
	if minSamples <= 0 {
		minSamples = 3
	}
	return minSamples
}

// scoreFromStats 把性能统计换算为排序得分：成功率*100 - 延迟(秒)。
// 成功率占比高（满分 100）优先于延迟，保证"稳"优先于"快"。
func scoreFromStats(st AutoRankStats) float64 {
	return st.SuccessRate*100 - st.EWMALatencyMS/1000.0
}

// autoRankLess 返回 a 是否应排在 b 之前（稳定三档比较）：
//   - 档0 无有效样本：冷启动，排最后；
//   - 档1 样本不足 minSamples：置信度低，排在有足够样本的候选之后；
//   - 档2 有足够样本：按得分降序（成功率优先，其次延迟）。
func autoRankLess(a, b AutoRankStats) bool {
	minSamples := autoRankMinSamples()
	aNo, bNo := a.Samples == 0, b.Samples == 0
	aLow := !aNo && a.Samples < minSamples
	bLow := !bNo && b.Samples < minSamples
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

var globalChannelFactor sync.Map // key: int(channelID) -> *channelFactorEntry

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
	aggs := make(map[int]channelAggregate, 8)
	for _, item := range items {
		a := aggs[item.ChannelID]
		a.models++
		st := GetAutoRankStats(item.ChannelID, item.ModelName)
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

	key := channelID
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
	if v, ok := globalChannelFactor.Load(channelID); ok {
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
	return currentChannelFactor(channelID) < 0.99
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
func groupMedianLatencyMS(items []model.GroupItem) float64 {
	var vals []float64
	for _, item := range items {
		st := GetAutoRankStats(item.ChannelID, item.ModelName)
		if st.EWMALatencyMS > 0 {
			vals = append(vals, st.EWMALatencyMS)
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
func ttfbPenalty(st AutoRankStats, medianMS float64) float64 {
	if medianMS <= 0 || st.EWMALatencyMS <= 0 || st.Samples <= 0 {
		return 0
	}
	sRatio := st.EWMALatencyMS / medianMS
	slow := sRatio - 1.0
	if slow < 0 {
		slow = 0
	}
	if maxSlow := ttfbMaxSlowRatio(); maxSlow > 0 && slow > maxSlow {
		slow = maxSlow
	}
	confidence := 1.0
	if minConf := ttfbMinConfidentSample(); minConf > 0 {
		confidence = math.Min(1.0, float64(st.Samples)/float64(minConf))
	}
	return slow * float64(ttfbWeight()) * confidence
}

// effectiveScore 模型在组内排序的有效得分：
// 自身性能得分 × 渠道聚合系数 − 相对 TTFB 惩罚（medianMS<=0 时不启用）。
// 同一渠道所有模型乘以相同系数 → 渠道内相对顺序不变（模型级隔离）。
func effectiveScore(channelID int, st AutoRankStats, medianMS float64) float64 {
	score := scoreFromStats(st) * currentChannelFactor(channelID)
	if medianMS > 0 && ttfbEnabled() {
		score -= ttfbPenalty(st, medianMS)
	}
	return score
}

// autoRankLessScored 带渠道聚合系数与相对 TTFB 惩罚的排序比较：
// 档位逻辑同 autoRankLess，档2（样本充足）内比较 effectiveScore。
func autoRankLessScored(aC int, a AutoRankStats, bC int, b AutoRankStats, medianMS float64) bool {
	minSamples := autoRankMinSamples()
	aNo, bNo := a.Samples == 0, b.Samples == 0
	aLow := !aNo && a.Samples < minSamples
	bLow := !bNo && b.Samples < minSamples
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

// RecordAutoSample 数据面采集：记录一次候选最终结果。
// 成功时 durationMS 为转发耗时，用于 EWMA 延迟更新；失败时仅计入窗口失败数。
func RecordAutoSample(channelID int, modelName string, success bool, durationMS int64) {
	key := autoRankKey(channelID, modelName)
	e := getOrCreateAutoRank(key)
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.buf[e.next] = autoRankSample{at: now, success: success, durMS: durationMS}
	e.next = (e.next + 1) % AutoRankPhysicalCap
	if e.size < AutoRankPhysicalCap {
		e.size++
	}
	e.lastSeen = now
	if success && durationMS > 0 {
		if !e.ewmaInit {
			e.ewmaLatency = float64(durationMS)
			e.ewmaInit = true
		} else {
			e.ewmaLatency = 0.7*e.ewmaLatency + 0.3*float64(durationMS)
		}
	}
}

// AutoRankKeyedStats 带键的性能统计，供控制面落库。
type AutoRankKeyedStats struct {
	ChannelID int
	ModelName string
	Stats     AutoRankStats
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
		channelID, modelName := parseAutoRankKey(k)
		out = append(out, AutoRankKeyedStats{ChannelID: channelID, ModelName: modelName, Stats: st})
		return true
	})
	return out
}

// AutoRankRestore 从持久化快照重建内存窗口（近似：假定最近 failures 条为失败）。
// 恢复的样本随后会被真实流量逐条覆盖，约 AutoRankPhysicalCap 条后完全回归真实。
func AutoRankRestore(snaps []model.AutoRankSnapshot) {
	for _, s := range snaps {
		if s.Samples <= 0 {
			continue
		}
		key := autoRankKey(s.ChannelID, s.ModelName)
		e := getOrCreateAutoRank(key)
		e.mu.Lock()
		fails := s.Failures
		if fails > s.Samples {
			fails = s.Samples
		}
		size := s.Samples
		if size > AutoRankPhysicalCap {
			size = AutoRankPhysicalCap
		}
		now := time.Now()
		for i := 0; i < size; i++ {
			e.buf[i] = autoRankSample{at: now, success: i < size-fails}
		}
		e.next = size % AutoRankPhysicalCap
		e.size = size
		e.lastSeen = now
		if s.EWMALatencyMS > 0 {
			e.ewmaLatency = s.EWMALatencyMS
			e.ewmaInit = true
		}
		e.mu.Unlock()
	}
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
	return reaped
}

// AutoRankReset 清空内存排序状态（测试用）。
func AutoRankReset() {
	globalAutoRank = sync.Map{}
	globalChannelFactor = sync.Map{}
}
