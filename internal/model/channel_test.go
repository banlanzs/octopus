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
