package migrate

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 19,
		Up:      migrateAddChannelPriceMultiplier,
	})
}

// migrateAddChannelPriceMultiplier 给 channels 表加 price_multiplier 列并回填默认 1。
// 新列由 AutoMigrate 自动添加；此迁移仅做兜底（列缺失时补建 + 回填）。
func migrateAddChannelPriceMultiplier(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable(&model.Channel{}) {
		return nil
	}
	if !db.Migrator().HasColumn(&model.Channel{}, "PriceMultiplier") {
		if err := db.Migrator().AddColumn(&model.Channel{}, "PriceMultiplier"); err != nil {
			return err
		}
	}
	return db.Model(&model.Channel{}).
		Where("price_multiplier = 0 OR price_multiplier IS NULL").
		Update("price_multiplier", 1).Error
}
