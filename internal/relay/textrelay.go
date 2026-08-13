package relay

import (
	"fmt"
	"net/http"

	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/axonadapter"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/server/resp"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

// TextHandler 是文本 API 转换迁移阶段 1 的非流式转发入口：
// 使用 axonhub 的 inbound/outbound transformer 完成协议转换，复用本地
// 迭代器（选通道）与渠道 HTTP client（代理/超时）。
//
// 阶段 1 范围约束：仅支持非流式（stream=false）的 openai chat/completions
// 与 anthropic messages；直通、WebSocket、流式、跨协议补偿留待后续阶段。
// 该函数暂未接入生产路由，由 round-trip 测试验证转换闭环。
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

	// 阶段 1 仅支持非流式。
	if llmReq.Stream != nil && *llmReq.Stream {
		resp.Error(c, http.StatusBadRequest, "streaming is not supported by text relay (phase 1)")
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

	var lastErr error
	for iter.Next() {
		item := iter.Item()
		channel, err := op.ChannelGet(item.ChannelID, ctx)
		if err != nil {
			log.Warnf("failed to get channel %d: %v", item.ChannelID, err)
			lastErr = err
			continue
		}
		if !channel.Enabled {
			continue
		}
		usedKey := channel.GetChannelKey()
		if usedKey.ChannelKey == "" {
			continue
		}

		channelType, ok := axonadapter.OutboundTypeToAPIFormat(channel.Type)
		if !ok {
			lastErr = fmt.Errorf("unsupported channel type: %d", channel.Type)
			continue
		}

		outAdapter, err := axonadapter.NewOutbound(channelType, requestType, channel.GetBaseUrl(), usedKey.ChannelKey)
		if err != nil {
			lastErr = err
			continue
		}

		// 每次尝试都把统一请求的模型名改为本次候选的上游模型。
		llmReq.Model = item.ModelName

		outReq, err := outAdapter.TransformRequest(ctx, llmReq)
		if err != nil {
			lastErr = err
			continue
		}

		nativeClient, err := helper.ChannelHTTPClientWithContext(ctx, channel)
		if err != nil {
			lastErr = err
			continue
		}
		httpResp, err := httpclient.NewHttpClientWithClient(nativeClient).Do(ctx, outReq)
		if err != nil {
			lastErr = err
			continue
		}

		// 上游响应 → 统一 llm.Response → 客户端格式。
		llmResp, err := outAdapter.TransformResponse(ctx, httpResp)
		if err != nil {
			lastErr = err
			continue
		}
		outResp, err := inAdapter.TransformResponse(ctx, llmResp)
		if err != nil {
			lastErr = err
			continue
		}

		writeAxonResponse(c, outResp)
		return
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("all channels failed")
	}
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
