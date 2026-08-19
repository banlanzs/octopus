package model

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/bestruirui/octopus/internal/utils/xstrings"
)

type AutoGroupType int

const (
	AutoGroupTypeNone  AutoGroupType = 0 //不自动分组
	AutoGroupTypeFuzzy AutoGroupType = 1 //模糊匹配
	AutoGroupTypeExact AutoGroupType = 2 //准确匹配
	AutoGroupTypeRegex AutoGroupType = 3 //正则匹配
)

func (t AutoGroupType) Valid() bool {
	switch t {
	case AutoGroupTypeNone, AutoGroupTypeFuzzy, AutoGroupTypeExact, AutoGroupTypeRegex:
		return true
	default:
		return false
	}
}

func ParseAutoGroupSettingValue(value string) (AutoGroupType, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "false":
		return AutoGroupTypeNone, true
	case "true":
		return AutoGroupTypeFuzzy, true
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return AutoGroupTypeNone, false
	}
	mode := AutoGroupType(parsed)
	return mode, mode.Valid()
}

// ModelRedirect 渠道级模型重定向：客户端请求 Model 时，实际转发给上游的模型
// 改写为 TargetModel。Model 是暴露给客户端的别名，TargetModel 是上游实际模型。
type ModelRedirect struct {
	Model       string `json:"model"`
	TargetModel string `json:"target_model"`
}

type ChannelWSMode string

const (
	ChannelWSModeInherit     ChannelWSMode = "inherit"
	ChannelWSModeOff         ChannelWSMode = "off"
	ChannelWSModePassthrough ChannelWSMode = "passthrough"
	ChannelWSModeTransform   ChannelWSMode = "transform"
)

func (m ChannelWSMode) Normalize() ChannelWSMode {
	switch m {
	case ChannelWSModeOff, ChannelWSModePassthrough, ChannelWSModeTransform:
		return m
	default:
		return ChannelWSModeInherit
	}
}

type Channel struct {
	ID          int                   `json:"id" gorm:"primaryKey"`
	Name        string                `json:"name" gorm:"unique;not null"`
	Type        outbound.OutboundType `json:"type"`
	Enabled     bool                  `json:"enabled" gorm:"default:true"`
	BaseUrls    []BaseUrl             `json:"base_urls" gorm:"serializer:json"`
	Keys        []ChannelKey          `json:"keys" gorm:"foreignKey:ChannelID"`
	Model       string                `json:"model"`
	CustomModel string                `json:"custom_model"`
	Tags        []string              `json:"tags" gorm:"serializer:json"`
	// ModelRedirects 渠道级模型重定向列表。Model 为暴露给客户端的别名，
	// TargetModel 为实际发往上游的模型名。
	ModelRedirects []ModelRedirect `json:"model_redirects" gorm:"serializer:json"`
	// ModelRedirectOnly 仅暴露重定向别名模型：开启后渠道配置的原始
	// Model/CustomModel 不再出现在 /v1/models 与渠道模型列表中。
	ModelRedirectOnly bool           `json:"model_redirect_only" gorm:"default:false"`
	ProxyMode         ProxyUsageMode `json:"proxy_mode" gorm:"type:varchar(16);not null;default:'direct'"`
	ProxyConfigID     *int           `json:"proxy_config_id"`
	Proxy             bool           `json:"-" gorm:"default:false"`
	AutoSync          bool           `json:"auto_sync" gorm:"default:false"`
	AutoGroup         AutoGroupType  `json:"auto_group" gorm:"default:0"`
	CustomHeader      []CustomHeader `json:"custom_header" gorm:"serializer:json"`
	WSMode            ChannelWSMode  `json:"ws_mode" gorm:"type:varchar(16);not null;default:'inherit'"`
	ParamOverride     *string        `json:"param_override"`
	// ForceDeepSeekThinking 强制把 Anthropic thinking 语义透传为 DeepSeek `thinking`
	// 参数并重组 content[].thinking 块回传。用于中转站 DeepSeek 模型名不含
	// "deepseek" 的渠道（否则会被当作普通 OpenAI 兼容模型剥离 thinking）。
	ForceDeepSeekThinking bool `json:"force_deep_seek_thinking" gorm:"default:false"`
	// ProbeEnabled 是否允许系统自动向该渠道发起探测请求（AutoRank 主动补样本）。
	// 默认关闭：中转站与个人站点通常拒绝无业务意图的极小请求（ping + 1 token），
	// 甚至会将其判定为异常流量，须由用户确认该渠道可接受探测后再显式开启。
	// 只约束自动探测，不影响用户手动触发的分组健康检查。
	ProbeEnabled bool `json:"probe_enabled" gorm:"default:false"`
	// SchedulingExempt 调度策略豁免：开启后该渠道不参与熔断冷却、AutoRank
	// 失败降权、被动离群退役（POR）等自动调度惩罚，仍保留人工启停控制。
	// 适用于只配置少数渠道、偶发错误可通过客户端等待/重试恢复的场景，
	// 避免正常渠道因瞬时高负载错误被自动冷却或禁用。
	SchedulingExempt bool `json:"scheduling_exempt" gorm:"default:false"`
	// PriceMultiplier 渠道计费倍率。最终单价 = 模型价格 × 倍率。
	// 默认 1；0 视为 1（兼容旧数据未设置）。
	PriceMultiplier float64               `json:"price_multiplier" gorm:"default:1;not null"`
	ChannelProxy    *string               `json:"-" gorm:"column:channel_proxy"`
	Stats           *StatsChannel         `json:"stats,omitempty" gorm:"foreignKey:ChannelID"`
	MatchRegex      *string               `json:"match_regex"`
	Managed         bool                  `json:"managed" gorm:"-"`
	ManagedSource   *ManagedChannelSource `json:"managed_source,omitempty" gorm:"-"`
}

