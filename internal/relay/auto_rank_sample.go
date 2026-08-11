package relay

import (
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
)

func recordAutoRankResult(group dbmodel.Group, channelID int, modelName string, success bool, statusCode int, durationMS, ttfbMS int64) {
	if group.Mode != dbmodel.GroupModeAuto || group.ID <= 0 {
		return
	}
	enabled, err := op.SettingGetBool(dbmodel.SettingKeyAutoRankEnabled)
	if err != nil || !enabled {
		return
	}
	if !success && !isAutoRankCountableFailure(statusCode) {
		return
	}
	if durationMS < 0 {
		durationMS = 0
	}
	if ttfbMS <= 0 {
		ttfbMS = durationMS
	}
	balancer.RecordAutoSampleForGroup(group.ID, channelID, modelName, success, durationMS, ttfbMS)
}

func autoRankCandidateTimings(candidateStartedAt, finalAttemptStartedAt time.Time, result attemptResult) (durationMS, ttfbMS int64) {
	durationMS = time.Since(candidateStartedAt).Milliseconds()
	if finalAttemptStartedAt.IsZero() || result.TTFBMS <= 0 {
		return durationMS, durationMS
	}
	ttfbMS = finalAttemptStartedAt.Sub(candidateStartedAt).Milliseconds() + result.TTFBMS
	if ttfbMS <= 0 || ttfbMS > durationMS {
		ttfbMS = durationMS
	}
	return durationMS, ttfbMS
}

func (ra *relayAttempt) markFirstToken() {
	if ra == nil {
		return
	}
	now := time.Now()
	if ra.firstTokenUnixNano.CompareAndSwap(0, now.UnixNano()) {
		ra.metrics.SetFirstTokenTime(now)
	}
}

func (ra *relayAttempt) autoRankTTFBMS(startedAt time.Time, fallbackMS int64) int64 {
	if ra == nil {
		return fallbackMS
	}
	ns := ra.firstTokenUnixNano.Load()
	if ns <= 0 {
		return fallbackMS
	}
	d := time.Unix(0, ns).Sub(startedAt).Milliseconds()
	if d <= 0 {
		return fallbackMS
	}
	return d
}
