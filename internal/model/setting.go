package model

import (
	"fmt"
	"net/url"
	"strconv"
)

type SettingKey string

const (
	SettingKeyProxyURL                         SettingKey = "proxy_url"
	SettingKeyStatsSaveInterval                SettingKey = "stats_save_interval"                  // 将统计信息写入数据库的周期(分钟)
	SettingKeyModelInfoUpdateInterval          SettingKey = "model_info_update_interval"           // 模型信息更新间隔(小时)
	SettingKeySyncLLMInterval                  SettingKey = "sync_llm_interval"                    // LLM 同步间隔(小时)
	SettingKeySiteSyncInterval                 SettingKey = "site_sync_interval"                   // 站点账号同步间隔(小时)
	SettingKeySiteCheckinInterval              SettingKey = "site_checkin_interval"                // 站点自动签到间隔(小时)
	SettingKeyRelayLogKeepPeriod               SettingKey = "relay_log_keep_period"                // 日志保存时间范围(天)
	SettingKeyRelayLogKeepEnabled              SettingKey = "relay_log_keep_enabled"               // 是否保留历史日志
	SettingKeyRelayLogFailedDetailEnabled      SettingKey = "relay_log_failed_detail_enabled"      // 失败尝试是否记录请求体/响应体详情
	SettingKeyQualityFailEnabled               SettingKey = "relay_quality_fail_enabled"           // 质量失败检测开关（成功但输出异常→调度降权/冷却）
	SettingKeyQualityFailMinOutput             SettingKey = "relay_quality_fail_min_output"        // 质量失败输出 token 阈值（0=不限制）
	SettingKeyQualityFailMinMaxTokens          SettingKey = "relay_quality_fail_min_max_tokens"    // 排除小 max_tokens 请求（guardrail 等正常短输出）
	SettingKeyQualityFailCooldown              SettingKey = "relay_quality_fail_cooldown"          // 质量失败 key 级冷却秒数（0=仅降权不冷却）
	SettingKeyCORSAllowOrigins                 SettingKey = "cors_allow_origins"                   // 跨域白名单(逗号分隔, 如 "example.com,example2.com"). 为空不允许跨域, "*"允许所有
	SettingKeyCircuitBreakerThreshold          SettingKey = "circuit_breaker_threshold"            // 熔断触发阈值（连续失败次数）
	SettingKeyCircuitBreakerChannelThreshold   SettingKey = "circuit_breaker_channel_threshold"     // 渠道级熔断触发阈值（连续渠道级失败次数）
	SettingKeyCircuitBreakerCooldown           SettingKey = "circuit_breaker_cooldown"             // 熔断基础冷却时间（秒）
	SettingKeyCircuitBreakerMaxCooldown        SettingKey = "circuit_breaker_max_cooldown"         // 熔断最大冷却时间（秒），指数退避上限
	SettingKeyResponsesWSEnabled               SettingKey = "responses_ws_enabled"                 // 是否启用 OpenAI Responses WS 上游能力（仅客户端 WS 入站）
	SettingKeyResponsesWSDefaultMode           SettingKey = "responses_ws_default_mode"            // OpenAI Responses WS 默认模式：off/transform/passthrough
	SettingKeySSEHeartbeatInterval             SettingKey = "sse_heartbeat_interval"               // SSE 流式心跳间隔（秒），0 表示禁用
	SettingKeySSEPreStreamHeartbeatDelay       SettingKey = "sse_pre_stream_heartbeat_delay"       // SSE 上游流建立前心跳首次延迟（秒），0 表示禁用
	SettingKeyGroupHealthEnabled               SettingKey = "group_health_enabled"                 // 是否启用分组健康检查功能
	SettingKeyProjectedChannelAutoGroupEnabled SettingKey = "projected_channel_auto_group_enabled" // 全局站点投影渠道自动分组模式（0关闭/1模糊/2精确/3正则，兼容旧 true/false）
	SettingKeyJWTSecret                        SettingKey = "jwt_secret"                           // JWT 签名密钥（自动生成）
	SettingKeyStatsSiteModelBackfilled         SettingKey = "stats_site_model_backfilled"          // 站点渠道小时聚合是否已回填历史日志
	SettingKeyOutlierRetireEnabled             SettingKey = "outlier_retire_enabled"               // 被动离群退役(POR)总开关
	SettingKeyOutlierRetireInterval            SettingKey = "outlier_retire_interval"              // POR 任务轮询间隔(分钟)
	SettingKeyOutlierWindowCapacity            SettingKey = "outlier_window_capacity"              // POR 滚动窗口评估样本上限(≤20)
	SettingKeyOutlierWindowMinutes             SettingKey = "outlier_window_minutes"               // POR 滚动窗口时间窗(分钟)
	SettingKeyOutlierMinSamples                SettingKey = "outlier_min_samples"                  // POR 最小样本数,不足则跳过判定
	SettingKeyOutlierFailRatePct               SettingKey = "outlier_fail_rate_pct"                // POR 失败率阈值(百分比)
	SettingKeyOutlierConsecFails               SettingKey = "outlier_consec_fails"                 // POR 连续失败阈值
	SettingKeyOutlierRecoverStreak             SettingKey = "outlier_recover_streak"               // POR 连续探活成功恢复阈值
	SettingKeyOutlierReapMinutes               SettingKey = "outlier_reap_minutes"                 // POR 窗口内存回收 TTL(分钟)
	SettingKeyOutlierCFRecoverMinutes          SettingKey = "outlier_cf_recover_minutes"           // POR CF 退役渠道恢复探活冷却(分钟)
	SettingKeyAutoRankEnabled                  SettingKey = "auto_rank_enabled"                     // 自动排序(Auto)总开关
	SettingKeyAutoRankInterval                 SettingKey = "auto_rank_interval"                    // 自动排序控制面任务轮询间隔(秒)
	SettingKeyAutoRankExploreRatio             SettingKey = "auto_rank_explore_ratio"               // 自动排序探索比例(百分比 0-100)
	SettingKeyAutoRankMinSamples               SettingKey = "auto_rank_min_samples"                 // 参与排序所需最小样本数
	SettingKeyAutoRankChannelFactorEnabled     SettingKey = "auto_rank_channel_factor_enabled"      // 渠道级聚合健康修正开关（Auto 模式，默认开）
	SettingKeyAutoRankChannelMinSamples        SettingKey = "auto_rank_channel_min_samples"         // 渠道聚合触发所需最小样本总数
	SettingKeyAutoRankChannelMinModels         SettingKey = "auto_rank_channel_min_models"          // 渠道聚合触发所需最小失败模型数(≥2)
	SettingKeyAutoRankChannelDegradeRate       SettingKey = "auto_rank_channel_degrade_rate"        // 渠道聚合惩罚触发成功率阈值(百分比)
	SettingKeyAutoRankTTFBEnabled              SettingKey = "auto_rank_ttfb_enabled"                // 相对 TTFB 惩罚开关（Auto 排序，默认关）
	SettingKeyAutoRankTTFBWeight               SettingKey = "auto_rank_ttfb_weight"                 // 相对 TTFB 惩罚权重（×慢速比）
	SettingKeyAutoRankTTFBMaxSlowRatio         SettingKey = "auto_rank_ttfb_max_slow_ratio"         // TTFB 慢速比 (s-1) 上限(百分比)
	SettingKeyAutoRankTTFBMinConfidentSample   SettingKey = "auto_rank_ttfb_min_confident_sample"   // TTFB 惩罚置信样本量阈值
	SettingKeyAutoRankSuccessGap               SettingKey = "auto_rank_success_gap"                 // 竞技池准入：与最佳候选的成功率差距门槛(百分比)
	SettingKeyAutoRankLatencyRatio             SettingKey = "auto_rank_latency_ratio"               // 竞技池准入：与最佳候选的延迟倍率门槛(百分比)
	SettingKeyAutoRankHealthThreshold          SettingKey = "auto_rank_health_threshold"            // 竞技池准入：绝对健康度阈值(Wilson 下界，百分比)
	SettingKeyAutoRankChannelMaxShare          SettingKey = "auto_rank_channel_max_share"           // 公平调度：单渠道目标份额上限(百分比)
	SettingKeyAutoRankModelMaxShare            SettingKey = "auto_rank_model_max_share"             // 公平调度：单渠道内单模型目标份额上限(百分比)
	SettingKeyAutoRankSoftmaxTemp              SettingKey = "auto_rank_softmax_temp"                // 公平调度：softmax 温度(×10 存储，50=5.0)
	SettingKeyAutoRankFeedbackEnabled          SettingKey = "auto_rank_feedback_enabled"            // 实际分配反馈纠偏开关（基于 dispatched 的 EWMA actualShare）
	SettingKeyAutoRankFeedbackEwma             SettingKey = "auto_rank_feedback_ewma"               // 反馈纠偏：actualShare EWMA 新样本权重(百分比)
	SettingKeyAutoRankFeedbackTolerance        SettingKey = "auto_rank_feedback_tolerance"          // 反馈纠偏：actualShare 超额容忍度(百分比)
	SettingKeyAutoRankFeedbackPenalty          SettingKey = "auto_rank_feedback_penalty"            // 反馈纠偏：超额降权强度(百分比)
	SettingKeyApiBaseUrl                       SettingKey = "api_base_url"                         // 对外服务基础地址，用于一键导出客户端配置，为空时不显示导出入口
	SettingKeyWebDAVURL                        SettingKey = "webdav_url"                            // WebDAV 服务器地址
	SettingKeyWebDAVUsername                   SettingKey = "webdav_username"                       // WebDAV 用户名
	SettingKeyWebDAVPassword                   SettingKey = "webdav_password"                       // WebDAV 密码
	SettingKeyWebDAVBackupPath                 SettingKey = "webdav_backup_path"                    // WebDAV 远程备份目录
	SettingKeyWebDAVBackupInterval             SettingKey = "webdav_backup_interval"                // WebDAV 自动备份间隔(小时)，0=禁用
	SettingKeyWebDAVRetentionCount             SettingKey = "webdav_retention_count"                // WebDAV 保留备份份数
	SettingKeyWebDAVIncludeStats               SettingKey = "webdav_include_stats"                  // WebDAV 备份是否包含统计数据
)

