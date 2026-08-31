package model

import (
	"testing"
	"time"
)

func TestGetChannelKeyPrefersPreferredKeyID(t *testing.T) {
	channel := &Channel{
		Keys: []ChannelKey{
			{ID: 1, Enabled: true, ChannelKey: "first", TotalCost: 1},
			{ID: 2, Enabled: true, ChannelKey: "preferred", TotalCost: 100},
		},
	}

	selected := channel.GetChannelKey(ChannelKeySelectOptions{PreferredKeyID: 2})
	if selected.ID != 2 {
		t.Fatalf("expected preferred key 2, got %d", selected.ID)
	}
}

func TestGetChannelKeyUsesPreferredKeyAfterRecent429(t *testing.T) {
	channel := &Channel{
		Keys: []ChannelKey{
			{ID: 1, Enabled: true, ChannelKey: "fallback", TotalCost: 1},
			{ID: 2, Enabled: true, ChannelKey: "preferred", TotalCost: 100, StatusCode: 429, LastUseTimeStamp: time.Now().Unix()},
		},
	}

	selected := channel.GetChannelKey(ChannelKeySelectOptions{PreferredKeyID: 2})
	if selected.ID != 2 {
		t.Fatalf("expected preferred key 2 despite recent 429, got %d", selected.ID)
	}
}

func TestGetChannelKeyUsesLowestCostKeyAfterRecent429(t *testing.T) {
	channel := &Channel{
		Keys: []ChannelKey{
			{ID: 1, Enabled: true, ChannelKey: "recent-429", TotalCost: 1, StatusCode: 429, LastUseTimeStamp: time.Now().Unix()},
			{ID: 2, Enabled: true, ChannelKey: "other", TotalCost: 100},
		},
	}

	selected := channel.GetChannelKey()
	if selected.ID != 1 {
		t.Fatalf("expected lowest cost key 1 despite recent 429, got %d", selected.ID)
	}
}

func TestChannelModelRedirectExposureAndResolution(t *testing.T) {
	channel := &Channel{
		Model:       "gpt-4o,gpt-4o-mini",
		CustomModel: "my-original",
		ModelRedirects: []ModelRedirect{
			{Model: " fast-gpt ", TargetModel: " gpt-4o "},
			{Model: "my-alias", TargetModel: "my-original"},
			{Model: "fast-gpt", TargetModel: "dup-ignored"},
		},
	}

	t.Run("without only-alias switch both original and alias are exposed", func(t *testing.T) {
		got := channel.ExposedModelNames()
		want := []string{"gpt-4o", "gpt-4o-mini", "my-original", "fast-gpt", "my-alias"}
		if len(got) != len(want) {
			t.Fatalf("ExposedModelNames = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("ExposedModelNames = %v, want %v", got, want)
			}
		}
	})

	t.Run("only-alias switch hides original models", func(t *testing.T) {
		channel.ModelRedirectOnly = true
		defer func() { channel.ModelRedirectOnly = false }()
		got := channel.ExposedModelNames()
		want := []string{"fast-gpt", "my-alias"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("ExposedModelNames = %v, want %v", got, want)
		}
		if channel.IsModelExposed("gpt-4o") {
			t.Fatalf("original model should be hidden when ModelRedirectOnly is set")
		}
	})

	if got := channel.ResolveModelRedirect("fast-gpt"); got != "gpt-4o" {
		t.Fatalf("ResolveModelRedirect(fast-gpt) = %q, want gpt-4o", got)
	}
	if got := channel.ResolveModelRedirect("gpt-4o"); got != "gpt-4o" {
		t.Fatalf("ResolveModelRedirect(gpt-4o) = %q, want unchanged", got)
	}
}

