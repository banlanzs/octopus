package relay

import (
	"net/http"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
)

func TestRecordAutoRankResultHonorsModeSwitchAndFailureFilter(t *testing.T) {
	setupRelayTestDB(t)
	balancer.AutoRankReset()
	t.Cleanup(balancer.AutoRankReset)

	group := model.Group{ID: 41, Mode: model.GroupModeAuto}
	if err := op.SettingSetString(model.SettingKeyAutoRankEnabled, "false"); err != nil {
		t.Fatalf("disable auto rank failed: %v", err)
	}
	recordAutoRankResult(group, 7, "m1", true, http.StatusOK, 100, 50)
	if got := balancer.GetAutoRankStatsForGroup(group.ID, 7, "m1").Samples; got != 0 {
		t.Fatalf("disabled auto rank recorded %d samples", got)
	}

	if err := op.SettingSetString(model.SettingKeyAutoRankEnabled, "true"); err != nil {
		t.Fatalf("enable auto rank failed: %v", err)
	}
	nonAutoGroup := model.Group{ID: 42, Mode: model.GroupModeFailover}
	recordAutoRankResult(nonAutoGroup, 7, "m1", true, http.StatusOK, 100, 50)
	if got := balancer.GetAutoRankStatsForGroup(nonAutoGroup.ID, 7, "m1").Samples; got != 0 {
		t.Fatalf("non-auto group recorded %d samples", got)
	}

	recordAutoRankResult(group, 7, "m1", false, http.StatusTooManyRequests, 100, 100)
	if got := balancer.GetAutoRankStatsForGroup(group.ID, 7, "m1").Samples; got != 0 {
		t.Fatalf("filtered failure recorded %d samples", got)
	}

	recordAutoRankResult(group, 7, "m1", true, http.StatusOK, 100, 50)
	recordAutoRankResult(group, 7, "m1", false, http.StatusInternalServerError, 120, 120)
	stats := balancer.GetAutoRankStatsForGroup(group.ID, 7, "m1")
	if stats.Samples != 2 || stats.Failures != 1 || stats.EWMATTFBMS != 50 {
		t.Fatalf("unexpected auto rank stats: %+v", stats)
	}
}

func TestAutoRankCandidateTimingsIncludeEarlierRetries(t *testing.T) {
	candidateStartedAt := time.Now().Add(-100 * time.Millisecond)
	finalAttemptStartedAt := candidateStartedAt.Add(60 * time.Millisecond)
	durationMS, ttfbMS := autoRankCandidateTimings(candidateStartedAt, finalAttemptStartedAt, attemptResult{TTFBMS: 20})
	if durationMS < 90 {
		t.Fatalf("expected complete candidate duration, got %dms", durationMS)
	}
	if ttfbMS != 80 {
		t.Fatalf("expected retry-aware TTFB 80ms, got %dms", ttfbMS)
	}
}
