package balancer

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

const (
	defaultAutoSuccessGap      = 0.02
	defaultAutoLatencyRatio    = 1.5
	defaultAutoChannelMaxShare = 0.70
	defaultAutoModelMaxShare   = 0.80
	autoSoftmaxTemperature     = 5.0
	// feedbackMin 是 feedbackPenalty 的地板系数：adjustedTarget 不低于 target×feedbackMin，
	// 防止实际分配反馈把某候选完全斩断。
	feedbackMin = 0.5
	// feedbackUpdateInterval 是 EWMA 实际份额更新的触发间隔（每次组内该次数转发后更新一轮）。
	// 不暴露为 setting，测试可通过 autoScheduleConfig.FeedbackUpdateInterval 覆盖。
	feedbackUpdateInterval = 10
)

type autoScheduleConfig struct {
	ExploreRatio    float64
	MinSamples      int
	SuccessGap      float64
	LatencyRatio    float64
	ChannelMaxShare float64
	ModelMaxShare   float64
	SoftmaxTemp     float64 // 竞技池/公平调度 softmax 温度
	HealthThreshold float64 // 竞技池准入：绝对健康度阈值（Wilson 下界）
	// 实际分配反馈纠偏（基于 dispatched 的 EWMA actualShare）
	FeedbackEnabled        bool
	FeedbackEwma           float64 // EWMA 新样本权重
	FeedbackTolerance      float64 // 超额容忍度：ewmaActualShare 超过 targetShare 此值才降权
	FeedbackPenalty        float64 // 超额降权强度：1.0 - 超标量×此值
	FeedbackUpdateInterval int     // EWMA 更新触发间隔（组内转发次数）
}

func defaultAutoScheduleConfig() autoScheduleConfig {
	return autoScheduleConfig{
		ExploreRatio:            autoRankExploreRatio(),
		MinSamples:              autoRankMinSamples(),
		SuccessGap:              autoRankSuccessGap(),
		LatencyRatio:            autoRankLatencyRatio(),
		ChannelMaxShare:         autoRankChannelMaxShare(),
		ModelMaxShare:           autoRankModelMaxShare(),
		SoftmaxTemp:             autoRankSoftmaxTemp(),
		HealthThreshold:         autoRankHealthThreshold(),
		FeedbackEnabled:         autoRankFeedbackEnabled(),
		FeedbackEwma:            autoRankFeedbackEwma(),
		FeedbackTolerance:       autoRankFeedbackTolerance(),
		FeedbackPenalty:         autoRankFeedbackPenalty(),
		FeedbackUpdateInterval:  feedbackUpdateInterval,
	}
}

type autoCandidate struct {
	item           model.GroupItem
	stats          AutoRankStats
	effectiveScore float64
	tier           int
}

func newAutoCandidate(item model.GroupItem, stats AutoRankStats, effectiveScore float64) autoCandidate {
	return autoCandidate{item: item, stats: stats, effectiveScore: effectiveScore}
}

type autoDispatchState struct {
	lastOfferedSeq uint64
	offered        uint64
	dispatched     uint64
	lastDispatched time.Time
	targetShare    float64
	rank           int
	tier           int
	reason         string
	// 实际分配反馈纠偏状态（仅 feedbackEnabled 时使用）
	ewmaActualShare  float64 // EWMA 平滑的实际份额（基于 dispatched 的真实转发）
	feedbackPenalty  float64 // 当前降权系数：1.0=无惩罚，<1.0=降权，地板 feedbackMin
	activeDispatched uint64  // 自上次 EWMA 更新以来的 dispatched 计数
}

type autoGroupScheduleState struct {
	mu              sync.Mutex
	initialized     bool
	sequence        uint64
	exploreCredit   float64
	totalDispatched uint64
	lastSeen        time.Time
	candidates      map[string]*autoDispatchState
	channelOffered  map[int]uint64
	// 实际分配反馈纠偏状态
	totalActiveDispatched uint64 // 自上次 EWMA 更新以来全组 dispatched（与 activeDispatched 配对计算份额）
}

var globalAutoSchedule sync.Map // key: groupID -> *autoGroupScheduleState

func autoCandidateKey(channelID int, modelName string) string {
	return autoRankKey(channelID, modelName)
}