func TestNormalizeChannelGroups(t *testing.T) {
	groups := []ChannelGroup{
		{Alias: " unified ", Mode: GroupModeWeighted, Items: []ChannelGroupItem{
			{Model: " gpt-4o ", Priority: 1, Weight: 3},
			{Model: "gpt-4o-mini", Priority: 2, Weight: 1},
			{Model: " gpt-4o ", Priority: 9, Weight: 9},
			{Model: "   ", Priority: 1, Weight: 1},
		}},
		{Alias: "unified", Mode: GroupModeFailover, Items: []ChannelGroupItem{{Model: "dup", Weight: 1}}},
		{Alias: "  ", Mode: GroupModeFailover},
	}

	got := NormalizeChannelGroups(groups)
	if len(got) != 1 {
		t.Fatalf("NormalizeChannelGroups = %v, want 1 group", got)
	}
	group := got[0]
	if group.Alias != "unified" {
		t.Fatalf("alias = %q, want unified", group.Alias)
	}
	if len(group.Items) != 2 {
		t.Fatalf("items = %v, want 2 items (model dedup, empty dropped)", group.Items)
	}
	if group.Items[0].Model != "gpt-4o" || group.Items[0].Priority != 1 || group.Items[0].Weight != 3 {
		t.Fatalf("first item = %+v, want gpt-4o priority=1 weight=3", group.Items[0])
	}
	if group.Items[1].Model != "gpt-4o-mini" {
		t.Fatalf("second item = %+v, want gpt-4o-mini", group.Items[1])
	}

	if got := NormalizeChannelGroups(nil); got != nil {
		t.Fatalf("NormalizeChannelGroups(nil) = %v, want nil", got)
	}
	if got := NormalizeChannelGroups([]ChannelGroup{}); got != nil {
		t.Fatalf("NormalizeChannelGroups(empty) = %v, want nil", got)
	}
}

func TestValidateChannelGroupModes(t *testing.T) {
	if err := ValidateChannelGroupModes([]ChannelGroup{
		{Alias: "a", Mode: GroupModeFailover},
		{Alias: "b", Mode: GroupModeWeighted},
	}); err != nil {
		t.Fatalf("valid modes rejected: %v", err)
	}
	for _, mode := range []GroupMode{GroupModeRoundRobin, GroupModeRandom, GroupModeAuto, 0} {
		if err := ValidateChannelGroupModes([]ChannelGroup{{Alias: "bad", Mode: mode}}); err == nil {
			t.Fatalf("mode %d should be rejected", mode)
		}
	}
}

func TestChannelGroupExposureAndLookup(t *testing.T) {
	channel := &Channel{
		Model: "gpt-4o",
		ChannelGroups: []ChannelGroup{
			{Alias: "unified", Mode: GroupModeWeighted, Items: []ChannelGroupItem{
				{Model: "gpt-4o", Weight: 3},
				{Model: "gpt-4o-mini", Weight: 1},
			}},
		},
	}

	t.Run("alias exposed alongside originals", func(t *testing.T) {
		got := channel.ExposedModelNames()
		want := []string{"gpt-4o", "unified"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("ExposedModelNames = %v, want %v", got, want)
		}
	})

	t.Run("alias stays exposed when only-alias switch is on", func(t *testing.T) {
		channel.ModelRedirectOnly = true
		defer func() { channel.ModelRedirectOnly = false }()
		got := channel.ExposedModelNames()
		want := []string{"unified"}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("ExposedModelNames = %v, want %v", got, want)
		}
		if channel.IsModelExposed("gpt-4o") {
			t.Fatalf("original model should be hidden when ModelRedirectOnly is set")
		}
		if !channel.IsModelExposed("unified") {
			t.Fatalf("channel group alias should stay exposed when ModelRedirectOnly is set")
		}
	})

	group, ok := channel.ChannelGroupForAlias("unified")
	if !ok {
		t.Fatalf("ChannelGroupForAlias(unified) not found")
	}
	if group.Mode != GroupModeWeighted || len(group.Items) != 2 {
		t.Fatalf("group = %+v, want weighted group with 2 items", group)
	}
	if _, ok := channel.ChannelGroupForAlias("missing"); ok {
		t.Fatalf("ChannelGroupForAlias(missing) should not be found")
	}
}
