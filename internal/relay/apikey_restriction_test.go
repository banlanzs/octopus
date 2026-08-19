package relay

import (
	"reflect"
	"testing"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
)

func TestParseSupportedChannelIDs(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		allowed    map[int]struct{}
		restricted bool
	}{
		{name: "empty means unrestricted", raw: "", allowed: nil, restricted: false},
		{name: "whitespace-only whitelist stays restricted", raw: "  , ", allowed: map[int]struct{}{}, restricted: true},
		{name: "parses ids and trims whitespace", raw: " 1, 3 ,7,", allowed: map[int]struct{}{1: {}, 3: {}, 7: {}}, restricted: true},
		{name: "invalid entries are ignored but still restricted", raw: "1,abc,-2,0", allowed: map[int]struct{}{1: {}}, restricted: true},
		{name: "duplicates collapse", raw: "2,2, 2", allowed: map[int]struct{}{2: {}}, restricted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed, restricted := parseSupportedChannelIDs(tt.raw)
			if restricted != tt.restricted {
				t.Fatalf("restricted = %v, want %v", restricted, tt.restricted)
			}
			if tt.allowed == nil {
				if allowed != nil {
					t.Fatalf("allowed = %v, want nil", allowed)
				}
				return
			}
			if !reflect.DeepEqual(allowed, tt.allowed) {
				t.Fatalf("allowed = %v, want %v", allowed, tt.allowed)
			}
		})
	}
}

func TestRestrictGroupChannels(t *testing.T) {
	group := dbmodel.Group{
		ID: 1,
		Items: []dbmodel.GroupItem{
			{ChannelID: 1, ModelName: "a"},
			{ChannelID: 2, ModelName: "b"},
			{ChannelID: 3, ModelName: "c"},
		},
	}

	t.Run("unrestricted keeps all items", func(t *testing.T) {
		got := restrictGroupChannels(group, "")
		if !reflect.DeepEqual(got.Items, group.Items) {
			t.Fatalf("items = %v, want %v", got.Items, group.Items)
		}
	})

	t.Run("whitelist keeps only matching channels", func(t *testing.T) {
		got := restrictGroupChannels(group, "1,3")
		want := []dbmodel.GroupItem{
			{ChannelID: 1, ModelName: "a"},
			{ChannelID: 3, ModelName: "c"},
		}
		if !reflect.DeepEqual(got.Items, want) {
			t.Fatalf("items = %v, want %v", got.Items, want)
		}
	})

	t.Run("no matching channel yields empty candidate list", func(t *testing.T) {
		got := restrictGroupChannels(group, "99")
		if len(got.Items) != 0 {
			t.Fatalf("items = %v, want empty", got.Items)
		}
	})

	t.Run("invalid whitelist does not fall back to unrestricted", func(t *testing.T) {
		got := restrictGroupChannels(group, "abc")
		if len(got.Items) != 0 {
			t.Fatalf("items = %v, want empty", got.Items)
		}
	})
}