type Setting struct {
	Key   SettingKey `json:"key" gorm:"primaryKey"`
	Value string     `json:"value" gorm:"not null"`
}

func DefaultSettings() []Setting {
	return []Setting{
		{Key: SettingKeyProxyURL, Value: ""},
		{Key: SettingKeyStatsSaveInterval, Value: "10"},               // 默认10分钟保存一次统计信息
		{Key: SettingKeyCORSAllowOrigins, Value: ""},                  // CORS 默认不允许跨域，设置为 "*" 才允许所有来源
		{Key: SettingKeyModelInfoUpdateInterval, Value: "24"},         // 默认24小时更新一次模型信息
		{Key: SettingKeySyncLLMInterval, Value: "24"},                 // 默认24小时同步一次LLM
		{Key: SettingKeySiteSyncInterval, Value: "12"},                // 默认12小时同步一次站点账号信息
		{Key: SettingKeySiteCheckinInterval, Value: "24"},             // 默认24小时自动签到一次
		{Key: SettingKeyRelayLogKeepPeriod, Value: "7"},               // 默认日志保存7天
		{Key: SettingKeyRelayLogKeepEnabled, Value: "true"},           // 默认保留历史日志
		{Key: SettingKeyRelayLogFailedDetailEnabled, Value: "true"},   // 默认记录失败尝试请求详情
		{Key: SettingKeyQualityFailEnabled, Value: "true"},            // 默认启用质量失败检测（成功但输出异常→降权/冷却）
		{Key: SettingKeyQualityFailMinOutput, Value: "100"},           // 输出 <100 token 视为可疑
		{Key: SettingKeyQualityFailMinMaxTokens, Value: "1024"},       // max_tokens <1024 的请求（guardrail）不判定
		{Key: SettingKeyQualityFailCooldown, Value: "60"},             // 质量失败冷却 60 秒（0=仅降权）
		{Key: SettingKeyCircuitBreakerThreshold, Value: "5"},          // 默认连续失败5次触发熔断
		{Key: SettingKeyCircuitBreakerChannelThreshold, Value: "3"},   // 默认连续3次渠道级失败触发渠道熔断
		{Key: SettingKeyCircuitBreakerCooldown, Value: "60"},          // 默认基础冷却60秒
		{Key: SettingKeyCircuitBreakerMaxCooldown, Value: "600"},      // 默认最大冷却600秒（10分钟）
		{Key: SettingKeyResponsesWSEnabled, Value: "false"},           // 默认关闭 OpenAI Responses WS 新路径
		{Key: SettingKeyResponsesWSDefaultMode, Value: "passthrough"}, // 启用后默认使用协议保真的 passthrough
		{Key: SettingKeySSEHeartbeatInterval, Value: "0"},             // 默认禁用 SSE 流式心跳
		{Key: SettingKeySSEPreStreamHeartbeatDelay, Value: "0"},       // 默认禁用 SSE 上游流建立前心跳
		{Key: SettingKeyGroupHealthEnabled, Value: "false"},           // 默认不显示/运行分组健康检查，避免打扰主界面
		{Key: SettingKeyProjectedChannelAutoGroupEnabled, Value: "0"}, // 默认不强制站点投影渠道自动分组
		{Key: SettingKeyJWTSecret, Value: ""},                         // 为空时自动生成
		{Key: SettingKeyStatsSiteModelBackfilled, Value: "false"},
		{Key: SettingKeyOutlierRetireEnabled, Value: "false"}, // 默认关闭被动离群退役，保守上线
		{Key: SettingKeyOutlierRetireInterval, Value: "2"},    // 默认每 2 分钟评估一次
		{Key: SettingKeyOutlierWindowCapacity, Value: "20"},   // 评估取最近 20 条
		{Key: SettingKeyOutlierWindowMinutes, Value: "10"},    // 时间窗 10 分钟
		{Key: SettingKeyOutlierMinSamples, Value: "8"},        // 样本不足 8 条直接 PASS
		{Key: SettingKeyOutlierFailRatePct, Value: "85"},      // 失败率 ≥85% 才候选
		{Key: SettingKeyOutlierConsecFails, Value: "10"},      // 连续失败 ≥10 次
		{Key: SettingKeyOutlierRecoverStreak, Value: "2"},     // 连续探活成功 2 次恢复
		{Key: SettingKeyOutlierReapMinutes, Value: "30"},      // 窗口 30 分钟无流量回收
		{Key: SettingKeyOutlierCFRecoverMinutes, Value: "30"}, // CF 退役渠道 30 分钟后才探活恢复
		{Key: SettingKeyAutoRankEnabled, Value: "true"},    // 默认启用自动排序（仅在分组切换为 Auto 模式时生效）
		{Key: SettingKeyAutoRankInterval, Value: "60"},     // 默认每 60 秒执行一次快照落库与内存回收
		{Key: SettingKeyAutoRankExploreRatio, Value: "20"}, // 默认 20% 请求用于探索欠采样候选（冷启动模型需足够机会积累样本）
		{Key: SettingKeyAutoRankMinSamples, Value: "3"},    // 样本 ≥3 条才按得分参与排序
		{Key: SettingKeyAutoRankChannelFactorEnabled, Value: "true"}, // 默认启用渠道级聚合健康修正
		{Key: SettingKeyAutoRankChannelMinSamples, Value: "8"},       // 渠道聚合样本 ≥8 条才评估
		{Key: SettingKeyAutoRankChannelMinModels, Value: "2"},        // ≥2 个模型同时失败才触发渠道降级
		{Key: SettingKeyAutoRankChannelDegradeRate, Value: "85"},     // 聚合成功率 <85% 进入惩罚
		{Key: SettingKeyAutoRankTTFBEnabled, Value: "false"},         // 相对 TTFB 惩罚默认关闭，保守上线
		{Key: SettingKeyAutoRankTTFBWeight, Value: "20"},             // TTFB 惩罚权重 20
		{Key: SettingKeyAutoRankTTFBMaxSlowRatio, Value: "200"},      // 慢速比上限 2.0
		{Key: SettingKeyAutoRankTTFBMinConfidentSample, Value: "10"}, // TTFB 置信样本 10
		{Key: SettingKeyAutoRankSuccessGap, Value: "2"},              // 成功率差距门槛 2%（=0.02）
		{Key: SettingKeyAutoRankLatencyRatio, Value: "150"},          // 延迟倍率门槛 1.5
		{Key: SettingKeyAutoRankHealthThreshold, Value: "85"},        // 绝对健康度阈值 0.85（Wilson 下界）
		{Key: SettingKeyAutoRankChannelMaxShare, Value: "70"},        // 单渠道份额上限 70%
		{Key: SettingKeyAutoRankModelMaxShare, Value: "80"},          // 单渠道内单模型份额上限 80%
		{Key: SettingKeyAutoRankSoftmaxTemp, Value: "50"},            // softmax 温度 5.0（×10 存储）
		{Key: SettingKeyAutoRankFeedbackEnabled, Value: "true"},      // 默认启用实际分配反馈纠偏
		{Key: SettingKeyAutoRankFeedbackEwma, Value: "30"},           // EWMA 新样本权重 0.3
		{Key: SettingKeyAutoRankFeedbackTolerance, Value: "10"},      // 超额容忍度 0.10
		{Key: SettingKeyAutoRankFeedbackPenalty, Value: "30"},        // 超额降权强度 0.30/单位超额
		{Key: SettingKeyApiBaseUrl, Value: ""},                  // 默认为空，不显示客户端导出入口
		{Key: SettingKeyWebDAVURL, Value: ""},                   // 默认为空，未配置
		{Key: SettingKeyWebDAVUsername, Value: ""},              // 默认为空
		{Key: SettingKeyWebDAVPassword, Value: ""},              // 默认为空
		{Key: SettingKeyWebDAVBackupPath, Value: "/octopus-backups"}, // 默认远程目录
		{Key: SettingKeyWebDAVBackupInterval, Value: "0"},       // 默认禁用自动备份
		{Key: SettingKeyWebDAVRetentionCount, Value: "10"},      // 默认保留10份
		{Key: SettingKeyWebDAVIncludeStats, Value: "true"},      // 默认包含统计数据
	}
}