func getOrCreateAutoSchedule(groupID int) *autoGroupScheduleState {
	if v, ok := globalAutoSchedule.Load(groupID); ok {
		return v.(*autoGroupScheduleState)
	}
	s := &autoGroupScheduleState{
		candidates:            make(map[string]*autoDispatchState),
		channelOffered:        make(map[int]uint64),
		totalActiveDispatched: 0,
	}
	actual, _ := globalAutoSchedule.LoadOrStore(groupID, s)
	return actual.(*autoGroupScheduleState)
}

func scheduleAutoCandidates(groupID int, input []autoCandidate, cfg autoScheduleConfig) []autoCandidate {
	if len(input) == 0 {
		return nil
	}
	if cfg.MinSamples <= 0 {
		cfg.MinSamples = 3
	}
	if cfg.LatencyRatio < 1 {
		cfg.LatencyRatio = defaultAutoLatencyRatio
	}
	if cfg.ChannelMaxShare <= 0 || cfg.ChannelMaxShare > 1 {
		cfg.ChannelMaxShare = defaultAutoChannelMaxShare
	}
	if cfg.ModelMaxShare <= 0 || cfg.ModelMaxShare > 1 {
		cfg.ModelMaxShare = defaultAutoModelMaxShare
	}
	if cfg.SoftmaxTemp <= 0 {
		cfg.SoftmaxTemp = autoSoftmaxTemperature
	}
	if cfg.FeedbackEwma <= 0 || cfg.FeedbackEwma >= 1 {
		cfg.FeedbackEwma = 0.3
	}
	if cfg.FeedbackTolerance < 0 {
		cfg.FeedbackTolerance = 0.1
	}
	if cfg.FeedbackPenalty <= 0 {
		cfg.FeedbackPenalty = 0.3
	}
	if cfg.FeedbackUpdateInterval <= 0 {
		cfg.FeedbackUpdateInterval = feedbackUpdateInterval
	}

	candidates := append([]autoCandidate(nil), input...)
	for i := range candidates {
		candidates[i].tier = autoCandidateTier(candidates[i].stats, cfg.MinSamples)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].tier != candidates[j].tier {
			return candidates[i].tier > candidates[j].tier
		}
		if candidates[i].effectiveScore != candidates[j].effectiveScore {
			return candidates[i].effectiveScore > candidates[j].effectiveScore
		}
		if candidates[i].item.ChannelID != candidates[j].item.ChannelID {
			return candidates[i].item.ChannelID < candidates[j].item.ChannelID
		}
		return candidates[i].item.ModelName < candidates[j].item.ModelName
	})

	s := getOrCreateAutoSchedule(groupID)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.lastSeen = now
	s.sequence++
	reconcileAutoScheduleCandidates(s, candidates)

	if !s.initialized {
		s.initialized = true
		s.exploreCredit = 1
	} else {
		s.exploreCredit += math.Max(0, math.Min(1, cfg.ExploreRatio))
	}

	due := make([]autoCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		// 欠采样判据用真实样本：主动探测补的是成功率信号，不能顶替真实流量样本，
		// 否则被探测过的候选会退出探索池，却又因真实样本不足进不了竞技池而饿死。
		if candidate.stats.RealSamples() < cfg.MinSamples || candidate.stats.LastSeenAt.IsZero() || now.Sub(candidate.stats.LastSeenAt) >= AutoRankTimeWindow {
			due = append(due, candidate)
		}
	}
	competitive := competitiveAutoCandidates(candidates, cfg)
	updateAutoTargetShares(s, competitive, cfg)

	// 实际分配反馈纠偏：组内转发累计达到 FeedbackUpdateInterval 时，用 dispatched
	// 实际份额更新各候选的 EWMA actualShare 与 feedbackPenalty，供公平选择降权。
	if cfg.FeedbackEnabled && s.totalActiveDispatched >= uint64(cfg.FeedbackUpdateInterval) {
		updateAutoRankFeedbackEWMA(s, cfg)
	}

	var primary autoCandidate
	reason := "quality"
	// 只在真的要探索时才构造探索池：exploreCandidates 会逐个查渠道熔断状态，
	// 默认 20% 的探索比例下，每请求都算等于把这份开销放大五倍。
	var explorePool []autoCandidate
	if s.exploreCredit >= 1 {
		explorePool = exploreCandidates(due, candidates)
	}
	switch {
	case len(explorePool) > 0:
		s.exploreCredit -= 1
		primary = leastRecentlyOffered(s, explorePool)
		reason = "explore"
	case len(competitive) == 0:
		primary = candidates[0]
	default:
		primary = selectFairAutoCandidate(s, competitive, cfg)
		reason = "fair"
	}

	markAutoCandidateOffered(s, primary, reason)
	return autoFailoverOrder(primary, candidates)
}

