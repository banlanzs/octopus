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
)

type autoScheduleConfig struct {
	ExploreRatio    float64
	MinSamples      int
	SuccessGap      float64
	LatencyRatio    float64
	ChannelMaxShare float64
	ModelMaxShare   float64
}

func defaultAutoScheduleConfig() autoScheduleConfig {
	return autoScheduleConfig{
		ExploreRatio:    autoRankExploreRatio(),
		MinSamples:      autoRankMinSamples(),
		SuccessGap:      defaultAutoSuccessGap,
		LatencyRatio:    defaultAutoLatencyRatio,
		ChannelMaxShare: defaultAutoChannelMaxShare,
		ModelMaxShare:   defaultAutoModelMaxShare,
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
		candidates:     make(map[string]*autoDispatchState),
		channelOffered: make(map[int]uint64),
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
		if candidate.stats.Samples < cfg.MinSamples || candidate.stats.LastSeenAt.IsZero() || now.Sub(candidate.stats.LastSeenAt) >= AutoRankTimeWindow {
			due = append(due, candidate)
		}
	}
	competitive := competitiveAutoCandidates(candidates, cfg)
	updateAutoTargetShares(s, competitive, cfg)

	var primary autoCandidate
	reason := "quality"
	if s.exploreCredit >= 1 {
		s.exploreCredit -= 1
		pool := due
		if len(pool) == 0 {
			pool = candidates
		}
		primary = leastRecentlyOffered(s, pool)
		reason = "explore"
	} else {
		if len(competitive) == 0 {
			primary = candidates[0]
		} else {
			primary = selectFairAutoCandidate(s, competitive, cfg)
			reason = "fair"
		}
	}

	markAutoCandidateOffered(s, primary, reason)
	return autoFailoverOrder(primary, candidates)
}

func autoCandidateTier(stats AutoRankStats, minSamples int) int {
	switch {
	case stats.Samples >= minSamples:
		return 2
	case stats.Samples > 0:
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
			state = &autoDispatchState{}
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
		if bestConfidence-candidate.stats.SuccessConfidence > cfg.SuccessGap {
			continue
		}
		latency := autoRankLatencyMS(candidate.stats)
		if bestLatency > 0 && latency > bestLatency*cfg.LatencyRatio {
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
	for channelID, channelTarget := range cappedSoftmax(channelScores, cfg.ChannelMaxShare) {
		modelScores := make(map[string]float64, len(byChannel[channelID]))
		for _, candidate := range byChannel[channelID] {
			key := autoCandidateKey(candidate.item.ChannelID, candidate.item.ModelName)
			modelScores[key] = candidate.effectiveScore
		}
		for key, modelTarget := range cappedSoftmax(modelScores, cfg.ModelMaxShare) {
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
	channelTargets := cappedSoftmax(channelScores, cfg.ChannelMaxShare)
	selectedChannel := 0
	bestDebt := math.Inf(1)
	bestScore := math.Inf(-1)
	for channelID, target := range channelTargets {
		debt := float64(s.channelOffered[channelID]+1) / target
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
	modelTargets := cappedSoftmax(modelScores, cfg.ModelMaxShare)
	selectedKey := ""
	bestDebt = math.Inf(1)
	bestScore = math.Inf(-1)
	for key, target := range modelTargets {
		state := s.candidates[key]
		debt := float64(state.offered+1) / target
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

func cappedSoftmax[K comparable](scores map[K]float64, maxShare float64) map[K]float64 {
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
		weight := math.Exp((score - best) / autoSoftmaxTemperature)
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
		state = &autoDispatchState{}
		s.candidates[key] = state
	}
	s.lastSeen = time.Now()
	state.dispatched++
	state.lastDispatched = time.Now()
	s.totalDispatched++
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
