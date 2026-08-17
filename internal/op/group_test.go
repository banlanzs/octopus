package op

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/transformer/outbound"
)

func TestGroupListModelForAPIKeyFiltersByChannelsAndModels(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	channelCache.Clear()
	groupCache.Clear()
	groupMap.Clear()
	t.Cleanup(func() {
		channelCache.Clear()
		groupCache.Clear()
		groupMap.Clear()
	})

	allowedChannel := &model.Channel{
		Name:    "allowed-channel",
		Type:    outbound.OutboundTypeOpenAIChat,
		Enabled: true,
		Model:   "shared-a",
	}
	if err := ChannelCreate(allowedChannel, ctx); err != nil {
		t.Fatalf("ChannelCreate(allowed) failed: %v", err)
	}
	blockedChannel := &model.Channel{
		Name:    "blocked-channel",
		Type:    outbound.OutboundTypeOpenAIChat,
		Enabled: true,
		Model:   "shared-b",
	}
	if err := ChannelCreate(blockedChannel, ctx); err != nil {
		t.Fatalf("ChannelCreate(blocked) failed: %v", err)
	}

	groups := []*model.Group{
		{
			Name: "shared-a",
			Mode: model.GroupModeRoundRobin,
			Items: []model.GroupItem{
				{ChannelID: allowedChannel.ID, ModelName: "shared-a", Weight: 1},
			},
		},
		{
			Name: "shared-b",
			Mode: model.GroupModeRoundRobin,
			Items: []model.GroupItem{
				{ChannelID: blockedChannel.ID, ModelName: "shared-b", Weight: 1},
			},
		},
		{
			Name: "both",
			Mode: model.GroupModeRoundRobin,
			Items: []model.GroupItem{
				{ChannelID: allowedChannel.ID, ModelName: "both-a", Weight: 1},
				{ChannelID: blockedChannel.ID, ModelName: "both-b", Weight: 1},
			},
		},
	}
	for _, group := range groups {
		if err := GroupCreate(group, ctx); err != nil {
			t.Fatalf("GroupCreate(%s) failed: %v", group.Name, err)
		}
	}

	t.Run("channels filter narrows group model list", func(t *testing.T) {
		got, err := GroupListModelForAPIKey(model.APIKey{SupportedChannels: fmt.Sprintf("%d", allowedChannel.ID)}, ctx)
		if err != nil {
			t.Fatalf("GroupListModelForAPIKey failed: %v", err)
		}
		want := []string{"both", "shared-a"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("models = %v, want %v", got, want)
		}
	})

	t.Run("channels and models are intersected", func(t *testing.T) {
		key := model.APIKey{
			SupportedChannels: fmt.Sprintf("%d", allowedChannel.ID),
			SupportedModels:   "shared-a",
		}
		got, err := GroupListModelForAPIKey(key, ctx)
		if err != nil {
			t.Fatalf("GroupListModelForAPIKey failed: %v", err)
		}
		want := []string{"shared-a"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("models = %v, want %v", got, want)
		}
	})

	t.Run("unknown channel whitelist yields empty model list", func(t *testing.T) {
		got, err := GroupListModelForAPIKey(model.APIKey{SupportedChannels: "99999"}, ctx)
		if err != nil {
			t.Fatalf("GroupListModelForAPIKey failed: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("models = %v, want empty", got)
		}
	})

	t.Run("disabled channel is excluded", func(t *testing.T) {
		disabled := false
		updateReq := &model.ChannelUpdateRequest{
			ID:      allowedChannel.ID,
			Enabled: &disabled,
		}
		if _, err := ChannelUpdate(updateReq, ctx); err != nil {
			t.Fatalf("ChannelUpdate failed: %v", err)
		}
		defer func() {
			enabled := true
			_, _ = ChannelUpdate(&model.ChannelUpdateRequest{ID: allowedChannel.ID, Enabled: &enabled}, ctx)
		}()

		got, err := GroupListModelForAPIKey(model.APIKey{SupportedChannels: fmt.Sprintf("%d", allowedChannel.ID)}, ctx)
		if err != nil {
			t.Fatalf("GroupListModelForAPIKey failed: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("models = %v, want empty", got)
		}
	})
}