// exploreCandidates 构造本轮可探索的候选池。
//
// 基础池是欠采样候选（due）；所有候选样本都够时退回全体，让探索退化为轮转。
// 从池中剔除两类"探索必然浪费"的候选：
//   - 渠道级熔断中：会被 relay 层 SkipCircuitBreak 直接跳过，不发请求也不积累样本；
//   - 探测已确认不可用（ProbeOnlyDead）：没有真实数据且每次探测都失败，拿真实
//     用户请求去撞只是重复一次已知的失败。
//
// 两类候选都仍留在 failover 链里——排除只影响"主动挑谁当首选"，不影响兜底。
// 返回空表示本轮没有值得探索的目标，调用方应把探索额度留到下次而不是白扣。
func exploreCandidates(due, all []autoCandidate) []autoCandidate {
	pool := due
	if len(pool) == 0 {
		pool = all
	}
	out := make([]autoCandidate, 0, len(pool))
	for _, c := range pool {
		if autoCandidateCircuitTripped(c) || c.stats.ProbeOnlyDead() {
			continue
		}
		out = append(out, c)
	}
	return out
}

// autoCandidateCircuitTripped 只读判断候选所在渠道是否处于熔断状态
// （Open 冷却中 / HalfOpen）。探索选择时用于排除必然被 relay 层跳过的候选。
// 只读：ChannelCircuitStatus 不推进熔断状态机（不会 Open → HalfOpen）。
func autoCandidateCircuitTripped(c autoCandidate) bool {
	tripped, _, _ := ChannelCircuitStatus(c.item.ChannelID)
	return tripped
}

// autoCandidateTier 按真实转发样本数划分档位。主动探测样本不参与：探测没有
// 有效延迟观测，让它把候选推进 tier 2 会使该候选在 effectiveScore 上"零延迟"
// 虚高，抢走真实快速候选的份额（见 AutoRankStats.RealSamples）。
func autoCandidateTier(stats AutoRankStats, minSamples int) int {
	real := stats.RealSamples()
	switch {
	case real >= minSamples:
		return 2
	case real > 0:
		return 1
	default:
		return 0
	}
}

func reconcileAutoScheduleCandidates(s *autoGroupScheduleState, candidates []autoCandidate) {
	present := make(map[string]struct{}, len(candidates))
	for rank, candidate := range candidates {
		key := autoCandidateKey(candidate.item.ChannelID, candidate.item.ModelName)
		present[key] = struct{}{}
		state := s.candidates[key]
		if state == nil {
			state = &autoDispatchState{feedbackPenalty: 1.0}
			s.candidates[key] = state
		}
		state.rank = rank + 1
		state.tier = candidate.tier
		state.targetShare = 0
		state.reason = "ranked"
	}
	for key := range s.candidates {
		if _, ok := present[key]; !ok {
			state := s.candidates[key]
			if state.dispatched <= s.totalDispatched {
				s.totalDispatched -= state.dispatched
			}
			channelID, _ := parseAutoRankKey(key)
			if state.offered <= s.channelOffered[channelID] {
				s.channelOffered[channelID] -= state.offered
			}
			if s.channelOffered[channelID] == 0 {
				delete(s.channelOffered, channelID)
			}
			delete(s.candidates, key)
		}
	}
}

func leastRecentlyOffered(s *autoGroupScheduleState, candidates []autoCandidate) autoCandidate {
	best := candidates[0]
	bestSeq := s.candidates[autoCandidateKey(best.item.ChannelID, best.item.ModelName)].lastOfferedSeq
	for _, candidate := range candidates[1:] {
		seq := s.candidates[autoCandidateKey(candidate.item.ChannelID, candidate.item.ModelName)].lastOfferedSeq
		if seq < bestSeq {
			best = candidate
			bestSeq = seq
		}
	}
	return best
}