func TestGroupForAPIKeyRequestWithTagsBypassesGroupsAndAppliesRedirect(t *testing.T) {
	ctx := setupRelayTestDB(t)

	tagged := &dbmodel.Channel{
		Name:              "tagged-redirect-channel",
		Type:              0,
		Enabled:           true,
		Model:             "gpt-4o",
		Tags:              []string{"prod"},
		ModelRedirects:    []dbmodel.ModelRedirect{{Model: "fast-gpt", TargetModel: "gpt-4o"}},
		ModelRedirectOnly: true,
	}
	if err := op.ChannelCreate(tagged, ctx); err != nil {
		t.Fatalf("ChannelCreate(tagged) failed: %v", err)
	}
	untagged := &dbmodel.Channel{
		Name:    "untagged-direct-channel",
		Type:    0,
		Enabled: true,
		Model:   "gpt-4o",
	}
	if err := op.ChannelCreate(untagged, ctx); err != nil {
		t.Fatalf("ChannelCreate(untagged) failed: %v", err)
	}

	// 同名分组必须被标签路由忽略，不能把分组里的渠道纳入请求。
	if err := op.GroupCreate(&dbmodel.Group{
		Name: "fast-gpt",
		Mode: dbmodel.GroupModeRoundRobin,
		Items: []dbmodel.GroupItem{
			{ChannelID: untagged.ID, ModelName: "gpt-4o", Weight: 1},
		},
	}, ctx); err != nil {
		t.Fatalf("GroupCreate failed: %v", err)
	}

	group, direct, err := groupForAPIKeyRequestWithRestrictions("fast-gpt", "", []string{"prod"}, ctx)
	if err != nil {
		t.Fatalf("groupForAPIKeyRequestWithRestrictions failed: %v", err)
	}
	if !direct {
		t.Fatalf("tag-restricted route should be direct, got group route")
	}
	if len(group.Items) != 1 {
		t.Fatalf("items = %v, want one tagged channel", group.Items)
	}
	if group.Items[0].ChannelID != tagged.ID || group.Items[0].ModelName != "fast-gpt" {
		t.Fatalf("item = %+v, want channel=%d model=fast-gpt", group.Items[0], tagged.ID)
	}

	// 仅暴露别名时，原始模型不可通过标签渠道直连。
	group, _, err = groupForAPIKeyRequestWithRestrictions("gpt-4o", "", []string{"prod"}, ctx)
	if err != nil {
		t.Fatalf("groupForAPIKeyRequestWithRestrictions failed: %v", err)
	}
	if len(group.Items) != 0 {
		t.Fatalf("original model should be hidden for redirect-only channel, items = %v", group.Items)
	}
}

func TestGroupForAPIKeyRequestFiltersRedirectOnlyOriginalGroupItems(t *testing.T) {
	ctx := setupRelayTestDB(t)

	channel := &dbmodel.Channel{
		Name:              "redirect-group-channel",
		Type:              0,
		Enabled:           true,
		Model:             "gpt-4o",
		ModelRedirects:    []dbmodel.ModelRedirect{{Model: "fast-gpt", TargetModel: "gpt-4o"}},
		ModelRedirectOnly: true,
	}
	if err := op.ChannelCreate(channel, ctx); err != nil {
		t.Fatalf("ChannelCreate failed: %v", err)
	}

	if err := op.GroupCreate(&dbmodel.Group{
		Name: "fast-gpt",
		Mode: dbmodel.GroupModeRoundRobin,
		Items: []dbmodel.GroupItem{
			{ChannelID: channel.ID, ModelName: "fast-gpt", Weight: 1},
		},
	}, ctx); err != nil {
		t.Fatalf("GroupCreate(alias) failed: %v", err)
	}
	if err := op.GroupCreate(&dbmodel.Group{
		Name: "gpt-4o",
		Mode: dbmodel.GroupModeRoundRobin,
		Items: []dbmodel.GroupItem{
			{ChannelID: channel.ID, ModelName: "gpt-4o", Weight: 1},
		},
	}, ctx); err != nil {
		t.Fatalf("GroupCreate(original) failed: %v", err)
	}

	aliasGroup, _, err := groupForAPIKeyRequest("fast-gpt", "", ctx)
	if err != nil {
		t.Fatalf("groupForAPIKeyRequest(alias) failed: %v", err)
	}
	if len(aliasGroup.Items) != 1 || aliasGroup.Items[0].ModelName != "fast-gpt" {
		t.Fatalf("alias group items = %v, want alias item", aliasGroup.Items)
	}

	originalGroup, _, err := groupForAPIKeyRequest("gpt-4o", "", ctx)
	if err != nil {
		t.Fatalf("groupForAPIKeyRequest(original) failed: %v", err)
	}
	if len(originalGroup.Items) != 0 {
		t.Fatalf("original group items = %v, want empty", originalGroup.Items)
	}
}
