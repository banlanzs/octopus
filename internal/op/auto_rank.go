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

// AutoRankSnapshotReplaceAll 原子替换全部性能快照。
// 控制面内存窗口是事实源；整体替换同时清理已删除分组与已移除成员的陈旧行。
func AutoRankSnapshotReplaceAll(ctx context.Context, snaps []model.AutoRankSnapshot) error {
	return db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&model.AutoRankSnapshot{}).Error; err != nil {
			return err
		}
		if len(snaps) == 0 {
			return nil
		}
		return tx.Create(&snaps).Error
	})
}
