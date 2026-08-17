package relay

import (
	dbmodel "github.com/bestruirui/octopus/internal/model"
)

// parseSupportedChannelIDs 将 API Key 的 supported_channels 字段解析为渠道 ID 白名单。
// 返回的 restricted 为 false 表示未配置白名单（允许所有渠道）；
// restricted 为 true 时即使 allowed 为空也保持白名单语义（无渠道可用），
// 避免无效配置被误判为“不限渠道”。
func parseSupportedChannelIDs(raw string) (allowed map[int]struct{}, restricted bool) {
	return dbmodel.APIKey{SupportedChannels: raw}.SupportedChannelIDSet()
}

// restrictGroupChannels 按 API Key 的渠道白名单过滤分组条目。
// 未配置白名单时原样返回；配置后仅保留白名单中的渠道。
func restrictGroupChannels(group dbmodel.Group, supportedChannels string) dbmodel.Group {
	allowed, restricted := parseSupportedChannelIDs(supportedChannels)
	if !restricted {
		return group
	}
	filtered := make([]dbmodel.GroupItem, 0, len(group.Items))
	for _, item := range group.Items {
		if _, ok := allowed[item.ChannelID]; ok {
			filtered = append(filtered, item)
		}
	}
	group.Items = filtered
	return group
}