func (c *Channel) UnmarshalJSON(data []byte) error {
	type alias Channel
	aux := struct {
		*alias
		Proxy        *bool   `json:"proxy"`
		ChannelProxy *string `json:"channel_proxy"`
	}{alias: (*alias)(c)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.Proxy != nil {
		c.Proxy = *aux.Proxy
	}
	if aux.ChannelProxy != nil {
		c.ChannelProxy = aux.ChannelProxy
	}
	return nil
}

type ManagedChannelSource struct {
	SiteID          int    `json:"site_id"`
	SiteAccountID   int    `json:"site_account_id"`
	SiteUserGroupID *int   `json:"site_user_group_id,omitempty"`
	GroupKey        string `json:"group_key"`
}

type BaseUrl struct {
	URL   string `json:"url"`
	Delay int    `json:"delay"`
}

type CustomHeader struct {
	HeaderKey   string `json:"header_key"`
	HeaderValue string `json:"header_value"`
}

type ChannelKey struct {
	ID               int     `json:"id" gorm:"primaryKey"`
	ChannelID        int     `json:"channel_id"`
	Enabled          bool    `json:"enabled" gorm:"default:true"`
	ChannelKey       string  `json:"channel_key"`
	StatusCode       int     `json:"status_code"`
	LastUseTimeStamp int64   `json:"last_use_time_stamp"`
	TotalCost        float64 `json:"total_cost"`
	Remark           string  `json:"remark"`
}

type ChannelKeySelectOptions struct {
	ExcludeKeyIDs  map[int]struct{}
	PreferredKeyID int
}

// ChannelUpdateRequest 渠道更新请求 - 仅包含变更的数据
type ChannelUpdateRequest struct {
	ID          int                    `json:"id" binding:"required"`
	Name        *string                `json:"name,omitempty"`
	Type        *outbound.OutboundType `json:"type,omitempty"`
	Enabled     *bool                  `json:"enabled,omitempty"`
	BaseUrls    *[]BaseUrl             `json:"base_urls,omitempty"`
	Model       *string                `json:"model,omitempty"`
	CustomModel *string                `json:"custom_model,omitempty"`
	Tags        *[]string              `json:"tags,omitempty"`
	// ModelRedirects 为空数组表示清除重定向（nil 表示不修改）。
	ModelRedirects *[]ModelRedirect `json:"model_redirects,omitempty"`
	// ModelRedirectOnly 仅暴露重定向别名模型。
	ModelRedirectOnly *bool           `json:"model_redirect_only,omitempty"`
	ProxyMode         *ProxyUsageMode `json:"proxy_mode,omitempty"`
	ProxyConfigID     *int            `json:"proxy_config_id,omitempty"`
	Proxy             *bool           `json:"-"`
	AutoSync          *bool           `json:"auto_sync,omitempty"`
	AutoGroup         *AutoGroupType  `json:"auto_group,omitempty"`
	CustomHeader      *[]CustomHeader `json:"custom_header,omitempty"`
	WSMode            *ChannelWSMode  `json:"ws_mode,omitempty"`
	ChannelProxy      *string         `json:"-"`
	ParamOverride     *string         `json:"param_override,omitempty"`
	MatchRegex        *string         `json:"match_regex,omitempty"`
	// ForceDeepSeekThinking 强制启用 DeepSeek `thinking` 参数透传（中转站 DeepSeek 别名渠道）。
	ForceDeepSeekThinking *bool `json:"force_deep_seek_thinking,omitempty"`
	// ProbeEnabled 是否允许系统自动向该渠道发起探测请求（AutoRank 主动补样本）。
	ProbeEnabled *bool `json:"probe_enabled,omitempty"`
	// SchedulingExempt 调度策略豁免：不参与熔断/AutoRank/POR 等自动调度惩罚。
	SchedulingExempt *bool `json:"scheduling_exempt,omitempty"`
	// PriceMultiplier 渠道计费倍率。最终单价 = 模型价格 × 倍率。
	PriceMultiplier *float64 `json:"price_multiplier,omitempty"`

	KeysToAdd    []ChannelKeyAddRequest    `json:"keys_to_add,omitempty"`
	KeysToUpdate []ChannelKeyUpdateRequest `json:"keys_to_update,omitempty"`
	KeysToDelete []int                     `json:"keys_to_delete,omitempty"`

	BypassManagedCheck bool `json:"-"` // 内部使用：允许投影逻辑更新 managed channel
}

type ChannelKeyAddRequest struct {
	Enabled    bool   `json:"enabled"`
	ChannelKey string `json:"channel_key" binding:"required"`
	Remark     string `json:"remark"`
}

type ChannelKeyUpdateRequest struct {
	ID         int     `json:"id" binding:"required"`
	Enabled    *bool   `json:"enabled,omitempty"`
	ChannelKey *string `json:"channel_key,omitempty"`
	Remark     *string `json:"remark,omitempty"`
}

// NormalizeChannelTags 标准化渠道标签：trim、去空、去重。空标签列表返回 nil。
func NormalizeChannelTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	normalized := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		normalized = append(normalized, tag)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

// NormalizeModelRedirects 标准化模型重定向列表：trim、去空、按别名去重。
// 同一别名只保留第一条有效配置。
func NormalizeModelRedirects(redirects []ModelRedirect) []ModelRedirect {
	if len(redirects) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(redirects))
	normalized := make([]ModelRedirect, 0, len(redirects))
	for _, redirect := range redirects {
		alias := strings.TrimSpace(redirect.Model)
		target := strings.TrimSpace(redirect.TargetModel)
		if alias == "" || target == "" {
			continue
		}
		if _, ok := seen[alias]; ok {
			continue
		}
		seen[alias] = struct{}{}
		normalized = append(normalized, ModelRedirect{Model: alias, TargetModel: target})
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

// ModelRedirectMap 返回 别名 -> 上游实际模型 的映射。
func (c *Channel) ModelRedirectMap() map[string]string {
	redirects := NormalizeModelRedirects(c.ModelRedirects)
	if len(redirects) == 0 {
		return nil
	}
	mapping := make(map[string]string, len(redirects))
	for _, redirect := range redirects {
		mapping[redirect.Model] = redirect.TargetModel
	}
	return mapping
}

// ResolveModelRedirect 将候选模型名解析为实际发往上游的模型名。
// 未配置重定向时原样返回。
func (c *Channel) ResolveModelRedirect(modelName string) string {
	if c == nil {
		return modelName
	}
	name := strings.TrimSpace(modelName)
	mapping := c.ModelRedirectMap()
	if len(mapping) == 0 {
		return name
	}
	if target, ok := mapping[name]; ok {
		return target
	}
	return name
}

// ExposedModelNames 返回该渠道对客户端暴露的模型名列表：
// 未开启"仅别名"时 = Model/CustomModel + 重定向别名；
// 开启后仅返回重定向别名（原始模型不暴露）。
func (c *Channel) ExposedModelNames() []string {
	if c == nil {
		return nil
	}
	seen := make(map[string]struct{})
	names := make([]string, 0, 4)
	appendName := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}

	if !c.ModelRedirectOnly {
		for _, name := range xstrings.SplitTrimCompact(",", c.Model, c.CustomModel) {
			appendName(name)
		}
	}
	for _, redirect := range NormalizeModelRedirects(c.ModelRedirects) {
		appendName(redirect.Model)
	}
	return names
}

// IsModelExposed 判断模型名是否对该渠道客户端暴露。
func (c *Channel) IsModelExposed(modelName string) bool {
	if c == nil {
		return false
	}
	name := strings.TrimSpace(modelName)
	for _, exposed := range c.ExposedModelNames() {
		if exposed == name {
			return true
		}
	}
	return false
}

func (c *Channel) GetBaseUrl() string {
	if c == nil || len(c.BaseUrls) == 0 {
		return ""
	}

	bestURL := ""
	bestDelay := 0
	bestSet := false

	for _, bu := range c.BaseUrls {
		if bu.URL == "" {
			continue
		}
		if !bestSet || bu.Delay < bestDelay {
			bestURL = bu.URL
			bestDelay = bu.Delay
			bestSet = true
		}
	}

	return bestURL
}

func (c *Channel) GetChannelKey(opts ...ChannelKeySelectOptions) ChannelKey {
	if c == nil || len(c.Keys) == 0 {
		return ChannelKey{}
	}

	var selectOpts ChannelKeySelectOptions
	if len(opts) > 0 {
		selectOpts = opts[0]
	}

	if selectOpts.PreferredKeyID > 0 {
		for _, k := range c.Keys {
			if k.ID != selectOpts.PreferredKeyID || !k.Enabled || k.ChannelKey == "" {
				continue
			}
			if _, excluded := selectOpts.ExcludeKeyIDs[k.ID]; excluded {
				break
			}
			return k
		}
	}

	best := ChannelKey{}
	bestCost := 0.0
	bestSet := false

	for _, k := range c.Keys {
		if !k.Enabled || k.ChannelKey == "" {
			continue
		}
		if _, excluded := selectOpts.ExcludeKeyIDs[k.ID]; excluded {
			continue
		}
		if !bestSet || k.TotalCost < bestCost {
			best = k
			bestCost = k.TotalCost
			bestSet = true
		}
	}

	if !bestSet {
		return ChannelKey{}
	}
	return best
}
