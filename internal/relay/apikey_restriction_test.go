package relay

import (
	"reflect"
	"testing"

	dbmodel "github.com/bestruirui/octopus/internal/model"
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
