package task

import "testing"

func probeCandidate(channelID int, modelName string) autoRankProbeCandidate {
	return autoRankProbeCandidate{
		key: autoRankProbeKey{groupID: 1, channelID: channelID, modelName: modelName},
	}
}

// 新增渠道名下所有模型的真实样本都是 0，排序后会整片挤在队列最前。名额必须摊到
// 不同渠道，否则一轮会向同一个 key 连发十次——正是中转站风控要拦的形态。
func TestPickAutoRankProbeTargetsSpreadsAcrossChannels(t *testing.T) {
	candidates := []autoRankProbeCandidate{
		probeCandidate(7, "m1"),
		probeCandidate(7, "m2"),
		probeCandidate(7, "m3"),
		probeCandidate(8, "m1"),
		probeCandidate(9, "m1"),
	}

	picked := pickAutoRankProbeTargets(candidates, 10, 1)
	if len(picked) != 3 {
		t.Fatalf("expected one target per channel (3 channels), got %d", len(picked))
	}
	perChannel := make(map[int]int, len(picked))
	for _, c := range picked {
		perChannel[c.key.channelID]++
	}
	for channelID, n := range perChannel {
		if n != 1 {
			t.Fatalf("channel %d took %d probe slots, want 1", channelID, n)
		}
	}
	// 配额之内仍保持排序优先级：欠采样最严重的候选先入选。
	if picked[0].key.channelID != 7 || picked[0].key.modelName != "m1" {
		t.Fatalf("expected the highest-priority candidate first, got %+v", picked[0].key)
	}
}

// 总数上限在按渠道摊完名额之后才生效。
func TestPickAutoRankProbeTargetsHonorsMaxPerRound(t *testing.T) {
	candidates := []autoRankProbeCandidate{
		probeCandidate(1, "m"),
		probeCandidate(2, "m"),
		probeCandidate(3, "m"),
	}

	if picked := pickAutoRankProbeTargets(candidates, 2, 1); len(picked) != 2 {
		t.Fatalf("expected 2 targets under maxPerRound=2, got %d", len(picked))
	}
	if picked := pickAutoRankProbeTargets(candidates, 0, 1); len(picked) != 0 {
		t.Fatalf("expected no targets when maxPerRound is 0, got %d", len(picked))
	}
}

// 单渠道多模型时，一轮只放行一个，其余留给后续轮次轮转覆盖。
func TestPickAutoRankProbeTargetsSingleChannelTakesOneSlot(t *testing.T) {
	candidates := []autoRankProbeCandidate{
		probeCandidate(5, "m1"),
		probeCandidate(5, "m2"),
		probeCandidate(5, "m3"),
	}

	picked := pickAutoRankProbeTargets(candidates, 10, autoRankProbeChannelQuota)
	if len(picked) != 1 {
		t.Fatalf("a single channel must not take more than one slot per round, got %d", len(picked))
	}
}
