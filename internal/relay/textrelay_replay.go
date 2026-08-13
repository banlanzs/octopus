package relay

import (
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/relay/balancer"
)

// textRelayReplaySticky 保存 OpenAI Responses 的 response_id → 粘性渠道映射，
// 用于 previous_response_id 续传时路由回同一渠道/key（避免续传转发到不同渠道）。
//
// 注意：这是 HTTP replay 的「粘性路由」部分；完整 replay 的「历史合并」
// （transcript 重放，使客户端无需重复发送完整历史）深度绑定自研 wsConversationState
// 与 InternalLLMRequest 的 Responses 特有字段，尚未迁移，见 MIGRATION_AUDIT.md。
var textRelayReplaySticky sync.Map // key: response_id(string), value: *balancer.SessionEntry

func storeTextRelayReplaySticky(responseID string, channelID, keyID int) {
	if responseID == "" || channelID <= 0 {
		return
	}
	textRelayReplaySticky.Store(responseID, &balancer.SessionEntry{
		ChannelID:    channelID,
		ChannelKeyID: keyID,
		Timestamp:    time.Now(),
	})
}

func loadTextRelayReplaySticky(previousResponseID string) *balancer.SessionEntry {
	if previousResponseID == "" {
		return nil
	}
	v, ok := textRelayReplaySticky.Load(previousResponseID)
	if !ok {
		return nil
	}
	entry, ok := v.(*balancer.SessionEntry)
	if !ok {
		return nil
	}
	return entry
}
