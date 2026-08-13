package relay

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/price"
	transformerModel "github.com/bestruirui/octopus/internal/transformer/model"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/bestruirui/octopus/internal/utils/tokenizer"
	"github.com/looplj/axonhub/llm"
)

// RelayMetrics 负责最终的日志收集与持久化
type RelayMetrics struct {
	APIKeyID     int
	RequestModel string
	StartTime    time.Time

	// 首 Token 时间
	FirstTokenTime time.Time

	// 请求和响应内容
	RawRequest       []byte
	InternalRequest  *transformerModel.InternalLLMRequest
	InternalResponse *transformerModel.InternalLLMResponse

	// InboundAdapter 用于将内部响应转换回入站协议格式（如 Anthropic / OpenAI Responses），
	// 使日志记录的响应体与请求发起协议保持一致，而非统一序列化为 OpenAI 格式。
	// 为 nil 时退回原行为（序列化内部响应）。
	InboundAdapter transformerModel.Inbound

	// RequestHeaders 记录客户端原始请求头（JSON 序列化，已过滤 hop-by-hop 与敏感头），
	// 供日志详情页展示请求头/请求体区分。空值表示未捕获到请求头。
	RequestHeaders string
	// RequestPath 记录客户端请求路径（方法+路径，如 "POST /v1/messages"），供日志展示。
	RequestPath string

	// 统计指标
	ActualModel string
	Stats       model.StatsMetrics
	UsedWS      bool
	WSMode      *model.RelayLogWSMode
	WSExecMode  *model.RelayLogWSExecMode
	WSRecovery  *model.RelayLogWSRecovery

	TransportInputTokens *int
	BillInputTokens      *int
	CacheReadTokens      *int
	CacheWriteTokens     *int

	// ReasoningEffort 记录本次请求的推理强度（reasoning_effort / output_config.effort 值）。
	// 从 InternalRequest 提取，供日志页展示。
	ReasoningEffort string

	// AxonResponse 保存 axonhub 归一化响应（llm.Response），供文本迁移路径
	// 记录日志与用量。与 InternalResponse 互斥：文本路径用 AxonResponse，
	// 自研路径（WS/compact/images）用 InternalResponse。
	AxonResponse *llm.Response
}

func NewRelayMetrics(apiKeyID int, requestModel string, rawBody []byte, req *transformerModel.InternalLLMRequest) *RelayMetrics {
	effort := ""
	if req != nil {
		effort = req.ReasoningEffort
	}
	return &RelayMetrics{
		APIKeyID:        apiKeyID,
		RequestModel:    requestModel,
		StartTime:       time.Now(),
		RawRequest:      rawBody,
		InternalRequest: req,
		ReasoningEffort: effort,
	}
}

func (m *RelayMetrics) SetFirstTokenTime(t time.Time) {
	m.FirstTokenTime = t
}

func (m *RelayMetrics) SetTransportRequestPayload(payload []byte, modelName string) {
	if len(payload) == 0 {
		return
	}
	count := tokenizer.CountTokens(string(payload), modelName)
	m.TransportInputTokens = intPtr(count)
}

func (m *RelayMetrics) SetWSMode(mode model.RelayLogWSMode) {
	if mode == "" {
		return
	}
	m.WSMode = wsModePtr(mode)
}

func (m *RelayMetrics) SetWSExecMode(mode model.RelayLogWSExecMode) {
	if mode == "" {
		return
	}
	m.WSExecMode = wsExecModePtr(mode)
}

func (m *RelayMetrics) SetWSRecovery(recovery model.RelayLogWSRecovery) {
	if recovery == "" {
		return
	}
	m.WSRecovery = wsRecoveryPtr(recovery)
}

