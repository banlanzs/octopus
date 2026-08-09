package op

import (
	"context"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm/clause"
)

// AutoRankSnapshotListAll 读取全部自动排序性能快照（启动时恢复内存窗口用）。
func AutoRankSnapshotListAll(ctx context.Context) ([]model.AutoRankSnapshot, error) {
	var snaps []model.AutoRankSnapshot
	if err := db.GetDB().WithContext(ctx).Find(&snaps).Error; err != nil {
		return nil, err
	}
	return snaps, nil
}

// AutoRankSnapshotUpsertAll 批量 upsert 性能快照。
// 控制面任务周期性调用；键为 (group_id, channel_id, model_name)。
func AutoRankSnapshotUpsertAll(ctx context.Context, snaps []model.AutoRankSnapshot) error {
	if len(snaps) == 0 {
		return nil
	}
	return db.GetDB().WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "group_id"},
			{Name: "channel_id"},
			{Name: "model_name"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"samples", "failures", "success_rate", "ewma_latency_ms", "updated_at",
		}),
	}).Create(&snaps).Error
}
