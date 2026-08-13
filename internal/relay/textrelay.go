package relay

import (
	"context"
	"fmt"
	"net/http"

	"github.com/bestruirui/octopus/internal/helper"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
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

		outAdapter, err := axonadapter.NewOutbound(channelType, requestType, channel.GetBaseUrl(), usedKey.ChannelKey)
		if err != nil {
			iter.Skip(channel.ID, usedKey.ID, channel.Name, err.Error())
			lastErr = err
			continue
		}

		// 每次尝试都把统一请求的模型名改为本次候选的上游模型。
		llmReq.Model = item.ModelName

		outReq, err := outAdapter.TransformRequest(ctx, llmReq)
		if err != nil {
			iter.Skip(channel.ID, usedKey.ID, channel.Name, err.Error())
			lastErr = err
			continue
		}

		nativeClient, err := helper.ChannelHTTPClientWithContext(ctx, channel)
		if err != nil {
			iter.Skip(channel.ID, usedKey.ID, channel.Name, err.Error())
			lastErr = err
			continue
		}
		httpClient := httpclient.NewHttpClientWithClient(nativeClient)

		span := iter.StartAttempt(channel.ID, usedKey.ID, channel.Name, item.ModelName)

		// 流式：上游 SSE → 统一流 → 客户端格式流 → SSE 写回。
		if llmReq.Stream != nil && *llmReq.Stream {
			usage, ferr := forwardAxonStream(ctx, httpClient, outAdapter, inAdapter, outReq, c)
			if ferr != nil {
				span.End(dbmodel.AttemptFailed, 0, ferr.Error())
				lastErr = ferr
				continue
			}
			span.End(dbmodel.AttemptSuccess, http.StatusOK, "")
			if usage != nil {
				metrics.SetAxonResponse(&llm.Response{Usage: usage, Model: item.ModelName}, item.ModelName, channel.ID)
			} else {
				metrics.ActualModel = item.ModelName
			}
			metrics.Save(ctx, true, nil, iter.Attempts())
			return
		}

		httpResp, err := httpClient.Do(ctx, outReq)
		if err != nil {
			span.End(dbmodel.AttemptFailed, 0, err.Error())
			maybeLearnManagedRouteAxon(ctx, channel.ID, item.ModelName, format, err)
			lastErr = err
			continue
		}

		// 上游响应 → 统一 llm.Response → 客户端格式。
		llmResp, err := outAdapter.TransformResponse(ctx, httpResp)
		if err != nil {
			span.End(dbmodel.AttemptFailed, 0, err.Error())
			maybeLearnManagedRouteAxon(ctx, channel.ID, item.ModelName, format, err)
			lastErr = err
			continue
		}
		outResp, err := inAdapter.TransformResponse(ctx, llmResp)
		if err != nil {
			span.End(dbmodel.AttemptFailed, 0, err.Error())
			lastErr = err
			continue
		}

		span.End(dbmodel.AttemptSuccess, http.StatusOK, "")
		metrics.SetAxonResponse(llmResp, item.ModelName, channel.ID)

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

	if lastErr == nil {
		lastErr = fmt.Errorf("all channels failed")
	}
	metrics.Save(ctx, false, lastErr, iter.Attempts())
	resp.Error(c, http.StatusBadGateway, lastErr.Error())
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

// forwardAxonStream 处理流式转发：上游 SSE → 统一流 → 客户端格式流 → SSE 写回，
// 返回聚合后的 usage（聚合失败时返回 nil，不阻断转发）。
func forwardAxonStream(ctx context.Context, httpClient *httpclient.HttpClient, outAdapter transformer.Outbound, inAdapter transformer.Inbound, outReq *httpclient.Request, c *gin.Context) (*llm.Usage, error) {
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
	return writeAxonStream(c, clientStream, inAdapter)
}

// writeAxonStream 消费客户端格式流并逐事件写回 SSE，结束后聚合 usage。
func writeAxonStream(c *gin.Context, clientStream streams.Stream[*httpclient.StreamEvent], inAdapter transformer.Inbound) (*llm.Usage, error) {
	if clientStream == nil {
		return nil, fmt.Errorf("empty stream")
	}
	defer clientStream.Close()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	events := make([]*httpclient.StreamEvent, 0, 8)
	for clientStream.Next() {
		event := clientStream.Current()
		if event == nil || len(event.Data) == 0 {
			continue
		}
		events = append(events, event)
		writeAxonSSEEvent(c, event)
		c.Writer.Flush()
	}
	if err := clientStream.Err(); err != nil {
		return nil, err
	}

	// 聚合流式 chunk 得到 usage（非关键路径，失败仅降级为无 usage）。
	if len(events) > 0 && inAdapter != nil {
		_, meta, err := inAdapter.AggregateStreamChunks(context.Background(), events)
		if err == nil {
			return meta.Usage, nil
		}
	}
	return nil, nil
}

// writeAxonSSEEvent 写单个 SSE 事件。
func writeAxonSSEEvent(c *gin.Context, event *httpclient.StreamEvent) {
	if event.Type != "" {
		_, _ = fmt.Fprintf(c.Writer, "event: %s\n", event.Type)
	}
	_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", event.Data)
}
