package task

import (
	"context"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/utils/log"
)

// AutoRankTask 自动排序(Auto)控制面任务：
// 将内存学习窗口周期性落库为 AutoRankSnapshot，供重启恢复。
//
// 数据来源以被动学习（真实请求）为主：冷启动/低流量候选由 balancer 的确定性
// 有界探索在真实流量中按比例获取样本。用户显式开启主动探测后，AutoRankProbeTask
// 会额外为欠采样候选补成功率样本（只补成功率，不写延迟、不推进档位）。
func AutoRankTask() {
	now := time.Now()
	defer func() {
		if reaped := balancer.AutoRankReap(now, 30*time.Minute); reaped > 0 {
			log.Debugf("auto-rank reaped %d idle windows", reaped)
		}
	}()

	enabled, err := op.SettingGetBool(model.SettingKeyAutoRankEnabled)
	if err != nil || !enabled {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	groups, err := op.GroupList(ctx)
	if err != nil {
		log.Warnf("auto rank list groups failed: %v", err)
		return
	}

	type snapshotKey struct {
		groupID   int
		channelID int
		modelName string
	}
	statByKey := make(map[snapshotKey]balancer.AutoRankStats)
	for _, ks := range balancer.AutoRankAllStats() {
		statByKey[snapshotKey{groupID: ks.GroupID, channelID: ks.ChannelID, modelName: ks.ModelName}] = ks.Stats
	}

	var snaps []model.AutoRankSnapshot
	for _, group := range groups {
		if group.Mode != model.GroupModeAuto {
			continue
		}
		for _, item := range group.Items {
			st, ok := statByKey[snapshotKey{groupID: group.ID, channelID: item.ChannelID, modelName: item.ModelName}]
			if !ok {
				continue
			}
			snaps = append(snaps, model.AutoRankSnapshot{
				GroupID:       group.ID,
				ChannelID:     item.ChannelID,
				ModelName:     item.ModelName,
				Samples:       st.Samples,
				ProbeSamples:  st.ProbeSamples,
				Failures:      st.Failures,
				SuccessRate:   st.SuccessRate,
				EWMALatencyMS: st.EWMALatencyMS,
				EWMATTFBMS:    st.EWMATTFBMS,
				LastSeenAt:    st.LastSeenAt,
				UpdatedAt:     now,
			})
		}
	}
	if err := op.AutoRankSnapshotReplaceAll(ctx, snaps); err != nil {
		log.Warnf("auto rank snapshot replace failed: %v", err)
	}
}
