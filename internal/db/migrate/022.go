package migrate

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/model"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 22,
		Up:      migrateAddChannelTagsRedirectAndAPIKeyTags,
	})
}

// migrateAddChannelTagsRedirectAndAPIKeyTags 兜底补齐新增列：
//   - channels: tags、model_redirects、model_redirect_only
//   - api_keys: supported_tags
//
// 新列通常已由 AutoMigrate 添加；此迁移只负责旧库缺列时补建与回填默认值。
func migrateAddChannelTagsRedirectAndAPIKeyTags(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if db.Migrator().HasTable(&model.Channel{}) {
		for _, field := range []string{"Tags", "ModelRedirects", "ModelRedirectOnly"} {
			if !db.Migrator().HasColumn(&model.Channel{}, field) {
				if err := db.Migrator().AddColumn(&model.Channel{}, field); err != nil {
					return fmt.Errorf("add channels.%s: %w", field, err)
				}
			}
		}
		if err := db.Model(&model.Channel{}).
			Where("model_redirect_only IS NULL").
			Update("model_redirect_only", false).Error; err != nil {
			return err
		}
	}
	if db.Migrator().HasTable(&model.APIKey{}) {
		if !db.Migrator().HasColumn(&model.APIKey{}, "SupportedTags") {
			if err := db.Migrator().AddColumn(&model.APIKey{}, "SupportedTags"); err != nil {
				return fmt.Errorf("add api_keys.supported_tags: %w", err)
			}
		}
	}
	return nil
}
