package op

import (
	"testing"

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
