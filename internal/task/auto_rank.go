package task

import (
	"context"
	"fmt"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/utils/log"
)

// AutoRankTask 自动排序(Auto)控制面任务：
// 将内存学习窗口周期性落库为 AutoRankSnapshot，供重启恢复。
//
// 数据来源为纯被动学习（真实请求），不发起任何主动探测——中转站通常拒绝
// 无业务意图的极小请求（ping + 1 token）；冷启动/低流量候选由 balancer 的
// epsilon 探索在真实流量中按比例获取样本。
func AutoRankTask() {
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

	statByKey := make(map[string]balancer.AutoRankStats)
	for _, ks := range balancer.AutoRankAllStats() {
		statByKey[fmt.Sprintf("%d:%s", ks.ChannelID, ks.ModelName)] = ks.Stats
	}

	var snaps []model.AutoRankSnapshot
	for _, group := range groups {
		if group.Mode != model.GroupModeAuto {
			continue
		}
		for _, item := range group.Items {
			st, ok := statByKey[fmt.Sprintf("%d:%s", item.ChannelID, item.ModelName)]
			if !ok {
				continue
			}
			snaps = append(snaps, model.AutoRankSnapshot{
				GroupID:       group.ID,
				ChannelID:     item.ChannelID,
				ModelName:     item.ModelName,
				Samples:       st.Samples,
				Failures:      st.Failures,
				SuccessRate:   st.SuccessRate,
				EWMALatencyMS: st.EWMALatencyMS,
				UpdatedAt:     time.Now(),
			})
		}
	}
	if len(snaps) > 0 {
		if err := op.AutoRankSnapshotUpsertAll(ctx, snaps); err != nil {
			log.Warnf("auto rank snapshot upsert failed: %v", err)
		}
	}

	if reaped := balancer.AutoRankReap(time.Now(), 30*time.Minute); reaped > 0 {
		log.Debugf("auto-rank reaped %d idle windows", reaped)
	}
}