func (m *RelayMetrics) SetInternalResponse(resp *transformerModel.InternalLLMResponse, actualModel string, channelID int) {
	m.InternalResponse = resp
	m.ActualModel = actualModel

	if resp == nil {
		return
	}

	inputReported := false
	if usage := resp.Usage; usage != nil {
		nonCachedInput := usage.BillableNonCachedInput()
		cacheReadTokens := usage.BillableCacheReadInput()
		cacheWriteTokens := usage.BillableCacheWriteInput()

		m.BillInputTokens = intPtr(int(nonCachedInput))
		m.CacheReadTokens = intPtr(int(cacheReadTokens))
		m.CacheWriteTokens = intPtr(int(cacheWriteTokens))
		m.Stats.InputToken = usage.PromptTokens
		m.Stats.OutputToken = usage.CompletionTokens
		inputReported = usage.EffectiveInputTokens() > 0

		if modelPrice := resolveModelPrice(channelID, actualModel); modelPrice != nil {
			m.Stats.InputCost = (float64(cacheReadTokens)*modelPrice.CacheRead +
				float64(cacheWriteTokens)*modelPrice.CacheWrite +
				float64(nonCachedInput)*modelPrice.Input) * 1e-6
			m.Stats.OutputCost = float64(usage.CompletionTokens) * modelPrice.Output * 1e-6
		}
	}

	// 降级：上游未上报 input（usage 缺失，或 usage 中输入侧全为 0）时，用请求侧
	// 估算的 TransportInputTokens 兜底，使 input token/费用不为 0；output 无法从
	// 请求侧估算，保持 0。tiktoken 统一用 o200k_base，对 Claude/Gemini 为近似值。
	if !inputReported && m.TransportInputTokens != nil && *m.TransportInputTokens > 0 {
		estimated := int64(*m.TransportInputTokens)
		m.Stats.InputToken = estimated
		m.BillInputTokens = intPtr(int(estimated))
		if modelPrice := resolveModelPrice(channelID, actualModel); modelPrice != nil {
			m.Stats.InputCost = float64(estimated) * modelPrice.Input * 1e-6
		}
	}
}

// SetAxonResponse 从 axonhub 归一化响应（llm.Response）提取用量与成本，
// 供文本迁移路径（TextHandler）复用 RelayMetrics 的日志/统计链路。
// 计费口径与 SetInternalResponse 一致：缓存读/缓存写/普通输入三档分别计费。
func (m *RelayMetrics) SetAxonResponse(resp *llm.Response, actualModel string, channelID int) {
	m.AxonResponse = resp
	m.ActualModel = actualModel

	if resp == nil || resp.Usage == nil {
		return
	}

	usage := resp.Usage
	m.Stats.InputToken = usage.PromptTokens
	m.Stats.OutputToken = usage.CompletionTokens

	var cacheRead, cacheWrite int64
	if usage.PromptTokensDetails != nil {
		cacheRead = usage.PromptTokensDetails.CachedTokens
		cacheWrite = usage.PromptTokensDetails.WriteCachedTokens
	}
	nonCached := usage.PromptTokens - cacheRead - cacheWrite
	if nonCached < 0 {
		nonCached = usage.PromptTokens
	}
	m.BillInputTokens = intPtr(int(nonCached))
	m.CacheReadTokens = intPtr(int(cacheRead))
	m.CacheWriteTokens = intPtr(int(cacheWrite))

	if modelPrice := resolveModelPrice(channelID, actualModel); modelPrice != nil {
		m.Stats.InputCost = (float64(cacheRead)*modelPrice.CacheRead +
			float64(cacheWrite)*modelPrice.CacheWrite +
			float64(nonCached)*modelPrice.Input) * 1e-6
		m.Stats.OutputCost = float64(usage.CompletionTokens) * modelPrice.Output * 1e-6
	}
}

func (m *RelayMetrics) Save(ctx context.Context, success bool, err error, attempts []model.ChannelAttempt) {
	m.SaveWithChannelStats(ctx, success, err, attempts, true)
}

