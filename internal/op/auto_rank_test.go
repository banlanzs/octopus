package op

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func TestAutoRankSnapshotReplaceAllRemovesStaleRows(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	now := time.Now()
	initial := []model.AutoRankSnapshot{
		{GroupID: 1, ChannelID: 10, ModelName: "m1", Samples: 4, LastSeenAt: now},
		{GroupID: 1, ChannelID: 11, ModelName: "m2", Samples: 5, LastSeenAt: now},
	}
	if err := AutoRankSnapshotReplaceAll(ctx, initial); err != nil {
		t.Fatalf("initial replace failed: %v", err)
	}

	replacement := []model.AutoRankSnapshot{
		{GroupID: 1, ChannelID: 11, ModelName: "m2", Samples: 6, EWMATTFBMS: 250, LastSeenAt: now},
	}
	if err := AutoRankSnapshotReplaceAll(ctx, replacement); err != nil {
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

func TestAutoRankSnapshotReplaceAllAcceptsEmptySet(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	if err := AutoRankSnapshotReplaceAll(ctx, []model.AutoRankSnapshot{{GroupID: 1, ChannelID: 10, ModelName: "m1", Samples: 1}}); err != nil {
		t.Fatalf("seed replace failed: %v", err)
	}
	if err := AutoRankSnapshotReplaceAll(ctx, nil); err != nil {
		t.Fatalf("empty replace failed: %v", err)
	}
	got, err := AutoRankSnapshotListAll(ctx)
	if err != nil {
		t.Fatalf("list snapshots failed: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected snapshots to be cleared, got %+v", got)
	}
}
