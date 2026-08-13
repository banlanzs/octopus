package op

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func TestAutoRankSnapshotSyncRemovesStaleRows(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	now := time.Now()
	initial := []model.AutoRankSnapshot{
		{GroupID: 1, ChannelID: 10, ModelName: "m1", Samples: 4, LastSeenAt: now},
		{GroupID: 1, ChannelID: 11, ModelName: "m2", Samples: 5, LastSeenAt: now},
	}
	if err := AutoRankSnapshotSync(ctx, initial); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	replacement := []model.AutoRankSnapshot{
		{GroupID: 1, ChannelID: 11, ModelName: "m2", Samples: 6, EWMATTFBMS: 250, LastSeenAt: now},
	}
	if err := AutoRankSnapshotSync(ctx, replacement); err != nil {
		t.Fatalf("replacement failed: %v", err)
	}

	got, err := AutoRankSnapshotListAll(ctx)
	if err != nil {
		t.Fatalf("list snapshots failed: %v", err)
	}
	if len(got) != 1 || got[0].ChannelID != 11 || got[0].Samples != 6 || got[0].EWMATTFBMS != 250 {
		t.Fatalf("expected only current snapshot, got %+v", got)
	}
}

func TestAutoRankSnapshotSyncAcceptsEmptySet(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := AutoRankSnapshotSync(ctx, []model.AutoRankSnapshot{{GroupID: 1, ChannelID: 10, ModelName: "m1", Samples: 1}}); err != nil {
		t.Fatalf("seed sync failed: %v", err)
	}
	if err := AutoRankSnapshotSync(ctx, nil); err != nil {
		t.Fatalf("empty sync failed: %v", err)
	}
	got, err := AutoRankSnapshotListAll(ctx)
	if err != nil {
		t.Fatalf("list snapshots failed: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected snapshots to be cleared, got %+v", got)
	}
}

// 无变化同步不产生写入：行内容不变时 UpdatedAt 不应被刷新（差异化同步的核心收益）。
func TestAutoRankSnapshotSyncNoChangeNoWrite(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	now := time.Now()
	seed := []model.AutoRankSnapshot{
		{GroupID: 2, ChannelID: 20, ModelName: "m1", Samples: 3, Failures: 1, SampleTrail: `[{"age_ms":100,"ok":true,"d":500}]`, LastSeenAt: now},
	}
	if err := AutoRankSnapshotSync(ctx, seed); err != nil {
		t.Fatalf("seed sync failed: %v", err)
	}
	before, err := AutoRankSnapshotListAll(ctx)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("expected 1 row, got %d", len(before))
	}
	beforeUpdated := before[0].UpdatedAt
	time.Sleep(10 * time.Millisecond)

	// 完全相同的内容再次同步 → 不写 UpdatedAt
	if err := AutoRankSnapshotSync(ctx, seed); err != nil {
		t.Fatalf("no-change sync failed: %v", err)
	}
	after, err := AutoRankSnapshotListAll(ctx)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("expected 1 row, got %d", len(after))
	}
	if !after[0].UpdatedAt.Equal(beforeUpdated) {
		t.Fatalf("expected no write on identical sync: before=%v after=%v", beforeUpdated, after[0].UpdatedAt)
	}
}
