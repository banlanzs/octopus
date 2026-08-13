package op

import (
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
)

func TestChannelLLMListReportsAnthropicTypeForUnmanagedChannel(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	channelCache.Clear()
	t.Cleanup(channelCache.Clear)

	channel := &model.Channel{
		Name:        "direct-anthropic-channel",
		Type:        outbound.OutboundTypeAnthropic,
		Enabled:     true,
		Model:       "deepseek-v4-flash",
		CustomModel: "",
	}
	if err := ChannelCreate(channel, ctx); err != nil {
		t.Fatalf("ChannelCreate failed: %v", err)
	}

	models, err := ChannelLLMList(ctx)
	if err != nil {
		t.Fatalf("ChannelLLMList failed: %v", err)
	}
	for _, item := range models {
		if item.ChannelID != channel.ID || item.Name != "deepseek-v4-flash" {
			continue
		}
		if item.EndpointType != "anthropic" {
			t.Fatalf("EndpointType = %q, want anthropic", item.EndpointType)
		}
		return
	}
	t.Fatal("created channel model not found")
}

func TestChannelUpdateCascadesRemovedModelsFromGroupItems(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	channelCache.Clear()
	groupCache.Clear()
	groupMap.Clear()
	t.Cleanup(func() {
		channelCache.Clear()
		groupCache.Clear()
		groupMap.Clear()
	})

	ch := &model.Channel{
		Name:        "cascade-channel",
		Type:        outbound.OutboundTypeOpenAIChat,
		Enabled:     true,
		Model:       "gpt-4o,gpt-4o-mini",
		CustomModel: "my-custom",
	}
	if err := ChannelCreate(ch, ctx); err != nil {
		t.Fatalf("ChannelCreate failed: %v", err)
	}

	group := &model.Group{
		Name: "cascade-group",
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ChannelID: ch.ID, ModelName: "gpt-4o", Weight: 1},
			{ChannelID: ch.ID, ModelName: "my-custom", Weight: 1},
		},
	}
	if err := GroupCreate(group, ctx); err != nil {
		t.Fatalf("GroupCreate failed: %v", err)
	}

	// 为分组创建预设快照（引用即将被删除的模型）
	if _, err := GroupPresetCreate(group.ID, "snapshot", ctx); err != nil {
		t.Fatalf("GroupPresetCreate failed: %v", err)
	}

	// 渠道删除 my-custom 模型（gpt-4o 保留）
	newModel := "gpt-4o,gpt-4o-mini"
	newCustom := ""
	updated, err := ChannelUpdate(&model.ChannelUpdateRequest{
		ID:          ch.ID,
		Model:       &newModel,
		CustomModel: &newCustom,
	}, ctx)
	if err != nil {
		t.Fatalf("ChannelUpdate failed: %v", err)
	}
	if updated.Model != newModel {
		t.Fatalf("Model = %q, want %q", updated.Model, newModel)
	}

	// 分组 items 中 my-custom 的引用应被级联删除
	refreshed, err := GroupGet(group.ID, ctx)
	if err != nil {
		t.Fatalf("GroupGet failed: %v", err)
	}
	for _, item := range refreshed.Items {
		if item.ModelName == "my-custom" {
			t.Fatalf("group item referencing removed model still exists: %+v", item)
		}
	}
	if len(refreshed.Items) != 1 || refreshed.Items[0].ModelName != "gpt-4o" {
		t.Fatalf("Items = %+v, want single gpt-4o item", refreshed.Items)
	}

	// 预设快照中的引用也应被清理
	presets, err := GroupPresetList(group.ID, ctx)
	if err != nil {
		t.Fatalf("GroupPresetList failed: %v", err)
	}
	if len(presets) != 1 {
		t.Fatalf("presets len = %d, want 1", len(presets))
	}
	for _, item := range presets[0].Items {
		if item.ModelName == "my-custom" {
			t.Fatalf("preset item referencing removed model still exists: %+v", item)
		}
	}
	if len(presets[0].Items) != 1 || presets[0].Items[0].ModelName != "gpt-4o" {
		t.Fatalf("preset Items = %+v, want single gpt-4o item", presets[0].Items)
	}
}

func TestChannelUpdateKeepsGroupItemsForUntouchedModels(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	channelCache.Clear()
	groupCache.Clear()
	groupMap.Clear()
	t.Cleanup(func() {
		channelCache.Clear()
		groupCache.Clear()
		groupMap.Clear()
	})

	ch := &model.Channel{
		Name:    "keep-channel",
		Type:    outbound.OutboundTypeOpenAIChat,
		Enabled: true,
		Model:   "gpt-4o,gpt-4o-mini",
	}
	if err := ChannelCreate(ch, ctx); err != nil {
		t.Fatalf("ChannelCreate failed: %v", err)
	}

	group := &model.Group{
		Name: "keep-group",
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ChannelID: ch.ID, ModelName: "gpt-4o", Weight: 1},
		},
	}
	if err := GroupCreate(group, ctx); err != nil {
		t.Fatalf("GroupCreate failed: %v", err)
	}

	// 只改渠道名称，不动模型列表
	newName := "keep-channel-renamed"
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: ch.ID, Name: &newName}, ctx); err != nil {
		t.Fatalf("ChannelUpdate failed: %v", err)
	}

	refreshed, err := GroupGet(group.ID, ctx)
	if err != nil {
		t.Fatalf("GroupGet failed: %v", err)
	}
	if len(refreshed.Items) != 1 || refreshed.Items[0].ModelName != "gpt-4o" {
		t.Fatalf("Items = %+v, want single gpt-4o item unchanged", refreshed.Items)
	}
}

func TestChannelUpdateCascadeDeletesRowsFromDB(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	channelCache.Clear()
	groupCache.Clear()
	groupMap.Clear()
	t.Cleanup(func() {
		channelCache.Clear()
		groupCache.Clear()
		groupMap.Clear()
	})

	ch := &model.Channel{
		Name:    "cascade-db-channel",
		Type:    outbound.OutboundTypeOpenAIChat,
		Enabled: true,
		Model:   "gpt-4o,removed-model",
	}
	if err := ChannelCreate(ch, ctx); err != nil {
		t.Fatalf("ChannelCreate failed: %v", err)
	}

	group := &model.Group{
		Name: "cascade-db-group",
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ChannelID: ch.ID, ModelName: "removed-model", Weight: 1},
		},
	}
	if err := GroupCreate(group, ctx); err != nil {
		t.Fatalf("GroupCreate failed: %v", err)
	}

	newModel := "gpt-4o"
	if _, err := ChannelUpdate(&model.ChannelUpdateRequest{ID: ch.ID, Model: &newModel}, ctx); err != nil {
		t.Fatalf("ChannelUpdate failed: %v", err)
	}

	var count int64
	if err := db.GetDB().WithContext(ctx).
		Model(&model.GroupItem{}).
		Where("channel_id = ? AND model_name = ?", ch.ID, "removed-model").
		Count(&count).Error; err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("group item rows in DB = %d, want 0", count)
	}
}
