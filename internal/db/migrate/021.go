package migrate

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 21,
		Up:      migrateAddChannelSchedulingExempt,
	})
}

// migrateAddChannelSchedulingExempt 给 channels 表加 scheduling_exempt 列。
// 新列由 AutoMigrate 自动添加；此迁移仅做兜底（列缺失时补建 + 回填默认 false）。
func migrateAddChannelSchedulingExempt(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable(&model.Channel{}) {
		return nil
	}
	if !db.Migrator().HasColumn(&model.Channel{}, "SchedulingExempt") {
		if err := db.Migrator().AddColumn(&model.Channel{}, "SchedulingExempt"); err != nil {
			return err
		}
	}
	return db.Model(&model.Channel{}).
		Where("scheduling_exempt IS NULL").
		Update("scheduling_exempt", false).Error
}
