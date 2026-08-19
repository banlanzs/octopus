package relay

import (
	"context"
	"sort"
	"strings"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/gin-gonic/gin"
)

// parseSupportedChannelIDs 将 API Key 的 supported_channels 字段解析为渠道 ID 白名单。
// 返回的 restricted 为 false 表示未配置白名单（允许所有渠道）；
// restricted 为 true 时即使 allowed 为空也保持白名单语义（无渠道可用），
// 避免无效配置被误判为“不限渠道”。
func parseSupportedChannelIDs(raw string) (allowed map[int]struct{}, restricted bool) {
	return dbmodel.APIKey{SupportedChannels: raw}.SupportedChannelIDSet()
}

// parseSupportedTags 将 API Key 的标签白名单标准化。
// restricted 为 false 表示未配置标签；为 true 时即使没有任何渠道命中
// 也保持白名单语义，避免回退到不受限路由。
func parseSupportedTags(tags []string) (map[string]struct{}, bool) {
	normalized := dbmodel.NormalizeAPIKeyTags(tags)
	if len(normalized) == 0 {
		return nil, false
	}
	allowed := make(map[string]struct{}, len(normalized))
	for _, tag := range normalized {
		allowed[tag] = struct{}{}
	}
	return allowed, true
}

// channelIDsForSupportedTags 将标签白名单解析为可用渠道 ID，并与渠道
// ID 白名单（若配置）取交集。
func channelIDsForSupportedTags(supportedTags []string, supportedChannels string) (map[int]struct{}, bool) {
	tagSet, tagRestricted := parseSupportedTags(supportedTags)
	if !tagRestricted {
		return nil, false
	}
	tags := make([]string, 0, len(tagSet))
	for tag := range tagSet {
		tags = append(tags, tag)
	}
	allowed := op.ChannelIDsForTags(tags)
	channelIDs, channelRestricted := parseSupportedChannelIDs(supportedChannels)
	if channelRestricted {
		intersection := make(map[int]struct{}, len(allowed))
		for id := range allowed {
			if _, ok := channelIDs[id]; ok {
				intersection[id] = struct{}{}
			}
		}
		return intersection, true
	}
	return allowed, true
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

// restrictGroupChannelsForRouting 在渠道白名单过滤后，再应用渠道级
// "仅暴露别名"策略：ModelRedirectOnly 开启的渠道，其原始模型对应的
// 分组条目不允许被 API Key 调用（与 /v1/models 暴露口径一致）。
func restrictGroupChannelsForRouting(group dbmodel.Group, supportedChannels string, ctx context.Context) dbmodel.Group {
	group = restrictGroupChannels(group, supportedChannels)
	filtered := make([]dbmodel.GroupItem, 0, len(group.Items))
	for _, item := range group.Items {
		channel, err := op.ChannelGet(item.ChannelID, ctx)
		if err == nil && channel.ModelRedirectOnly && !channel.IsModelExposed(item.ModelName) {
			continue
		}
		filtered = append(filtered, item)
	}
	group.Items = filtered
	return group
}

// fallbackGroupForAPIKeyChannels 为限渠道的 API Key 构造临时分组：
// 当请求模型没有对应分组、或已有分组没有白名单渠道条目时，
// 直接使用白名单渠道暴露模型（含重定向别名）中匹配的模型路由，
// 保证 /v1/models 返回的渠道模型可被实际调用。
func fallbackGroupForAPIKeyChannels(requestModel, supportedChannels string, ctx context.Context) (dbmodel.Group, bool) {
	allowed, restricted := parseSupportedChannelIDs(supportedChannels)
	if !restricted {
		return dbmodel.Group{}, false
	}
	return fallbackGroupForAllowedChannels(requestModel, allowed, ctx)
}

// fallbackGroupForAllowedChannels 为给定渠道 ID 集合构造直连临时分组。
// 只使用渠道自身暴露的模型（遵守 ModelRedirectOnly），不读取分组路由。
// GroupItem.ModelName 保持为客户端请求模型名，转发循环内再按渠道
// 重定向配置解析为上游实际模型，避免重定向链被重复解析。
func fallbackGroupForAllowedChannels(requestModel string, allowed map[int]struct{}, ctx context.Context) (dbmodel.Group, bool) {
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
		if !channel.IsModelExposed(requestModel) {
			continue
		}
		group.Items = append(group.Items, dbmodel.GroupItem{
			ChannelID: channel.ID,
			ModelName: requestModel,
			Priority:  len(group.Items) + 1,
			Weight:    1,
		})
	}
	return group, len(group.Items) > 0
}

// groupForAPIKeyRequest 解析请求模型对应的可用分组（不含标签限制）：
// 优先使用已有分组并应用渠道白名单；分组不存在或无白名单条目时，
// 再回退到渠道配置的模型路由。
// direct 为 true 表示使用渠道配置构造的临时路由，此时转发应优先按
// 客户端请求格式选择上游协议，而不是渠道声明的固定类型。
func groupForAPIKeyRequest(requestModel, supportedChannels string, ctx context.Context) (dbmodel.Group, bool, error) {
	group, err := op.GroupGetEnabledMap(requestModel, ctx)
	if err == nil {
		group = restrictGroupChannelsForRouting(group, supportedChannels, ctx)
		if len(group.Items) > 0 {
			return group, false, nil
		}
	}
	if fallback, ok := fallbackGroupForAPIKeyChannels(requestModel, supportedChannels, ctx); ok {
		return fallback, true, nil
	}
	if err != nil {
		return dbmodel.Group{}, false, err
	}
	return group, false, nil
}

// groupForAPIKeyRequestWithRestrictions 解析请求模型对应的可用分组，并
// 同时应用 API Key 的渠道白名单与标签白名单。
// 标签受限时完全绕开已有分组（不把"分组"里的同名模型纳入请求），
// 只用标签渠道自身暴露的模型构造直连路由。

// supportedTagsFromContext 从 Gin 上下文读取 API Key 标签白名单。
func supportedTagsFromContext(c *gin.Context) []string {
	if c == nil {
		return nil
	}
	if value, ok := c.Get("supported_tags"); ok {
		switch tags := value.(type) {
		case []string:
			return dbmodel.NormalizeAPIKeyTags(tags)
		case string:
			return dbmodel.NormalizeAPIKeyTags(strings.Split(tags, ","))
		}
	}
	return nil
}

func groupForAPIKeyRequestWithRestrictions(requestModel, supportedChannels string, supportedTags []string, ctx context.Context) (dbmodel.Group, bool, error) {
	allowed, tagRestricted := channelIDsForSupportedTags(supportedTags, supportedChannels)
	if !tagRestricted {
		return groupForAPIKeyRequest(requestModel, supportedChannels, ctx)
	}
	if fallback, ok := fallbackGroupForAllowedChannels(requestModel, allowed, ctx); ok {
		return fallback, true, nil
	}
	return dbmodel.Group{Mode: dbmodel.GroupModeFailover}, false, nil
}