func competitiveAutoCandidates(candidates []autoCandidate, cfg autoScheduleConfig) []autoCandidate {
	ready := make([]autoCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.tier == 2 {
			ready = append(ready, candidate)
		}
	}
	if len(ready) == 0 {
		return nil
	}
	bestConfidence := ready[0].stats.SuccessConfidence
	bestLatency := autoRankLatencyMS(ready[0].stats)
	for _, candidate := range ready[1:] {
		if candidate.stats.SuccessConfidence > bestConfidence {
			bestConfidence = candidate.stats.SuccessConfidence
		}
		latency := autoRankLatencyMS(candidate.stats)
		if latency > 0 && (bestLatency <= 0 || latency < bestLatency) {
			bestLatency = latency
		}
	}

	competitive := make([]autoCandidate, 0, len(ready))
	for _, candidate := range ready {
		// 双通道准入：
		//   - 绝对健康度达标（SuccessConfidence ≥ HealthThreshold）：不因"有略好的兄弟存在"被排除；
		//   - 相对差距达标（与 best 的成功率差距 ≤ SuccessGap 且延迟 ≤ bestLatency×LatencyRatio）。
		// 任一通道通过即进竞技池；HealthThreshold=0 表示禁用绝对通道，退化为纯相对差距判据。
		healthPass := cfg.HealthThreshold > 0 && candidate.stats.SuccessConfidence >= cfg.HealthThreshold
		relativePass := bestConfidence-candidate.stats.SuccessConfidence <= cfg.SuccessGap
		if bestLatency > 0 {
			latency := autoRankLatencyMS(candidate.stats)
			relativePass = relativePass && latency <= bestLatency*cfg.LatencyRatio
		}
		if !healthPass && !relativePass {
			continue
		}
		competitive = append(competitive, candidate)
	}
	return competitive
}

func updateAutoTargetShares(s *autoGroupScheduleState, candidates []autoCandidate, cfg autoScheduleConfig) {
	byChannel := make(map[int][]autoCandidate)
	channelScores := make(map[int]float64)
	for _, candidate := range candidates {
		channelID := candidate.item.ChannelID
		byChannel[channelID] = append(byChannel[channelID], candidate)
		if score, ok := channelScores[channelID]; !ok || candidate.effectiveScore > score {
			channelScores[channelID] = candidate.effectiveScore
		}
	}
	for channelID, channelTarget := range cappedSoftmax(channelScores, cfg.ChannelMaxShare, cfg.SoftmaxTemp) {
		modelScores := make(map[string]float64, len(byChannel[channelID]))
		for _, candidate := range byChannel[channelID] {
			key := autoCandidateKey(candidate.item.ChannelID, candidate.item.ModelName)
			modelScores[key] = candidate.effectiveScore
		}
		for key, modelTarget := range cappedSoftmax(modelScores, cfg.ModelMaxShare, cfg.SoftmaxTemp) {
			s.candidates[key].targetShare = channelTarget * modelTarget
		}
	}
}