func (m *RelayMetrics) SaveWithChannelStats(ctx context.Context, success bool, err error, attempts []model.ChannelAttempt, updateChannelStats bool) {
	duration := time.Since(m.StartTime)

	globalStats := model.StatsMetrics{
		WaitTime:    duration.Milliseconds(),
		InputToken:  m.Stats.InputToken,
		OutputToken: m.Stats.OutputToken,
		InputCost:   m.Stats.InputCost,
		OutputCost:  m.Stats.OutputCost,
	}
	if success {
		globalStats.RequestSuccess = 1
	} else {
		globalStats.RequestFailed = 1
	}

	channelID, channelName := finalChannel(attempts)
	op.StatsTotalUpdate(globalStats)
	op.StatsHourlyUpdate(globalStats)
	op.StatsDailyUpdate(context.Background(), globalStats)
	op.StatsAPIKeyUpdate(m.APIKeyID, globalStats)
	if updateChannelStats {
		op.StatsChannelUpdate(channelID, globalStats)
	} else {
		updateFinalChannelUsageStats(channelID, globalStats)
	}
	op.StatsSiteModelHourlyRecordAttempts(attempts, m.ActualModel)

	// 上游未上报 usage（或输入侧全为 0）时打告警，便于定位是哪个通道缺失 usage。
	usageMissing := false
	if m.AxonResponse != nil {
		usageMissing = m.AxonResponse.Usage == nil || m.AxonResponse.Usage.PromptTokens == 0
	} else {
		usageMissing = m.InternalResponse == nil || m.InternalResponse.Usage == nil ||
			m.InternalResponse.Usage.EffectiveInputTokens() == 0
	}
	if success && usageMissing {
		fallbackInput := 0
		if m.TransportInputTokens != nil {
			fallbackInput = *m.TransportInputTokens
		}
		log.Debugw("relay.usage_missing",
			"actual_model", m.ActualModel,
			"channel_id", channelID,
			"channel", channelName,
			"had_usage", m.InternalResponse != nil && m.InternalResponse.Usage != nil,
			"fallback_input_tokens", fallbackInput,
		)
	}

	if conf.AppConfig.Log.Relay.Summary || !success {
		fields := []interface{}{
			"model", m.RequestModel,
			"actual_model", m.ActualModel,
			"channel_id", channelID,
			"channel", channelName,
			"success", success,
			"duration_ms", duration.Milliseconds(),
			"input_token", m.Stats.InputToken,
			"output_token", m.Stats.OutputToken,
			"input_cost", m.Stats.InputCost,
			"output_cost", m.Stats.OutputCost,
			"total_cost", m.Stats.InputCost + m.Stats.OutputCost,
			"attempts", len(attempts),
			"ws", m.UsedWS,
		}
		if success {
			log.Infow("relay.complete", fields...)
		} else {
			log.Warnw("relay.complete", fields...)
		}
	}

	m.saveLog(ctx, success, err, duration, attempts, channelID, channelName)
}

func finalChannel(attempts []model.ChannelAttempt) (int, string) {
	var lastID int
	var lastName string
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if a.Status == model.AttemptSuccess {
			return a.ChannelID, a.ChannelName
		}
		if a.Status == model.AttemptFailed && lastID == 0 {
			lastID = a.ChannelID
			lastName = a.ChannelName
		}
	}
	return lastID, lastName
}

func (m *RelayMetrics) saveLog(ctx context.Context, success bool, err error, duration time.Duration, attempts []model.ChannelAttempt, channelID int, channelName string) {
	actualModel := m.ActualModel
	if actualModel == "" {
		actualModel = m.RequestModel
	}

	relayLog := model.RelayLog{
		Time:             m.StartTime.Unix(),
		RequestModelName: m.RequestModel,
		ChannelName:      channelName,
		ChannelId:        channelID,
		ActualModelName:  actualModel,
		UseTime:          int(duration.Milliseconds()),
		Attempts:         attempts,
		TotalAttempts:    len(attempts),
		UsedWS:           m.UsedWS,
	}

	if apiKey, getErr := op.APIKeyGet(m.APIKeyID, ctx); getErr == nil {
		relayLog.RequestAPIKeyName = apiKey.Name
	}

	// 首字时间
	if !m.FirstTokenTime.IsZero() {
		relayLog.Ftut = int(m.FirstTokenTime.Sub(m.StartTime).Milliseconds())
	}

	// Usage：统一从 Stats 读取。Stats 在 SetInternalResponse 中已由上游 usage 填充，
	// 或在 usage 缺失时由 TransportInputTokens 降级填充，确保降级值也写入日志。
	relayLog.InputTokens = int(m.Stats.InputToken)
	relayLog.OutputTokens = int(m.Stats.OutputToken)
	relayLog.Cost = m.Stats.InputCost + m.Stats.OutputCost
	relayLog.TransportInputTokens = m.TransportInputTokens
	relayLog.BillInputTokens = m.BillInputTokens
	relayLog.CacheReadTokens = m.CacheReadTokens
	relayLog.CacheWriteTokens = m.CacheWriteTokens
	relayLog.ReasoningEffort = m.ReasoningEffort
	relayLog.WSMode = m.WSMode
	relayLog.WSExecMode = m.WSExecMode
	relayLog.WSRecovery = m.WSRecovery
	relayLog.RequestHeaders = m.RequestHeaders
	relayLog.RequestPath = m.RequestPath

	// 请求内容：优先原始请求体，保留 provider 专有字段（如 Anthropic cache_control）
	if len(m.RawRequest) > 0 {
		relayLog.RequestContent = string(m.RawRequest)
	} else if m.InternalRequest != nil {
		if reqJSON, jsonErr := json.Marshal(m.InternalRequest); jsonErr == nil {
			relayLog.RequestContent = string(reqJSON)
		}
	}

	// 响应内容：若配置了 InboundAdapter，则将内部响应转换回入站协议格式
	// （如 Anthropic Message / OpenAI Responses），使日志中响应体与请求发起
	// 协议保持一致，而非统一序列化为 OpenAI 格式；转换失败或无适配器时
	// 回退为内部 OpenAI 格式序列化。
	relayLog.ResponseContent = m.responseContentForLog(ctx)

	// 错误信息
	if err != nil {
		relayLog.Error = err.Error()
	}
	relayLog.Success = success

	if logErr := op.RelayLogAdd(ctx, relayLog); logErr != nil {
		log.Warnf("failed to save relay log: %v", logErr)
	}
}

