package migrate

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestMigrateRelayLogRequestPathAddsColumn 验证迁移在旧版 relay_logs 表（无
// request_path 列）上幂等地补列，且不丢已有数据、可正常写入新列。
func TestMigrateRelayLogRequestPathAddsColumn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	// 模拟旧 schema：relay_logs 表存在但无 request_path 列
	if err := db.Exec(`CREATE TABLE relay_logs (
		id INTEGER PRIMARY KEY,
		time INTEGER,
		request_model_name TEXT,
		channel_name TEXT,
		request_content TEXT,
		response_content TEXT,
		success numeric NOT NULL DEFAULT false
	)`).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Exec(`INSERT INTO relay_logs (id, time, request_model_name, request_content, response_content, success)
		VALUES (1, 1000, 'gpt-4o', 'req', 'resp', 1)`).Error; err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	// 迁移前：列不存在
	if hasRelayLogColumn(db, "request_path") {
		t.Fatal("request_path column should not exist before migration")
	}

	// 第一次执行
	if err := migrateRelayLogRequestPath(db); err != nil {
		t.Fatalf("migration run #1: %v", err)
	}
	if !hasRelayLogColumn(db, "request_path") {
		t.Fatal("request_path column missing after migration")
	}

	// 幂等：第二次执行不应报错
	if err := migrateRelayLogRequestPath(db); err != nil {
		t.Fatalf("migration run #2: %v", err)
	}

	// 旧数据完好 + 新列可写入
	if err := db.Exec(`UPDATE relay_logs SET request_path = 'POST /v1/messages' WHERE id = 1`).Error; err != nil {
		t.Fatalf("write request_path: %v", err)
	}
	var path string
	if err := db.Raw(`SELECT request_path FROM relay_logs WHERE id = 1`).Scan(&path).Error; err != nil {
		t.Fatalf("read request_path: %v", err)
	}
	if path != "POST /v1/messages" {
		t.Fatalf("request_path mismatch: got %q", path)
	}
	var content string
	if err := db.Raw(`SELECT request_content FROM relay_logs WHERE id = 1`).Scan(&content).Error; err != nil {
		t.Fatalf("read legacy column: %v", err)
	}
	if content != "req" {
		t.Fatalf("legacy data lost: got %q", content)
	}
}
