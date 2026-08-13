package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/outlierwindow"
	"github.com/bestruirui/octopus/internal/relay/axonadapter"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/streams"
	"github.com/looplj/axonhub/llm/transformer"
)

// TextHandler 是文本 API 转换迁移的转发入口（阶段 3）：
// 使用 axonhub 的 inbound/outbound transformer 完成协议转换，复用本地
// 迭代器（选通道）与渠道 HTTP client（代理/超时），支持非流式与流式 SSE，
// 并接入 RelayMetrics（审计日志 + 用量统计）。
//
// 阶段 3 范围约束：支持 openai chat/completions 与 anthropic messages 的
// 非流式 + 流式转发；直通、WebSocket、replay、quality、route-learning 的
// 双 IR 适配留待后续。该函数暂未接入生产路由，由 round-trip 测试验证。
func TextHandler(format llm.APIFormat, c *gin.Context) {
	inAdapter := axonadapter.NewInbound(format)
	if inAdapter == nil {
		resp.Error(c, http.StatusBadRequest, "unsupported inbound format")
		return
	}

	ctx := c.Request.Context()

	// 解析客户端请求为统一 llm.Request。
	httpReq, err := httpclient.ReadHTTPRequest(c.Request)
	if err != nil {
		resp.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	llmReq, err := inAdapter.TransformRequest(ctx, httpReq)
	if err != nil {
		resp.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	if llmReq == nil {
		resp.Error(c, http.StatusInternalServerError, "empty transformed request")
		return
	}

	// APIKey 模型白名单校验（与旧 relay 一致）。
	if supportedModels := c.GetString("supported_models"); supportedModels != "" {
		if !slices.Contains(strings.Split(supportedModels, ","), llmReq.Model) {
			resp.Error(c, http.StatusBadRequest, "model not supported")
			return
		}
	}

	requestType := llmReq.RequestType
	if requestType == "" {
		requestType = llm.RequestTypeChat
	}

	// 分组与迭代器（选通道），与现有 relay 保持一致。
	group, err := op.GroupGetEnabledMap(llmReq.Model, ctx)
	if err != nil {
		resp.Error(c, http.StatusNotFound, "model not found")
		return
	}
	apiKeyID := c.GetInt("api_key_id")
	iter := balancer.NewIterator(group, apiKeyID, llmReq.Model)
	if iter.Len() == 0 {
		resp.Error(c, http.StatusServiceUnavailable, "no available channel")
		return
	}

	// Metrics：复用现有日志/统计链路，InternalRequest 传 nil（文本路径用 AxonResponse）。
	metrics := NewRelayMetrics(apiKeyID, llmReq.Model, httpReq.Body, nil)
	metrics.RequestPath = c.Request.Method + " " + c.Request.URL.Path

	var lastErr error
	for iter.Next() {
		item := iter.Item()
		channel, err := op.ChannelGet(item.ChannelID, ctx)
		if err != nil {
			log.Warnf("failed to get channel %d: %v", item.ChannelID, err)
			iter.Skip(item.ChannelID, 0, fmt.Sprintf("channel_%d", item.ChannelID), "channel not found")
			lastErr = err
			continue
		}
		if !channel.Enabled {
			iter.Skip(channel.ID, 0, channel.Name, "channel disabled")
			continue
		}
		usedKey := channel.GetChannelKey()
		if usedKey.ChannelKey == "" {
			iter.Skip(channel.ID, 0, channel.Name, "no available key")
			continue
		}
		if iter.SkipCircuitBreak(channel.ID, usedKey.ID, channel.Name) {
			continue
		}

		channelType, ok := axonadapter.OutboundTypeToAPIFormat(channel.Type)
		if !ok {
			iter.Skip(channel.ID, usedKey.ID, channel.Name, fmt.Sprintf("unsupported channel type: %d", channel.Type))
			lastErr = fmt.Errorf("unsupported channel type: %d", channel.Type)
			continue
		}

		// 同通道重试次数（与现有 relay 一致）。
		maxRetries := 1
		if group.RetryEnabled {
			maxRetries = group.MaxRetries
			if maxRetries <= 0 {
				maxRetries = 3
			}
		}

		var lastSpan *balancer.AttemptSpan
		var lastStatusCode int
		var lastAttemptErr error

		for retryNum := 0; retryNum < maxRetries; retryNum++ {
			if retryNum > 0 {
				delay := computeBackoff(retryNum, 0)
				select {
				case <-ctx.Done():
					metrics.Save(ctx, false, context.Canceled, iter.Attempts())
					return
				case <-time.After(delay):
				}
			}

			// 每次重试重建 outAdapter，重置流式状态（toolIndex 等）。
			outAdapter, err := axonadapter.NewOutbound(channelType, requestType, channel.GetBaseUrl(), usedKey.ChannelKey)
			if err != nil {
				lastAttemptErr = err
				break
			}

			isStream := llmReq.Stream != nil && *llmReq.Stream

			// 同格式直通（anthropic → anthropic，非流式）：字节稳定转发保 prompt caching。
			if !isStream && isAnthropicPassthrough(format, channelType) {
				span := iter.StartAttempt(channel.ID, usedKey.ID, channel.Name, item.ModelName)
				lastSpan = span
				sc, llmResp, perr := passthroughAnthropicNonStream(ctx, channel, usedKey, httpReq.Body, item.ModelName, c, outAdapter)
				if perr != nil {
					span.End(dbmodel.AttemptFailed, sc, perr.Error())
					recordAxonAttemptFailure(channel, usedKey, span, sc)
					lastStatusCode = sc
					lastAttemptErr = perr
					if !isRetryableStatus(sc) {
						break
					}
					continue
				}
				span.End(dbmodel.AttemptSuccess, sc, "")
				if llmResp != nil {
					metrics.SetAxonResponse(llmResp, item.ModelName, channel.ID)
				} else {
					metrics.ActualModel = item.ModelName
				}
				recordAxonSuccess(group, channel, usedKey, metrics, span, item.ModelName, sc)
				if llmResp != nil && llmResp.Usage != nil {
					if outputTokens := llmResp.Usage.CompletionTokens; isQualityFailureResponseAxon(llmReq, outputTokens) {
						recordQualityFailure(group, channel.ID, usedKey.ID, item.ModelName, outputTokens, span.Duration().Milliseconds(), 0)
					}
				}
				metrics.Save(ctx, true, nil, iter.Attempts())
				return
			}

			// 每次尝试都把统一请求的模型名改为本次候选的上游模型。
			llmReq.Model = item.ModelName

			outReq, err := outAdapter.TransformRequest(ctx, llmReq)
			if err != nil {
				lastAttemptErr = err
				break
			}
			// 渠道级请求定制：参数覆盖（仅 JSON body）+ 自定义 header（敏感头保持转换器已写优先）。
			applyAxonChannelOptions(channel, outReq)
			// 火山（volcengine）渠道：在 responses outbound 之上补齐火山 Responses 特化。
			if channelType == axonadapter.ChannelTypeDoubao {
				applyVolcengineCompensation(outReq, llmReq)
			}

			nativeClient, err := helper.ChannelHTTPClientWithContext(ctx, channel)
			if err != nil {
				lastAttemptErr = err
				break
			}
			httpClient := httpclient.NewHttpClientWithClient(nativeClient)

			span := iter.StartAttempt(channel.ID, usedKey.ID, channel.Name, item.ModelName)
			lastSpan = span

			// 流式：上游 SSE → 统一流 → 客户端格式流 → SSE 写回。
			if llmReq.Stream != nil && *llmReq.Stream {
				usage, ferr := forwardAxonStream(ctx, httpClient, outAdapter, inAdapter, outReq, c, time.Duration(group.FirstTokenTimeOut)*time.Second, streamHeartbeatInterval())
				if ferr != nil {
					sc := axonErrorStatusCode(ferr)
					span.End(dbmodel.AttemptFailed, sc, ferr.Error())
					recordAxonAttemptFailure(channel, usedKey, span, sc)
					lastStatusCode = sc
					lastAttemptErr = ferr
					if errors.Is(ferr, errAxonFirstTokenTimeout) || !isRetryableStatus(sc) {
						break // 首 token 超时切换通道，不重试同一通道
					}
					continue
				}
				span.End(dbmodel.AttemptSuccess, http.StatusOK, "")
				if usage != nil {
					metrics.SetAxonResponse(&llm.Response{Usage: usage, Model: item.ModelName}, item.ModelName, channel.ID)
				} else {
					metrics.ActualModel = item.ModelName
				}
				recordAxonSuccess(group, channel, usedKey, metrics, span, item.ModelName, http.StatusOK)
				metrics.Save(ctx, true, nil, iter.Attempts())
				return
			}

			httpResp, err := httpClient.Do(ctx, outReq)
			if err != nil {
				sc := axonErrorStatusCode(err)
				span.End(dbmodel.AttemptFailed, sc, err.Error())
				recordAxonAttemptFailure(channel, usedKey, span, sc)
				lastStatusCode = sc
				lastAttemptErr = err
				if !isRetryableStatus(sc) {
					break
				}
				continue
			}

			// 上游响应 → 统一 llm.Response → 客户端格式。
			llmResp, err := outAdapter.TransformResponse(ctx, httpResp)
			if err != nil {
				sc := axonErrorStatusCode(err)
				span.End(dbmodel.AttemptFailed, sc, err.Error())
				recordAxonAttemptFailure(channel, usedKey, span, sc)
				lastStatusCode = sc
				lastAttemptErr = err
				if !isRetryableStatus(sc) {
					break
				}
				continue
			}
			outResp, err := inAdapter.TransformResponse(ctx, llmResp)
			if err != nil {
				span.End(dbmodel.AttemptFailed, 0, err.Error())
				recordAxonAttemptFailure(channel, usedKey, span, 0)
				lastAttemptErr = err
				break // 客户端格式转换错误不可重试
			}

			span.End(dbmodel.AttemptSuccess, http.StatusOK, "")
			metrics.SetAxonResponse(llmResp, item.ModelName, channel.ID)

			// 渠道健康闭环：成功样本 + 熔断/粘性/统计，随后质量检测（可能追加失败样本）。
			recordAxonSuccess(group, channel, usedKey, metrics, span, item.ModelName, http.StatusOK)

			// 质量失败检测（工具循环短输出）：与现有 relay 语义一致。
			if llmResp.Usage != nil {
				if outputTokens := llmResp.Usage.CompletionTokens; isQualityFailureResponseAxon(llmReq, outputTokens) {
					recordQualityFailure(group, channel.ID, usedKey.ID, item.ModelName, outputTokens, span.Duration().Milliseconds(), 0)
				}
			}

			metrics.Save(ctx, true, nil, iter.Attempts())
			writeAxonResponse(c, outResp)
			return
		}

		// 同通道重试耗尽：一次性记录渠道级失败（熔断/AutoRank/outlier）。
		if lastAttemptErr != nil {
			if lastSpan != nil {
				recordAxonChannelFailure(group, channel, usedKey, lastSpan, item.ModelName, lastStatusCode)
			}
			maybeLearnManagedRouteAxon(ctx, channel.ID, item.ModelName, format, lastAttemptErr)
			lastErr = lastAttemptErr
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("all channels failed")
	}
	metrics.Save(ctx, false, lastErr, iter.Attempts())
	resp.Error(c, http.StatusBadGateway, lastErr.Error())
}

// applyAxonChannelOptions 在出站请求上应用渠道级定制：参数覆盖 + 自定义 header。
func applyAxonChannelOptions(channel *dbmodel.Channel, outReq *httpclient.Request) {
	// ParamOverride 只覆盖 JSON 请求体（multipart 图片编辑等不能按 map 合并）。
	if channel.ParamOverride != nil && *channel.ParamOverride != "" &&
		strings.Contains(strings.ToLower(outReq.Headers.Get("Content-Type")+" "+outReq.ContentType), "application/json") {
		var bodyMap map[string]any
		if err := json.Unmarshal(outReq.Body, &bodyMap); err == nil {
			var override map[string]any
			if err := json.Unmarshal([]byte(*channel.ParamOverride), &override); err == nil {
				maps.Copy(bodyMap, override)
				if modified, err := json.Marshal(bodyMap); err == nil {
					outReq.Body = modified
				}
			}
		}
	}
	// 自定义 header；敏感头（认证）保持 transformer 已写优先，避免覆盖鉴权。
	for _, h := range channel.CustomHeader {
		if h.HeaderKey == "" {
			continue
		}
		if outReq.Headers.Get(h.HeaderKey) != "" && httpclient.IsSensitiveHeader(h.HeaderKey) {
			continue
		}
		outReq.Headers.Set(h.HeaderKey, h.HeaderValue)
	}
}

// axonErrorStatusCode 从 axonhub httpclient 错误中提取上游 HTTP 状态码。
func axonErrorStatusCode(err error) int {
	var he *httpclient.Error
	if errors.As(err, &he) {
		return he.StatusCode
	}
	return 0
}

// recordAxonSuccess 记录文本路径成功后的渠道健康闭环：
// key 状态/成本、渠道统计、熔断成功、粘性、outlier、AutoRank 样本。
func recordAxonSuccess(group dbmodel.Group, channel *dbmodel.Channel, usedKey dbmodel.ChannelKey, metrics *RelayMetrics, span *balancer.AttemptSpan, modelName string, statusCode int) {
	usedKey.TotalCost += metrics.Stats.InputCost + metrics.Stats.OutputCost
	usedKey.StatusCode = statusCode
	usedKey.LastUseTimeStamp = time.Now().Unix()
	_ = op.ChannelKeyUpdate(usedKey)

	op.StatsChannelUpdate(channel.ID, dbmodel.StatsMetrics{
		WaitTime:       span.Duration().Milliseconds(),
		RequestSuccess: 1,
	})
	balancer.RecordSuccess(channel.ID, usedKey.ID, modelName)
	balancer.SetSticky(metrics.APIKeyID, metrics.RequestModel, channel.ID, usedKey.ID)
	outlierwindow.Report(channel.ID, true, statusCode, time.Now())
	recordAutoRankResult(group, channel.ID, modelName, true, statusCode, span.Duration().Milliseconds(), 0)
}

// recordAxonAttemptFailure 记录单次尝试失败（轻量）：key 状态 + 渠道统计。
// 重试期间多次调用不叠加熔断，避免过早触发。
func recordAxonAttemptFailure(channel *dbmodel.Channel, usedKey dbmodel.ChannelKey, span *balancer.AttemptSpan, statusCode int) {
	usedKey.StatusCode = statusCode
	usedKey.LastUseTimeStamp = time.Now().Unix()
	_ = op.ChannelKeyUpdate(usedKey)

	op.StatsChannelUpdate(channel.ID, dbmodel.StatsMetrics{
		WaitTime:      span.Duration().Milliseconds(),
		RequestFailed: 1,
	})
}

// recordAxonChannelFailure 记录渠道级失败（重试耗尽后一次性）：熔断、AutoRank、outlier。
func recordAxonChannelFailure(group dbmodel.Group, channel *dbmodel.Channel, usedKey dbmodel.ChannelKey, span *balancer.AttemptSpan, modelName string, statusCode int) {
	failureKind := circuitFailureKind(group.RetryEnabled, statusCode)
	balancer.RecordFailure(channel.ID, usedKey.ID, modelName, failureKind)
	recordAutoRankResult(group, channel.ID, modelName, false, statusCode, span.Duration().Milliseconds(), 0)
	outlierwindow.Report(channel.ID, false, statusCode, time.Now())
	if balancer.IsChannelLevelFailure(statusCode) {
		balancer.RecordChannelFailure(channel.ID, failureKind)
	}
}

// writeAxonResponse 将 axonhub 归一化响应写回 gin 客户端。
func writeAxonResponse(c *gin.Context, out *httpclient.Response) {
	if out == nil {
		resp.Error(c, http.StatusInternalServerError, "empty response")
		return
	}
	for k, vs := range out.Headers {
		for _, v := range vs {
			c.Header(k, v)
		}
	}
	contentType := out.Headers.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	statusCode := out.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	c.Data(statusCode, contentType, out.Body)
}

// errAxonFirstTokenTimeout 标记首 token 超时，供 TextHandler 识别并切换通道（而非同通道重试）。
var errAxonFirstTokenTimeout = errors.New("first token timeout")

// forwardAxonStream 处理流式转发：上游 SSE → 统一流 → 客户端格式流 → SSE 写回，
// 返回聚合后的 usage（聚合失败时返回 nil，不阻断转发）。
func forwardAxonStream(ctx context.Context, httpClient *httpclient.HttpClient, outAdapter transformer.Outbound, inAdapter transformer.Inbound, outReq *httpclient.Request, c *gin.Context, firstTokenTimeout, heartbeatInterval time.Duration) (*llm.Usage, error) {
	upstreamStream, err := httpClient.DoStream(ctx, outReq)
	if err != nil {
		return nil, err
	}
	llmStream, err := outAdapter.TransformStream(ctx, outReq, upstreamStream)
	if err != nil {
		_ = upstreamStream.Close()
		return nil, err
	}
	clientStream, err := inAdapter.TransformStream(ctx, llmStream)
	if err != nil {
		_ = llmStream.Close()
		return nil, err
	}
	return writeAxonStream(c, clientStream, inAdapter, firstTokenTimeout, heartbeatInterval)
}

// writeAxonStream 消费客户端格式流并逐事件写回 SSE。并发支持：
//   - 首 token 超时（firstTokenTimeout > 0）：第一个事件到达前超时则中断流，返回 errAxonFirstTokenTimeout；
//   - 心跳（heartbeatInterval > 0）：无事件时定期写 SSE 注释字节（":\n\n"）保活。
//
// 流正常结束后用 AggregateStreamChunks 聚合 usage。
func writeAxonStream(c *gin.Context, clientStream streams.Stream[*httpclient.StreamEvent], inAdapter transformer.Inbound, firstTokenTimeout, heartbeatInterval time.Duration) (*llm.Usage, error) {
	if clientStream == nil {
		return nil, fmt.Errorf("empty stream")
	}
	defer clientStream.Close()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	type streamResult struct {
		event *httpclient.StreamEvent
		err   error
	}
	results := make(chan streamResult, 1)
	done := make(chan struct{})
	defer close(done)

	go func() {
		defer close(results)
		for clientStream.Next() {
			ev := clientStream.Current()
			select {
			case results <- streamResult{event: ev}:
			case <-done:
				return
			}
		}
		if err := clientStream.Err(); err != nil {
			select {
			case results <- streamResult{err: err}:
			case <-done:
			}
		}
	}()

	var firstTokenTimer *time.Timer
	var firstTokenC <-chan time.Time
	if firstTokenTimeout > 0 {
		firstTokenTimer = time.NewTimer(firstTokenTimeout)
		firstTokenC = firstTokenTimer.C
		defer firstTokenTimer.Stop()
	}

	var heartbeatTicker *time.Ticker
	var heartbeatC <-chan time.Time
	if heartbeatInterval > 0 {
		heartbeatTicker = time.NewTicker(heartbeatInterval)
		heartbeatC = heartbeatTicker.C
		defer heartbeatTicker.Stop()
	}

	events := make([]*httpclient.StreamEvent, 0, 8)
	firstToken := true

	for {
		select {
		case <-firstTokenC:
			return nil, fmt.Errorf("%w (%s)", errAxonFirstTokenTimeout, firstTokenTimeout)
		case <-heartbeatC:
			if _, err := c.Writer.Write([]byte(":\n\n")); err != nil {
				return nil, err
			}
			c.Writer.Flush()
		case <-c.Request.Context().Done():
			return nil, c.Request.Context().Err()
		case r, ok := <-results:
			if !ok {
				if len(events) > 0 && inAdapter != nil {
					_, meta, err := inAdapter.AggregateStreamChunks(context.Background(), events)
					if err == nil {
						return meta.Usage, nil
					}
				}
				return nil, nil
			}
			if r.err != nil {
				return nil, r.err
			}
			event := r.event
			if event == nil || len(event.Data) == 0 {
				continue
			}
			events = append(events, event)
			if firstToken {
				firstToken = false
				if firstTokenTimer != nil {
					firstTokenTimer.Stop()
				}
			}
			writeAxonSSEEvent(c, event)
			c.Writer.Flush()
		}
	}
}

// writeAxonSSEEvent 写单个 SSE 事件。
func writeAxonSSEEvent(c *gin.Context, event *httpclient.StreamEvent) {
	if event.Type != "" {
		_, _ = fmt.Fprintf(c.Writer, "event: %s\n", event.Type)
	}
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", event.Data)
}