func updateFinalChannelUsageStats(channelID int, metrics model.StatsMetrics) {
	if channelID == 0 {
		return
	}
	usageStats := model.StatsMetrics{
		InputToken:  metrics.InputToken,
		OutputToken: metrics.OutputToken,
		InputCost:   metrics.InputCost,
		OutputCost:  metrics.OutputCost,
	}
	if usageStats.InputToken == 0 && usageStats.OutputToken == 0 && usageStats.InputCost == 0 && usageStats.OutputCost == 0 {
		return
	}
	op.StatsChannelUpdate(channelID, usageStats)
}

func intPtr(value int) *int {
	return &value
}

// resolveModelPrice 返回实际计费价格：渠道内模型价优先，未配置时回退全局价。
func resolveModelPrice(channelID int, actualModel string) *model.LLMPrice {
	return price.GetChannelModelPrice(channelID, actualModel)
}

func wsModePtr(value model.RelayLogWSMode) *model.RelayLogWSMode {
	return &value
}

func wsExecModePtr(value model.RelayLogWSExecMode) *model.RelayLogWSExecMode {
	return &value
}

func wsRecoveryPtr(value model.RelayLogWSRecovery) *model.RelayLogWSRecovery {
	return &value
}

// responseContentForLog 生成日志用的响应体：若配置了 InboundAdapter，则将
// 内部响应转换回入站协议格式（如 Anthropic Message / OpenAI Responses），
// 使日志中响应体与请求发起协议保持一致，而非统一序列化为 OpenAI 格式；
// 转换失败或无适配器时回退为内部 OpenAI 格式序列化。
// 返回空字符串表示无可记录内容。
func (m *RelayMetrics) responseContentForLog(ctx context.Context) string {
	if m.AxonResponse != nil {
		if respBytes, err := json.Marshal(m.AxonResponse); err == nil {
			return string(respBytes)
		}
		return ""
	}
	if m.InternalResponse == nil {
		return ""
	}
	respForLog := m.filterResponseForLog(m.InternalResponse)
	var respBytes []byte
	var transformErr error
	if m.InboundAdapter != nil {
		respBytes, transformErr = m.InboundAdapter.TransformResponse(ctx, respForLog)
	}
	if transformErr != nil || len(respBytes) == 0 {
		respBytes, transformErr = json.Marshal(respForLog)
	}
	if transformErr != nil {
		return ""
	}
	return string(respBytes)
}

// filterResponseForLog 创建响应的浅拷贝，过滤掉 images、MultipleContent 中的图片数据和 Audio.Data 以减少存储压力
func (m *RelayMetrics) filterResponseForLog(resp *transformerModel.InternalLLMResponse) *transformerModel.InternalLLMResponse {
	if resp == nil {
		return nil
	}

	filterMsg := func(msg *transformerModel.Message) *transformerModel.Message {
		if msg == nil {
			return nil
		}
		c := *msg
		c.Images = nil
		if len(c.Content.MultipleContent) > 0 {
			parts := make([]transformerModel.MessageContentPart, 0, len(c.Content.MultipleContent))
			for _, p := range c.Content.MultipleContent {
				if p.Type == "image_url" && p.ImageURL != nil {
					parts = append(parts, transformerModel.MessageContentPart{
						Type:     "image_url",
						ImageURL: &transformerModel.ImageURL{URL: "[image data omitted for storage]"},
					})
				} else {
					parts = append(parts, p)
				}
			}
			c.Content = transformerModel.MessageContent{Content: c.Content.Content, MultipleContent: parts}
		}
		if c.Audio != nil && c.Audio.Data != "" {
			a := *c.Audio
			a.Data = "[audio data omitted for storage]"
			c.Audio = &a
		}
		return &c
	}

	filtered := *resp
	filtered.Choices = make([]transformerModel.Choice, len(resp.Choices))
	for i, choice := range resp.Choices {
		filtered.Choices[i] = choice
		filtered.Choices[i].Message = filterMsg(choice.Message)
		filtered.Choices[i].Delta = filterMsg(choice.Delta)
	}
	return &filtered
}
