package model

type LLMPrice struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

type LLMInfo struct {
	Name string `json:"name" gorm:"primaryKey;not null"`
	LLMPrice
}

type LLMChannel struct {
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	ChannelID       int    `json:"channel_id"`
	ChannelName     string `json:"channel_name"`
	SiteID          *int   `json:"site_id,omitempty"`
	SiteAccountID   *int   `json:"site_account_id,omitempty"`
	SiteGroupKey    string `json:"site_group_key,omitempty"`
	SiteGroupName   string `json:"site_group_name,omitempty"`
	SiteName        string `json:"site_name,omitempty"`
	SiteAccountName string `json:"site_account_name,omitempty"`
	EndpointType    string `json:"endpoint_type,omitempty"`
	// AutoRank 该 (渠道, 模型) 的自动排序性能统计与熔断状态摘要。
	// 仅在组内 Auto 模式或存在熔断/降级信号时由 handler 附加，用于
	// 管理面板解释"自动排序为什么把流量分到这里/那里"。
	AutoRank *LLMAutoRankHealth `json:"auto_rank,omitempty"`
}

// LLMAutoRankHealth 渠道-模型的 AutoRank 被动性能统计与熔断状态摘要。
// 字段全部来自 relay/balancer 的实时窗口（AutoRankStats + 渠道级熔断），
// 不做持久化，仅随 /api/v1/model/channel 返回给管理面板展示。
type LLMAutoRankHealth struct {
	// Samples 时间窗口内采样的请求数。
	Samples int `json:"samples"`
	// Failures 时间窗口内失败请求数。
	Failures int `json:"failures"`
	// SuccessRate 窗口成功率（0~1），无样本时为 0。
	SuccessRate float64 `json:"success_rate"`
	// EWMALatencyMS 窗口内 EWMA 平滑后的延迟（毫秒）。
	EWMALatencyMS float64 `json:"ewma_latency_ms"`
	// Score 基础排序得分：成功率*100 - 延迟(秒)。
	// 与 AutoRank 排序档位一致（无样本/样本不足时仅供参考）。
	Score float64 `json:"score"`
	// Degraded 渠道是否处于聚合惩罚状态（系数 <1）：窗口内多模型同时
	// 恶化导致该渠道所有模型得分被统一压低。
	Degraded bool `json:"degraded"`
	// ChannelTripped 渠道级熔断是否处于 Open/HalfOpen。
	ChannelTripped bool `json:"channel_tripped"`
	// ChannelCooldownSec 渠道级熔断剩余冷却秒数；未熔断为 0。
	ChannelCooldownSec int64 `json:"channel_cooldown_sec"`
	// ChannelTripCount 渠道级累计熔断触发次数（指数退避依据）。
	ChannelTripCount int64 `json:"channel_trip_count"`
}

type GeminiModel struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
}

type GeminiModelList struct {
	Models        []GeminiModel `json:"models"`
	NextPageToken string        `json:"nextPageToken"`
}

type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int    `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type OpenAIModelList struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}
type AnthropicModel struct {
	ID          string `json:"id"`
	CreatedAt   string `json:"created_at"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
}

type AnthropicModelList struct {
	Data    []AnthropicModel `json:"data"`
	FirstID string           `json:"first_id"`
	HasMore bool             `json:"has_more"`
	LastID  string           `json:"last_id"`
}
