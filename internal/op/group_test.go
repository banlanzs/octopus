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

	t.Run("channel model config is used without group items", func(t *testing.T) {
		configOnly := &model.Channel{
			Name:    "config-only-channel",
			Type:    outbound.OutboundTypeOpenAIChat,
			Enabled: true,
			Model:   "config-only-a,config-only-b",
		}
		if err := ChannelCreate(configOnly, ctx); err != nil {
			t.Fatalf("ChannelCreate(config-only) failed: %v", err)
		}
		got, err := GroupListModelForAPIKey(model.APIKey{SupportedChannels: fmt.Sprintf("%d", configOnly.ID)}, ctx)
		if err != nil {
			t.Fatalf("GroupListModelForAPIKey failed: %v", err)
		}
		want := []string{"config-only-a", "config-only-b"}
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

func TestGroupListModelForAPIKeyTagsUseChannelModelsOnly(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	channelCache.Clear()
	groupCache.Clear()
	groupMap.Clear()
	t.Cleanup(func() {
		channelCache.Clear()
		groupCache.Clear()
		groupMap.Clear()
	})

	tagged := &model.Channel{
		Name:              "tagged-channel",
		Type:              outbound.OutboundTypeOpenAIChat,
		Enabled:           true,
		Model:             "gpt-4o",
		Tags:              []string{"prod", "cheap"},
		ModelRedirects:    []model.ModelRedirect{{Model: "fast-gpt", TargetModel: "gpt-4o"}},
		ModelRedirectOnly: true,
	}
	if err := ChannelCreate(tagged, ctx); err != nil {
		t.Fatalf("ChannelCreate(tagged) failed: %v", err)
	}
	untagged := &model.Channel{
		Name:    "untagged-channel",
		Type:    outbound.OutboundTypeOpenAIChat,
		Enabled: true,
		Model:   "other-model",
	}
	if err := ChannelCreate(untagged, ctx); err != nil {
		t.Fatalf("ChannelCreate(untagged) failed: %v", err)
	}

	// 分组里存在同名/别名模型：标签受限时不应纳入 /v1/models 或路由。
	group := &model.Group{
		Name: "group-alias",
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ChannelID: tagged.ID, ModelName: "gpt-4o", Weight: 1},
		},
	}
	if err := GroupCreate(group, ctx); err != nil {
		t.Fatalf("GroupCreate failed: %v", err)
	}

	got, err := GroupListModelForAPIKey(model.APIKey{SupportedTags: []string{"prod"}}, ctx)
	if err != nil {
		t.Fatalf("GroupListModelForAPIKey failed: %v", err)
	}
	want := []string{"fast-gpt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %v, want %v", got, want)
	}

	// 标签 + 模型白名单取交集。
	got, err = GroupListModelForAPIKey(model.APIKey{SupportedTags: []string{"prod"}, SupportedModels: "gpt-4o"}, ctx)
	if err != nil {
		t.Fatalf("GroupListModelForAPIKey failed: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("models = %v, want empty", got)
	}

	// 标签与渠道 ID 白名单取交集：无交集时为空。
	got, err = GroupListModelForAPIKey(model.APIKey{
		SupportedTags:     []string{"prod"},
		SupportedChannels: fmt.Sprintf("%d", untagged.ID),
	}, ctx)
	if err != nil {
		t.Fatalf("GroupListModelForAPIKey failed: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("models = %v, want empty", got)
	}
}

func TestGroupListModelHidesRedirectOnlyOriginalGroup(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	channelCache.Clear()
	groupCache.Clear()
	groupMap.Clear()
	t.Cleanup(func() {
		channelCache.Clear()
		groupCache.Clear()
		groupMap.Clear()
	})

	redirectOnly := &model.Channel{
		Name:              "redirect-only-channel",
		Type:              outbound.OutboundTypeOpenAIChat,
		Enabled:           true,
		Model:             "gpt-4o",
		ModelRedirects:    []model.ModelRedirect{{Model: "fast-gpt", TargetModel: "gpt-4o"}},
		ModelRedirectOnly: true,
	}
	if err := ChannelCreate(redirectOnly, ctx); err != nil {
		t.Fatalf("ChannelCreate failed: %v", err)
	}

	if err := GroupCreate(&model.Group{
		Name: "gpt-4o",
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ChannelID: redirectOnly.ID, ModelName: "gpt-4o", Weight: 1},
		},
	}, ctx); err != nil {
		t.Fatalf("GroupCreate failed: %v", err)
	}
	if err := GroupCreate(&model.Group{
		Name: "fast-gpt",
		Mode: model.GroupModeRoundRobin,
		Items: []model.GroupItem{
			{ChannelID: redirectOnly.ID, ModelName: "fast-gpt", Weight: 1},
		},
	}, ctx); err != nil {
		t.Fatalf("GroupCreate(alias) failed: %v", err)
	}

	got, err := GroupListModel(ctx)
	if err != nil {
		t.Fatalf("GroupListModel failed: %v", err)
	}
	want := []string{"fast-gpt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GroupListModel = %v, want %v", got, want)
	}
}