func selectFairAutoCandidate(s *autoGroupScheduleState, candidates []autoCandidate, cfg autoScheduleConfig) autoCandidate {
	byChannel := make(map[int][]autoCandidate)
	channelScores := make(map[int]float64)
	for _, candidate := range candidates {
		channelID := candidate.item.ChannelID
		byChannel[channelID] = append(byChannel[channelID], candidate)
		if score, ok := channelScores[channelID]; !ok || candidate.effectiveScore > score {
			channelScores[channelID] = candidate.effectiveScore
		}
	}
	channelTargets := cappedSoftmax(channelScores, cfg.ChannelMaxShare, cfg.SoftmaxTemp)
	selectedChannel := 0
	bestDebt := math.Inf(1)
	bestScore := math.Inf(-1)
	for channelID, target := range channelTargets {
		// 渠道层降权取渠道内候选 feedbackPenalty 的最小值：任一候选被实际分配反馈罚
		// 即整体缩小渠道目标份额，防止"渠道内单模型超额"演变成渠道级垄断。
		penalty := 1.0
		if cfg.FeedbackEnabled {
			for _, candidate := range byChannel[channelID] {
				state := s.candidates[autoCandidateKey(candidate.item.ChannelID, candidate.item.ModelName)]
				if state != nil && state.feedbackPenalty > 0 && state.feedbackPenalty < penalty {
					penalty = state.feedbackPenalty
				}
			}
		}
		adjustedTarget := target * penalty
		if adjustedTarget <= 0 {
			adjustedTarget = target * feedbackMin
		}
		debt := float64(s.channelOffered[channelID]+1) / adjustedTarget
		if debt < bestDebt ||
			(debt == bestDebt && channelScores[channelID] > bestScore) ||
			(debt == bestDebt && channelScores[channelID] == bestScore && (selectedChannel == 0 || channelID < selectedChannel)) {
			selectedChannel = channelID
			bestDebt = debt
			bestScore = channelScores[channelID]
		}
	}

	modelScores := make(map[string]float64)
	modelByKey := make(map[string]autoCandidate)
	for _, candidate := range byChannel[selectedChannel] {
		key := autoCandidateKey(candidate.item.ChannelID, candidate.item.ModelName)
		modelScores[key] = candidate.effectiveScore
		modelByKey[key] = candidate
	}
	modelTargets := cappedSoftmax(modelScores, cfg.ModelMaxShare, cfg.SoftmaxTemp)
	selectedKey := ""
	bestDebt = math.Inf(1)
	bestScore = math.Inf(-1)
	for key, target := range modelTargets {
		state := s.candidates[key]
		penalty := 1.0
		if cfg.FeedbackEnabled && state != nil && state.feedbackPenalty > 0 {
			penalty = state.feedbackPenalty
		}
		adjustedTarget := target * penalty
		if adjustedTarget <= 0 {
			adjustedTarget = target * feedbackMin
		}
		debt := float64(state.offered+1) / adjustedTarget
		if debt < bestDebt ||
			(debt == bestDebt && modelScores[key] > bestScore) ||
			(debt == bestDebt && modelScores[key] == bestScore && (selectedKey == "" || key < selectedKey)) {
			selectedKey = key
			bestDebt = debt
			bestScore = modelScores[key]
		}
	}

	return modelByKey[selectedKey]
}

func cappedSoftmax[K comparable](scores map[K]float64, maxShare, temperature float64) map[K]float64 {
	weights := make(map[K]float64, len(scores))
	if len(scores) == 0 {
		return weights
	}
	best := math.Inf(-1)
	for _, score := range scores {
		if score > best {
			best = score
		}
	}
	total := 0.0
	for key, score := range scores {
		weight := math.Exp((score - best) / temperature)
		weights[key] = weight
		total += weight
	}
	for key := range weights {
		weights[key] /= total
	}
	if len(weights) == 1 || maxShare >= 1 {
		return weights
	}
	for iteration := 0; iteration < len(weights); iteration++ {
		var cappedKey K
		found := false
		for key, share := range weights {
			if share > maxShare {
				cappedKey = key
				found = true
				break
			}
		}
		if !found {
			break
		}
		remainingTotal := 0.0
		for key, share := range weights {
			if key != cappedKey {
				remainingTotal += share
			}
		}
		weights[cappedKey] = maxShare
		if remainingTotal <= 0 {
			break
		}
		scale := (1 - maxShare) / remainingTotal
		for key := range weights {
			if key != cappedKey {
				weights[key] *= scale
			}
		}
	}
	return weights
}

func markAutoCandidateOffered(s *autoGroupScheduleState, candidate autoCandidate, reason string) {
	key := autoCandidateKey(candidate.item.ChannelID, candidate.item.ModelName)
	state := s.candidates[key]
	state.lastOfferedSeq = s.sequence
	state.offered++
	state.reason = reason
	s.channelOffered[candidate.item.ChannelID]++
}

