package migrate

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 23,
		Up:      migrateAddChannelGroups,
	})
}

// migrateAddChannelGroups 兜底补齐新增列：
//   - channels: channel_groups
//
// 新列通常已由 AutoMigrate 添加；此迁移只负责旧库缺列时补建。
func migrateAddChannelGroups(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if db.Migrator().HasTable(&model.Channel{}) {
		if !db.Migrator().HasColumn(&model.Channel{}, "ChannelGroups") {
			if err := db.Migrator().AddColumn(&model.Channel{}, "ChannelGroups"); err != nil {
				return fmt.Errorf("add channels.channel_groups: %w", err)
			}
		}
	}
	return nil
}
