package op

import (
	"context"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

// AutoRankSnapshotListAll 读取全部自动排序性能快照（启动时恢复内存窗口用）。
func AutoRankSnapshotListAll(ctx context.Context) ([]model.AutoRankSnapshot, error) {
	var snaps []model.AutoRankSnapshot
	if err := db.GetDB().WithContext(ctx).Find(&snaps).Error; err != nil {
		return nil, err
	}
	return snaps, nil
}

// autoRankSnapshotKey 快照唯一键，与表唯一索引 (group_id, channel_id, model_name) 一致。
type autoRankSnapshotKey struct {
	groupID   int
	channelID int
	modelName string
}

// AutoRankSnapshotSync 差异化同步全部性能快照：无变化跳过、变化行 UPDATE、
// 新增行 INSERT、消失键（已删除分组/已移除成员）DELETE。控制面内存窗口是
// 事实源；相比整体替换，流量稳定时几乎零写入，降低大部署下 DB 压力。
func AutoRankSnapshotSync(ctx context.Context, snaps []model.AutoRankSnapshot) error {
	return db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []model.AutoRankSnapshot
		if err := tx.Find(&existing).Error; err != nil {
			return err
		}
		byKey := make(map[autoRankSnapshotKey]model.AutoRankSnapshot, len(existing))
		for _, row := range existing {
			byKey[autoRankSnapshotKey{row.GroupID, row.ChannelID, row.ModelName}] = row
		}
		seen := make(map[autoRankSnapshotKey]bool, len(snaps))
		for _, s := range snaps {
			k := autoRankSnapshotKey{s.GroupID, s.ChannelID, s.ModelName}
			seen[k] = true
			old, ok := byKey[k]
			if !ok {
				if err := tx.Create(&s).Error; err != nil {
					return err
				}
				continue
			}
			if autoRankSnapshotsEqual(old, s) {
				continue
			}
			updates := map[string]any{
				"samples":         s.Samples,
				"probe_samples":   s.ProbeSamples,
				"failures":        s.Failures,
				"success_rate":    s.SuccessRate,
				"ewma_latency_ms": s.EWMALatencyMS,
				// 列名由 GORM 命名策略从字段 EWMATTFBMS 生成（EWM+ATTFBMS → ewmattfbms），
				// 不是 json tag；历史表结构即如此，map 更新必须匹配实际列名。
				"ewmattfbms":  s.EWMATTFBMS,
				"last_seen_at": s.LastSeenAt,
				"sample_trail": s.SampleTrail,
				"updated_at":   s.UpdatedAt,
			}
			if err := tx.Model(&model.AutoRankSnapshot{}).Where("id = ?", old.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
		for k, row := range byKey {
			if !seen[k] {
				if err := tx.Delete(&model.AutoRankSnapshot{}, row.ID).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// autoRankSnapshotsEqual 判断两条快照的统计内容是否一致（忽略 ID 等管理字段）。
func autoRankSnapshotsEqual(a, b model.AutoRankSnapshot) bool {
	return a.Samples == b.Samples &&
		a.ProbeSamples == b.ProbeSamples &&
		a.Failures == b.Failures &&
		a.SuccessRate == b.SuccessRate &&
		a.EWMALatencyMS == b.EWMALatencyMS &&
		a.EWMATTFBMS == b.EWMATTFBMS &&
		a.LastSeenAt.Equal(b.LastSeenAt) &&
		a.SampleTrail == b.SampleTrail
}
