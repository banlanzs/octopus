package op

import (
	"testing"

	"github.com/bestruirui/octopus/internal/db"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
)

func TestChannelImportKeysParsesContentAndDeduplicates(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	channelCache.Clear()
	channelKeyCache.Clear()
	t.Cleanup(func() {
		channelCache.Clear()
		channelKeyCache.Clear()
	})

	channel := &model.Channel{
		Name:    "import-keys-channel",
		Type:    outbound.OutboundTypeOpenAIChat,
		Enabled: true,
		Model:   "gpt-4o",
		Keys: []model.ChannelKey{
			{Enabled: true, ChannelKey: "sk-existing"},
		},
	}
	if err := ChannelCreate(channel, ctx); err != nil {
		t.Fatalf("ChannelCreate failed: %v", err)
	}

	result, err := ChannelImportKeys(&model.ChannelKeyImportRequest{
		ID:      channel.ID,
		Content: "sk-new-1\nBearer sk-new-2\nsk-new-1\nsk-existing",
	}, ctx)
	if err != nil {
		t.Fatalf("ChannelImportKeys failed: %v", err)
	}
	if result.Imported != 2 {
		t.Fatalf("Imported = %d, want 2", result.Imported)
	}
	if result.Duplicated != 2 {
		t.Fatalf("Duplicated = %d, want 2", result.Duplicated)
	}
	if result.Channel == nil {
		t.Fatal("result.Channel is nil")
	}

	got := make(map[string]model.ChannelKey)
	for _, key := range result.Channel.Keys {
		got[key.ChannelKey] = key
	}
	if _, ok := got["sk-existing"]; !ok {
		t.Fatalf("existing key missing from result: %+v", got)
	}
	if _, ok := got["sk-new-1"]; !ok {
		t.Fatalf("imported key sk-new-1 missing from result: %+v", got)
	}
	if key := got["sk-new-2"]; !key.Enabled {
		t.Fatalf("imported key sk-new-2 should default to enabled: %+v", key)
	}

	// 数据库与缓存都应有新 key。
	var dbCount int64
	if err := db.GetDB().WithContext(ctx).Model(&model.ChannelKey{}).
		Where("channel_id = ? AND channel_key IN ?", channel.ID, []string{"sk-new-1", "sk-new-2"}).
		Count(&dbCount).Error; err != nil {
		t.Fatalf("count imported keys failed: %v", err)
	}
	if dbCount != 2 {
		t.Fatalf("db imported key count = %d, want 2", dbCount)
	}
}

func TestChannelImportKeysSupportsStructuredKeysAndDisabled(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	channelCache.Clear()
	channelKeyCache.Clear()
	t.Cleanup(func() {
		channelCache.Clear()
		channelKeyCache.Clear()
	})

	channel := &model.Channel{
		Name:    "import-keys-structured",
		Type:    outbound.OutboundTypeOpenAIChat,
		Enabled: true,
		Model:   "gpt-4o",
	}
	if err := ChannelCreate(channel, ctx); err != nil {
		t.Fatalf("ChannelCreate failed: %v", err)
	}

	disabled := false
	remark := "imported"
	result, err := ChannelImportKeys(&model.ChannelKeyImportRequest{
		ID:      channel.ID,
		Keys:    []string{" sk-a ", "\"sk-b\"", "sk-a"},
		Enabled: &disabled,
		Remark:  remark,
	}, ctx)
	if err != nil {
		t.Fatalf("ChannelImportKeys failed: %v", err)
	}
	if result.Imported != 2 || result.Duplicated != 1 {
		t.Fatalf("Imported/Duplicated = %d/%d, want 2/1", result.Imported, result.Duplicated)
	}
	for _, key := range result.Channel.Keys {
		if key.Enabled {
			t.Fatalf("imported key %q should be disabled", key.ChannelKey)
		}
		if key.Remark != remark {
			t.Fatalf("imported key %q remark = %q, want %q", key.ChannelKey, key.Remark, remark)
		}
	}
}

func TestChannelImportKeysRejectsEmptyRequest(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	channelCache.Clear()
	t.Cleanup(channelCache.Clear)

	channel := &model.Channel{
		Name:    "import-keys-empty",
		Type:    outbound.OutboundTypeOpenAIChat,
		Enabled: true,
		Model:   "gpt-4o",
	}
	if err := ChannelCreate(channel, ctx); err != nil {
		t.Fatalf("ChannelCreate failed: %v", err)
	}

	if _, err := ChannelImportKeys(&model.ChannelKeyImportRequest{ID: channel.ID}, ctx); err == nil {
		t.Fatal("expected error for empty import request")
	}
}
