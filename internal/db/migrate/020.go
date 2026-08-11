package migrate

import (
	"fmt"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/utils/log"
	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 20,
		Up:      migrateRelayLogRequestPath,
	})
}

// migrateRelayLogRequestPath 给 relay_logs 加 request_path 列（日志页展示客户端请求路径）。
// 与 013 迁移同策略：
//   - SQLite：用裸 ALTER TABLE 加列，避免触发 glebarez 的 AlterColumn →
//     recreateTable 全表拷贝（GB 级表会直接 OOM）。
//   - MySQL/Postgres：主流程 AutoMigrate 已包含 RelayLog，加列由它完成；
//     这里仅兜底调用一次 AutoMigrate，列已存在时是幂等 no-op。
func migrateRelayLogRequestPath(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	if !db.Migrator().HasTable("relay_logs") {
		return nil
	}
	if hasRelayLogColumn(db, "request_path") {
		return nil
	}

	start := time.Now()
	log.Infow("migration.relay_logs_request_path.start", "dialect", db.Dialector.Name())
	if db.Dialector.Name() == "sqlite" {
		// 列类型字面量对齐 GORM SQLite dialector 给 string 字段生成的 "text"，
		// 避免后续 MigrateColumn 判定 schema drift 触发 recreateTable。
		if err := db.Exec("ALTER TABLE relay_logs ADD COLUMN request_path text").Error; err != nil {
			return err
		}
	} else {
		if err := db.AutoMigrate(&model.RelayLog{}); err != nil {
			return err
		}
	}
	log.Infow("migration.relay_logs_request_path.done", "duration", time.Since(start).String())
	return nil
}