func autoFailoverOrder(primary autoCandidate, ranked []autoCandidate) []autoCandidate {
	result := make([]autoCandidate, 0, len(ranked))
	result = append(result, primary)
	remaining := make([]autoCandidate, 0, len(ranked)-1)
	primaryKey := autoCandidateKey(primary.item.ChannelID, primary.item.ModelName)
	for _, candidate := range ranked {
		if autoCandidateKey(candidate.item.ChannelID, candidate.item.ModelName) != primaryKey {
			remaining = append(remaining, candidate)
		}
	}
	lastChannel := primary.item.ChannelID
	for len(remaining) > 0 {
		idx := 0
		for i, candidate := range remaining {
			if candidate.item.ChannelID != lastChannel {
				idx = i
				break
			}
		}
		selected := remaining[idx]
		result = append(result, selected)
		lastChannel = selected.item.ChannelID
		remaining = append(remaining[:idx], remaining[idx+1:]...)
	}
	return result
}

// updateAutoRankFeedbackEWMA 用 dispatched 实际份额更新候选的 EWMA actualShare 与
// feedbackPenalty。调用方需持有 s.mu。
//
//   - instantShare = activeDispatched / totalActiveDispatched（本轮窗口内真实转发占比）；
//   - ewmaActualShare = alpha*instantShare + (1-alpha)*ewmaActualShare；
//   - excess = max(0, ewmaActualShare - targetShare - tolerance)；
//   - excess > 0 时 feedbackPenalty = max(feedbackMin, 1 - excess*penaltyStrength)，
//     否则向 1.0 平滑恢复（alpha 为恢复权重）。
func updateAutoRankFeedbackEWMA(s *autoGroupScheduleState, cfg autoScheduleConfig) {
	total := s.totalActiveDispatched
	if total == 0 {
		return
	}
	alpha := cfg.FeedbackEwma
	tolerance := cfg.FeedbackTolerance
	strength := cfg.FeedbackPenalty
	for _, state := range s.candidates {
		instantShare := float64(state.activeDispatched) / float64(total)
		state.ewmaActualShare = alpha*instantShare + (1-alpha)*state.ewmaActualShare

		excess := state.ewmaActualShare - state.targetShare - tolerance
		if excess > 0 {
			p := 1.0 - excess*strength
			if p < feedbackMin {
				p = feedbackMin
			}
			state.feedbackPenalty = p
		} else {
			// EWMA 平滑恢复到 1.0
			state.feedbackPenalty = alpha*1.0 + (1-alpha)*state.feedbackPenalty
		}
		state.activeDispatched = 0
	}
	s.totalActiveDispatched = 0
}

func RecordAutoDispatch(groupID, channelID int, modelName string) {
	if groupID <= 0 {
		return
	}
	s := getOrCreateAutoSchedule(groupID)
	s.mu.Lock()
	defer s.mu.Unlock()
	key := autoCandidateKey(channelID, modelName)
	state := s.candidates[key]
	if state == nil {
		state = &autoDispatchState{feedbackPenalty: 1.0}
		s.candidates[key] = state
	}
	s.lastSeen = time.Now()
	state.dispatched++
	state.lastDispatched = time.Now()
	s.totalDispatched++
	// 实际分配反馈记账：仅计数，EWMA 更新由调度侧（scheduleAutoCandidates）
	// 在下次 Candidates() 时按 FeedbackUpdateInterval 统一触发。
	if autoRankFeedbackEnabled() {
		state.activeDispatched++
		s.totalActiveDispatched++
	}
}

type AutoDispatchStats struct {
	Rank           int
	Tier           int
	TargetShare    float64
	ActualShare    float64
	LastDispatched time.Time
	Reason         string
}

func GetAutoDispatchStats(groupID, channelID int, modelName string) AutoDispatchStats {
	v, ok := globalAutoSchedule.Load(groupID)
	if !ok {
		return AutoDispatchStats{}
	}
	s := v.(*autoGroupScheduleState)
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.candidates[autoCandidateKey(channelID, modelName)]
	if state == nil {
		return AutoDispatchStats{}
	}
	actualShare := 0.0
	if s.totalDispatched > 0 {
		actualShare = float64(state.dispatched) / float64(s.totalDispatched)
	}
	return AutoDispatchStats{
		Rank:           state.rank,
		Tier:           state.tier,
		TargetShare:    state.targetShare,
		ActualShare:    actualShare,
		LastDispatched: state.lastDispatched,
		Reason:         state.reason,
	}
}