func (s *Setting) Validate() error {
	switch s.Key {
	case SettingKeyModelInfoUpdateInterval, SettingKeySyncLLMInterval, SettingKeySiteSyncInterval,
		SettingKeySiteCheckinInterval, SettingKeyRelayLogKeepPeriod,
		SettingKeyCircuitBreakerThreshold, SettingKeyCircuitBreakerChannelThreshold, SettingKeyCircuitBreakerCooldown, SettingKeyCircuitBreakerMaxCooldown:
		_, err := strconv.Atoi(s.Value)
		if err != nil {
			return fmt.Errorf("setting value must be an integer")
		}
		return nil
	case SettingKeyOutlierWindowCapacity:
		// 评估样本上限受环形缓冲物理容量约束（≤20，见 outlierwindow.physicalCap）。
		return validateIntRange(s.Value, 1, 20)
	case SettingKeyOutlierFailRatePct:
		// 失败率阈值为百分比，超出 [1,100] 会被运行时回退默认值，与展示不符。
		return validateIntRange(s.Value, 1, 100)
	case SettingKeyOutlierRetireInterval, SettingKeyOutlierWindowMinutes, SettingKeyOutlierMinSamples,
		SettingKeyOutlierConsecFails, SettingKeyOutlierRecoverStreak,
		SettingKeyOutlierReapMinutes, SettingKeyOutlierCFRecoverMinutes,
		SettingKeyAutoRankInterval, SettingKeyAutoRankMinSamples,
		SettingKeyAutoRankChannelMinSamples,
		SettingKeyWebDAVRetentionCount:
		// 时间窗/样本/连击/间隔等：0 或负值无意义，下限为 1。
		return validateIntMin(s.Value, 1)
	case SettingKeyAutoRankExploreRatio, SettingKeyAutoRankChannelDegradeRate, SettingKeyAutoRankTTFBMaxSlowRatio,
		SettingKeyAutoRankSuccessGap, SettingKeyAutoRankHealthThreshold,
		SettingKeyAutoRankChannelMaxShare, SettingKeyAutoRankModelMaxShare,
		SettingKeyAutoRankFeedbackTolerance, SettingKeyAutoRankFeedbackPenalty:
		// 比例类取百分比，允许 0（纯贪婪）到 100（全探索）。
		return validateIntRange(s.Value, 0, 100)
	case SettingKeyAutoRankTTFBWeight:
		// TTFB 惩罚权重，允许 0（禁用惩罚）及以上。
		return validateIntMin(s.Value, 0)
	case SettingKeyAutoRankChannelMinModels:
		// 渠道级降级需要至少 2 个模型同时失败（单模型失败不触发，语义下限为 2）。
		return validateIntMin(s.Value, 2)
	case SettingKeyAutoRankTTFBMinConfidentSample:
		return validateIntMin(s.Value, 1)
	case SettingKeyAutoRankLatencyRatio:
		// 延迟倍率门槛必须 ≥1.0（1.0 表示不允许任何延迟差异）。
		return validateIntMin(s.Value, 100)
	case SettingKeyAutoRankSoftmaxTemp:
		// softmax 温度 ×10 存储（50=5.0），下限 10（=1.0），防止用户设 T<1 导致赢家通吃。
		return validateIntMin(s.Value, 10)
	case SettingKeyAutoRankFeedbackEwma:
		// EWMA 新样本权重必须 <1.0（存百分比 1-99），避免 alpha=1 时完全丢弃历史。
		return validateIntRange(s.Value, 1, 99)
	case SettingKeySSEHeartbeatInterval, SettingKeySSEPreStreamHeartbeatDelay, SettingKeyWebDAVBackupInterval,
		SettingKeyQualityFailMinOutput, SettingKeyQualityFailMinMaxTokens, SettingKeyQualityFailCooldown:
		value, err := strconv.Atoi(s.Value)
		if err != nil {
			return fmt.Errorf("setting value must be an integer")
		}
		if value < 0 {
			return fmt.Errorf("setting value must be non-negative")
		}
		return nil
	case SettingKeyRelayLogKeepEnabled, SettingKeyQualityFailEnabled, SettingKeyResponsesWSEnabled, SettingKeyGroupHealthEnabled, SettingKeyStatsSiteModelBackfilled, SettingKeyOutlierRetireEnabled, SettingKeyAutoRankEnabled, SettingKeyAutoRankChannelFactorEnabled, SettingKeyAutoRankTTFBEnabled, SettingKeyAutoRankFeedbackEnabled, SettingKeyWebDAVIncludeStats:
		if s.Value != "true" && s.Value != "false" {
			return fmt.Errorf("setting value must be true or false")
		}
		return nil
	case SettingKeyProjectedChannelAutoGroupEnabled:
		if _, ok := ParseAutoGroupSettingValue(s.Value); !ok {
			return fmt.Errorf("setting value must be one of 0, 1, 2, 3, true, false")
		}
		return nil
	case SettingKeyResponsesWSDefaultMode:
		switch s.Value {
		case "off", "transform", "passthrough":
			return nil
		default:
			return fmt.Errorf("setting value must be one of off, transform, passthrough")
		}
	case SettingKeyProxyURL:
		if s.Value == "" {
			return nil
		}
		parsedURL, err := url.Parse(s.Value)
		if err != nil {
			return fmt.Errorf("proxy URL is invalid: %w", err)
		}
		validSchemes := map[string]bool{
			"http":   true,
			"https":  true,
			"socks5": true,
		}
		if !validSchemes[parsedURL.Scheme] {
			return fmt.Errorf("proxy URL scheme must be http, https, socks, or socks5")
		}
		if parsedURL.Host == "" {
			return fmt.Errorf("proxy URL must have a host")
		}
		return nil
	case SettingKeyApiBaseUrl:
		if s.Value == "" {
			return nil
		}
		parsedURL, err := url.Parse(s.Value)
		if err != nil {
			return fmt.Errorf("api base URL is invalid: %w", err)
		}
		if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
			return fmt.Errorf("api base URL scheme must be http or https")
		}
		if parsedURL.Host == "" {
			return fmt.Errorf("api base URL must have a host")
		}
		return nil
	case SettingKeyWebDAVURL:
		if s.Value == "" {
			return nil
		}
		parsedURL, err := url.Parse(s.Value)
		if err != nil {
			return fmt.Errorf("WebDAV URL is invalid: %w", err)
		}
		if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
			return fmt.Errorf("WebDAV URL scheme must be http or https")
		}
		if parsedURL.Host == "" {
			return fmt.Errorf("WebDAV URL must have a host")
		}
		return nil
	}

	return nil
}

// validateIntRange 校验 v 为整数且落在闭区间 [lo, hi]。
func validateIntRange(v string, lo, hi int) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("setting value must be an integer")
	}
	if n < lo || n > hi {
		return fmt.Errorf("setting value must be between %d and %d", lo, hi)
	}
	return nil
}

// validateIntMin 校验 v 为整数且不小于 lo。
func validateIntMin(v string, lo int) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("setting value must be an integer")
	}
	if n < lo {
		return fmt.Errorf("setting value must be at least %d", lo)
	}
	return nil
}
