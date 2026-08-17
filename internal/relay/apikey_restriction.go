package relay

import (
	"context"
	"sort"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/xstrings"
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

// fallbackGroupForAPIKeyChannels 为限渠道的 API Key 构造临时分组：
// 当请求模型没有对应分组、或已有分组没有白名单渠道条目时，
// 直接使用白名单渠道 Model/CustomModel 中匹配的模型路由，
// 保证 /v1/models 返回的渠道模型可被实际调用。
func fallbackGroupForAPIKeyChannels(requestModel, supportedChannels string, ctx context.Context) (dbmodel.Group, bool) {
	allowed, restricted := parseSupportedChannelIDs(supportedChannels)
	if !restricted {
		return dbmodel.Group{}, false
	}

	channels, err := op.ChannelList(ctx)
	if err != nil {
		return dbmodel.Group{}, false
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i].ID < channels[j].ID })

	group := dbmodel.Group{Mode: dbmodel.GroupModeFailover}
	for _, channel := range channels {
		if !channel.Enabled {
			continue
		}
		if _, ok := allowed[channel.ID]; !ok {
			continue
		}
		for _, modelName := range xstrings.SplitTrimCompact(",", channel.Model, channel.CustomModel) {
			if modelName != requestModel {
				continue
			}
			group.Items = append(group.Items, dbmodel.GroupItem{
				ChannelID: channel.ID,
				ModelName: modelName,
				Priority:  len(group.Items) + 1,
				Weight:    1,
			})
			break
		}
	}
	return group, len(group.Items) > 0
}

// groupForAPIKeyRequest 解析请求模型对应的可用分组：
// 优先使用已有分组并应用渠道白名单；分组不存在或无白名单条目时，
// 再回退到渠道配置的模型路由。
func groupForAPIKeyRequest(requestModel, supportedChannels string, ctx context.Context) (dbmodel.Group, error) {
	group, err := op.GroupGetEnabledMap(requestModel, ctx)
	if err == nil {
		group = restrictGroupChannels(group, supportedChannels)
		if len(group.Items) > 0 {
			return group, nil
		}
	}
	if fallback, ok := fallbackGroupForAPIKeyChannels(requestModel, supportedChannels, ctx); ok {
		return fallback, nil
	}
	if err != nil {
		return dbmodel.Group{}, err
	}
	return group, nil
}
