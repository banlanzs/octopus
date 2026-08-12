package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/gin-gonic/gin"
)

func setupGroupExportTestDB(t *testing.T) context.Context {
	t.Helper()
	if db.GetDB() != nil {
		_ = db.Close()
	}
	dbPath := filepath.Join(t.TempDir(), "octopus-group-export-test.db")
	if err := db.InitDB("sqlite", dbPath, false); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	if err := op.InitCache(); err != nil {
		t.Fatalf("InitCache failed: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return context.Background()
}

// TestExportGroupHandler 验证分组导出接口：调度策略 + 条目（模型/渠道/健康度）。
// key 必须脱敏；渠道信息、熔断状态、POR 字段齐备。
func TestExportGroupHandler(t *testing.T) {
	ctx := setupGroupExportTestDB(t)

	ch := &model.Channel{
		Name:    "export-test-channel",
		Type:    outbound.OutboundTypeAnthropic,
		Enabled: true,
		BaseUrls: []model.BaseUrl{{URL: "https://api.example.com/anthropic"}},
		Keys: []model.ChannelKey{
			{Enabled: true, ChannelKey: "sk-test-1234567890abcd", Remark: "primary"},
			{Enabled: false, ChannelKey: "sk-test-zyxwvutsrqponmlk", Remark: "backup"},
		},
		Model: "deepseek-v4-pro",
	}
	if err := op.ChannelCreate(ch, ctx); err != nil {
		t.Fatalf("ChannelCreate failed: %v", err)
	}

	group := &model.Group{
		Name:              "export-test-group",
		Mode:              model.GroupModeFailover,
		FirstTokenTimeOut: 20,
		MatchRegex:        "deepseek-",
		Items: []model.GroupItem{
			{ChannelID: ch.ID, ModelName: "deepseek-v4-pro", Priority: 2, Weight: 1},
			{ChannelID: ch.ID, ModelName: "deepseek-v4-flash", Priority: 1, Weight: 1},
		},
	}
	if err := op.GroupCreate(group, ctx); err != nil {
		t.Fatalf("GroupCreate failed: %v", err)
	}

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/group/export/"+strconv.Itoa(group.ID), nil)
	c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(group.ID)}}
	exportGroup(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var wrapper struct {
		Data groupExportResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wrapper); err != nil {
		t.Fatalf("响应 JSON 损坏: %v\n%s", err, rec.Body.String())
	}
	exported := wrapper.Data

	// 分组调度策略
	if exported.Group.Name != "export-test-group" {
		t.Fatalf("group.name 错误: %v", exported.Group.Name)
	}
	if exported.Group.ModeLabel != "failover" {
		t.Fatalf("mode_label 错误: %v", exported.Group.ModeLabel)
	}
	if exported.Group.FirstTokenTimeOut != 20 {
		t.Fatalf("first_token_time_out 错误: %v", exported.Group.FirstTokenTimeOut)
	}
	if exported.ExportedAt == "" {
		t.Fatalf("exported_at 缺失")
	}

	// 条目按 priority 排序（flash 在前）
	if len(exported.Items) != 2 {
		t.Fatalf("items 数量错误: %d", len(exported.Items))
	}
	if exported.Items[0].ModelName != "deepseek-v4-flash" || exported.Items[0].Priority != 1 {
		t.Fatalf("条目未按 priority 排序: %+v", exported.Items[0])
	}

	// 渠道信息 + key 脱敏
	item := exported.Items[0]
	if item.Channel == nil {
		t.Fatalf("channel 缺失: %+v", item)
	}
	if item.Channel.TypeLabel != "anthropic" {
		t.Fatalf("type_label 错误: %v", item.Channel.TypeLabel)
	}
	if item.Channel.Name != "export-test-channel" || !item.Channel.Enabled {
		t.Fatalf("渠道信息错误: %+v", item.Channel)
	}
	if len(item.Channel.Keys) != 2 {
		t.Fatalf("keys 数量错误: %d", len(item.Channel.Keys))
	}
	if item.Channel.Keys[0].KeyMasked != "sk-****abcd" {
		t.Fatalf("key 未脱敏: %q", item.Channel.Keys[0].KeyMasked)
	}
	if item.Channel.Keys[1].KeyMasked != "sk-****nmlk" {
		t.Fatalf("backup key 未脱敏: %q", item.Channel.Keys[1].KeyMasked)
	}
	// 明文 key 不得出现在导出中
	for _, k := range item.Channel.Keys {
		if k.KeyMasked == "sk-test-1234567890abcd" || k.KeyMasked == "sk-test-zyxwvutsrqponmlk" {
			t.Fatalf("明文 key 泄漏: %q", k.KeyMasked)
		}
	}
	// 熔断状态字段存在（默认 Closed：tripped=false）
	if item.Channel.Keys[0].Tripped {
		t.Fatalf("新渠道不应熔断: %+v", item.Channel.Keys[0])
	}

	// 健康度摘要存在
	if item.Health.Samples != 0 {
		t.Fatalf("health.samples 应初始为 0: %+v", item.Health)
	}
	if item.Health.ChannelTripped {
		t.Fatalf("health.channel_tripped 应初始为 false: %+v", item.Health)
	}

	// POR 字段：无退役记录时省略
	if item.Channel.PORRetired != nil {
		t.Fatalf("por_retired 应省略: %+v", item.Channel.PORRetired)
	}
}
