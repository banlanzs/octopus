package model

import "time"

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

// ChannelModelPrice 渠道内模型价格。主键 = (channel_id, model_name)。
// 查询优先级高于全局 LLMInfo，未配置时回退到全局官方价。
type ChannelModelPrice struct {
	ChannelID int `json:"channel_id" gorm:"primaryKey;not null"`
	ModelName string `json:"model_name" gorm:"primaryKey;not null;size:128"`
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
	// HasChannelPrice 该 (渠道, 模型) 是否配置了专属渠道价（true 表示计费不走全局兜底）。
	// 由 model handler 在 listLLMByChannel 时附加，供前端区分"是否已单独定价"。
	HasChannelPrice bool `json:"has_channel_price"`
	// Price 该 (渠道, 模型) 的计费价格（已乘渠道倍率，供价格页直接渲染展示）。
	// 由 model handler 在 listLLMByChannel 时附加。
	Price *LLMPrice `json:"price,omitempty"`
	// PriceMultiplier 该渠道的计费倍率（默认 1）。最终单价 = 模型价格 × 倍率。
	PriceMultiplier float64 `json:"price_multiplier,omitempty"`
	// AutoRank 该 (渠道, 模型) 的自动排序性能统计与熔断状态摘要。
	// 仅在组内 Auto 模式或存在熔断/降级信号时由 handler 附加，用于
	// 管理面板解释"自动排序为什么把流量分到这里/那里"。
	AutoRank *LLMAutoRankHealth `json:"auto_rank,omitempty"`
}

// LLMAutoRankHealth 渠道-模型的 AutoRank 被动性能统计与熔断状态摘要。
// 字段全部来自 relay/balancer 的实时窗口（AutoRankStats + 渠道级熔断），
// 不做持久化，随模型渠道或分组列表返回给管理面板展示。
type LLMAutoRankHealth struct {
	// Samples 时间窗口内采样的请求数。
	Samples int `json:"samples"`
	// ProbeSamples Samples 中来自主动探测的条数。探测只补成功率信号，
	// 不写延迟、不推进"样本充足"判定，因此排序档位看的是 Samples-ProbeSamples。
	ProbeSamples int `json:"probe_samples"`
	// ProbeDead 只有探测样本且探测全部失败：调度侧已把该候选从探索池剔除，
	// 不再拿真实用户请求去撞，但它仍留在 failover 链末尾兜底。
	// 一旦出现任何真实样本即失效。
	ProbeDead bool `json:"probe_dead"`
	// Failures 时间窗口内失败请求数。
	Failures int `json:"failures"`
	// SuccessRate 窗口成功率（0~1），无样本时为 0。
	SuccessRate float64 `json:"success_rate"`
	// SuccessConfidence Wilson 置信下界，用于避免少量成功样本过早登顶。
	SuccessConfidence float64 `json:"success_confidence"`
	// EWMALatencyMS 窗口内 EWMA 平滑后的延迟（毫秒）。
	EWMALatencyMS float64 `json:"ewma_latency_ms"`
	// EWMATTFBMS 首 Token 时间的 EWMA；无独立首 Token 时回退为完整耗时。
	EWMATTFBMS float64 `json:"ewma_ttfb_ms"`
	// Score 基础排序得分：成功率置信下界*100 - 延迟(秒)。
	// 与 AutoRank 排序档位一致（无样本/样本不足时仅供参考）。
	Score float64 `json:"score"`
	// EffectiveScore 应用渠道修正后的实际质量得分。
	EffectiveScore   float64   `json:"effective_score"`
	Rank             int       `json:"rank"`
	Tier             int       `json:"tier"`
	TargetShare      float64   `json:"target_share"`
	ActualShare      float64   `json:"actual_share"`
	LastSampleAt     time.Time `json:"last_sample_at"`
	LastDispatchedAt time.Time `json:"last_dispatched_at"`
	SelectionReason  string    `json:"selection_reason"`
	// Degraded 渠道是否处于聚合惩罚状态（系数 <1）：窗口内多模型同时
	// 恶化导致该渠道所有模型得分被统一压低。
	Degraded bool `json:"degraded"`
	// ChannelTripped 渠道级熔断是否处于 Open/HalfOpen。
	ChannelTripped bool `json:"channel_tripped"`
	// ChannelCooldownSec 渠道级熔断剩余冷却秒数；未熔断为 0。
	ChannelCooldownSec int64 `json:"channel_cooldown_sec"`
	// ChannelTripCount 渠道级累计熔断触发次数（指数退避依据）。
	ChannelTripCount int64 `json:"channel_trip_count"`
	// TrailSummary 窗口样本时间线摘要（✓ 成功 / ✗ 失败 / p 探测，从旧到新，最多 20 字符）。
	// 直观展示排序依据，供管理面板健康徽章提示展示；无样本时为空。
	TrailSummary string `json:"trail_summary,omitempty"`
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
