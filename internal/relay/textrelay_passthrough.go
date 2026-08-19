package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/bestruirui/octopus/internal/helper"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer"
)

// defaultAnthropicPassthroughBeta 与自研 transformer 的
// DefaultAnthropicPassthroughBeta 保持一致，确保直通请求命中 prompt-caching。
const defaultAnthropicPassthroughBeta = "prompt-caching-2024-07-31,extended-cache-ttl-2025-04-11"

// volcengineReasoningModels 是火山 Responses 端点支持 reasoning_effort 的模型白名单（对齐自研 transformer）。
var volcengineReasoningModels = map[string]bool{
	"doubao-seed-1-8-251228":      true,
	"doubao-seed-1-6-lite-251015": true,
	"doubao-seed-1-6-251015":      true,
}

// applyVolcengineCompensation 在 axonhub responses outbound 生成的请求体上补齐
// 火山（volcengine/doubao）Responses 特化（对齐自研 volcengine.ResponseOutbound）：
//   - metadata 置空（火山不支持）
//   - thinking 字段：minimal→disabled，low/medium/high→enabled
//   - input 为数组时，最后一条 assistant 设 partial（续写语义）
//   - reasoning 白名单：非白名单模型清空 reasoning
func applyVolcengineCompensation(outReq *httpclient.Request, llmReq *llm.Request) {
	var bodyMap map[string]any
	if err := json.Unmarshal(outReq.Body, &bodyMap); err != nil {
		return
	}

	delete(bodyMap, "metadata")

	switch llmReq.ReasoningEffort {
	case "minimal":
		bodyMap["thinking"] = map[string]any{"type": "disabled"}
	case "low", "medium", "high":
		bodyMap["thinking"] = map[string]any{"type": "enabled"}
	}

	if input, ok := bodyMap["input"].([]any); ok && len(input) > 0 {
		if last, ok := input[len(input)-1].(map[string]any); ok && last["role"] == "assistant" {
			last["partial"] = true
		}
	}

	if !volcengineReasoningModels[llmReq.Model] {
		delete(bodyMap, "reasoning")
	}

	if modified, err := json.Marshal(bodyMap); err == nil {
		outReq.Body = modified
	}
}

// isAnthropicPassthrough 判断是否可做 anthropic→anthropic 同格式直通。
// 直通保留客户端请求字节（仅改写 model），对 Anthropic prompt caching 至关重要。
func isAnthropicPassthrough(format llm.APIFormat, channelType llm.APIFormat) bool {
	return format == llm.APIFormatAnthropicMessage && channelType == llm.APIFormatAnthropicMessage
}

// openAIRawPassthroughPaths 定义 OpenAI 系同格式直通的出站端点。
// 与 axonhub 各 outbound transformer 的 buildFullRequestURL 保持一致。
var openAIRawPassthroughPaths = map[llm.APIFormat]string{
	llm.APIFormatOpenAIChatCompletion: "/chat/completions",
	llm.APIFormatOpenAIResponse:       "/responses",
	llm.APIFormatOpenAIEmbedding:      "/embeddings",
}

// isOpenAIRawPassthrough 判断 OpenAI 系请求是否可做同格式直通。
// Codex / 各 OpenAI 兼容客户端可能携带统一模型无法表示的原生扩展字段，
// 同格式路径保留原始请求字节（仅重写顶层 model）比 round-trip 更保真。
func isOpenAIRawPassthrough(format llm.APIFormat, channelType llm.APIFormat) bool {
	if format != channelType {
		return false
	}
	_, ok := openAIRawPassthroughPaths[format]
	return ok
}

// buildOpenAIRawPassthroughRequest 构造 OpenAI 系同格式直通出站请求。
// 处理顺序与 axonhub pipeline.processRequest 对齐：
// 生成器请求 → 合并客户端原始请求（Codex 协商头）→ 固化鉴权 → 渠道覆盖。
func buildOpenAIRawPassthroughRequest(ctx context.Context, inReq *httpclient.Request, channel *dbmodel.Channel, usedKey dbmodel.ChannelKey, modelName string, stream bool, format llm.APIFormat) (*httpclient.Request, error) {
	endpoint, ok := openAIRawPassthroughPaths[format]
	if !ok {
		return nil, fmt.Errorf("unsupported openai passthrough format: %s", format)
	}
	if inReq == nil || len(inReq.Body) == 0 {
		return nil, fmt.Errorf("raw request body is empty")
	}

	body, err := rewriteRawRequestModel(inReq.Body, modelName)
	if err != nil {
		return nil, err
	}

	outReq := &httpclient.Request{
		Method:                http.MethodPost,
		URL:                   strings.TrimSuffix(channel.GetBaseUrl(), "/") + endpoint,
		Headers:               make(http.Header),
		Body:                  body,
		Auth:                  &httpclient.AuthConfig{Type: httpclient.AuthTypeBearer, APIKey: usedKey.ChannelKey},
		APIFormat:             string(format),
		SkipInboundQueryMerge: format == llm.APIFormatOpenAIResponse,
	}
	outReq.Headers.Set("Content-Type", "application/json")
	if stream {
		outReq.Headers.Set("Accept", "text/event-stream")
	} else {
		outReq.Headers.Set("Accept", "application/json")
	}

	outReq, err = prepareAxonOutboundRequest(outReq, inReq, channel)
	if err != nil {
		return nil, err
	}

	return outReq, nil
}

// passthroughOpenAINonStream 执行 OpenAI 系非流式同格式直通：
// 原始响应字节写回客户端，sidecar 用同格式 outbound 解析 usage/metrics。
func passthroughOpenAINonStream(ctx context.Context, httpClient *httpclient.HttpClient, outAdapter transformer.Outbound, outReq *httpclient.Request, c *gin.Context) (int, *llm.Response, error) {
	resp, err := httpClient.Do(ctx, outReq)
	if err != nil {
		return axonErrorStatusCode(err), nil, err
	}

	contentType := resp.Headers.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(http.StatusOK, contentType, resp.Body)

	// sidecar：直通已经把字节写回客户端，这里再解析 usage 供 metrics。
	llmResp, err := outAdapter.TransformResponse(ctx, resp)
	if err != nil {
		return resp.StatusCode, nil, nil
	}
	return resp.StatusCode, llmResp, nil
}

// passthroughOpenAIStream 执行 OpenAI 系流式同格式直通：
// 上游 SSE 事件按原始类型/data 直接写回客户端，结束前 sidecar 聚合 usage。
func passthroughOpenAIStream(ctx context.Context, httpClient *httpclient.HttpClient, inAdapter transformer.Inbound, outReq *httpclient.Request, c *gin.Context, firstTokenTimeout, heartbeatInterval time.Duration) (*llm.Usage, error) {
	stream, err := httpClient.DoStream(ctx, outReq)
	if err != nil {
		return nil, err
	}
	return writeAxonStream(c, stream, inAdapter, firstTokenTimeout, heartbeatInterval)
}

// buildPassthroughRequest 构造 anthropic 直通出站请求：字节改写 model + 参数覆盖 + 出站头。
// 同时返回最终出站字节，供失败详情落盘（req.Body 是一次性 reader，事后不便再取）。
func buildPassthroughRequest(ctx context.Context, channel *dbmodel.Channel, usedKey dbmodel.ChannelKey, rawBody []byte, modelName string, c *gin.Context) (*http.Request, []byte, error) {
	body, err := rewriteRawRequestModel(rawBody, modelName)
	if err != nil {
		return nil, nil, err
	}
	// 渠道参数覆盖（对齐本地 forwardViaHTTPPassthrough；显式配置覆盖时接受字节重排）。
	if channel.ParamOverride != nil && strings.TrimSpace(*channel.ParamOverride) != "" {
		var bodyMap map[string]any
		if json.Unmarshal(body, &bodyMap) == nil {
			var override map[string]any
			if json.Unmarshal([]byte(*channel.ParamOverride), &override) == nil {
				maps.Copy(bodyMap, override)
				if modified, err := json.Marshal(bodyMap); err == nil {
					body = modified
				}
			}
		}
	}

	baseURL := strings.TrimSuffix(channel.GetBaseUrl(), "/") + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("anthropic-beta", defaultAnthropicPassthroughBeta)
	req.Header.Set("X-API-Key", usedKey.ChannelKey)
	copyPassthroughHeaders(c, req, channel)
	return req, body, nil
}

// recordAnthropicPassthroughFailure 记录 anthropic 直通失败详情：出站体为改写
// model 后的客户端原始字节，出站头为直通构造的 header（脱敏由记录层负责）。
// 只在失败分支调用，与标准路径口径一致。
func recordAnthropicPassthroughFailure(span *balancer.AttemptSpan, req *http.Request, outBody, respBody []byte) {
	recordSpanRequestBody(span, outBody)
	if req != nil {
		recordSpanOutboundHeaders(span, req.Header)
	}
	recordSpanResponseBody(span, respBody)
}

// passthroughAnthropicNonStream 执行 anthropic 非流式直通：
// 字节稳定改写 model → 直发上游 → 响应体直通写回 → sidecar 解析 usage。
func passthroughAnthropicNonStream(ctx context.Context, channel *dbmodel.Channel, usedKey dbmodel.ChannelKey, rawBody []byte, modelName string, c *gin.Context, outAdapter transformer.Outbound, span *balancer.AttemptSpan) (int, *llm.Response, error) {
	req, outBody, err := buildPassthroughRequest(ctx, channel, usedKey, rawBody, modelName, c)
	if err != nil {
		return 0, nil, err
	}

	nativeClient, err := helper.ChannelHTTPClientWithContext(ctx, channel)
	if err != nil {
		return 0, nil, err
	}
	resp, err := nativeClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		recordAnthropicPassthroughFailure(span, req, outBody, errBody)
		return resp.StatusCode, nil, fmt.Errorf("upstream error: %d: %s", resp.StatusCode, truncateBodyForMessage(errBody))
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(http.StatusOK, contentType, respBody)

	// sidecar：直通已把字节写回客户端，这里再用 axonhub transformer 解析 usage 供 metrics。
	sidecar := &httpclient.Response{StatusCode: resp.StatusCode, Headers: resp.Header, Body: respBody}
	llmResp, err := outAdapter.TransformResponse(ctx, sidecar)
	if err != nil {
		return resp.StatusCode, nil, nil
	}
	return resp.StatusCode, llmResp, nil
}

// passthroughAnthropicStream 执行 anthropic 流式直通：
// 字节改写 model → 流式发送 → SSE 字节透传写回 → sidecar 聚合 usage。
func passthroughAnthropicStream(ctx context.Context, channel *dbmodel.Channel, usedKey dbmodel.ChannelKey, rawBody []byte, modelName string, c *gin.Context, inAdapter transformer.Inbound, span *balancer.AttemptSpan) (*llm.Usage, error) {
	req, outBody, err := buildPassthroughRequest(ctx, channel, usedKey, rawBody, modelName, c)
	if err != nil {
		return nil, err
	}

	nativeClient, err := helper.ChannelHTTPClientWithContext(ctx, channel)
	if err != nil {
		return nil, err
	}
	resp, err := nativeClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		recordAnthropicPassthroughFailure(span, req, outBody, errBody)
		return nil, fmt.Errorf("upstream error: %d: %s", resp.StatusCode, truncateBodyForMessage(errBody))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "text/event-stream") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		recordAnthropicPassthroughFailure(span, req, outBody, body)
		return nil, fmt.Errorf("upstream returned non-SSE content-type %q: %s", ct, string(body))
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	// 透传 SSE 字节 + 缓冲原始流供 sidecar 聚合。
	var rawBuf bytes.Buffer
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			rawBuf.Write(buf[:n])
			if _, werr := c.Writer.Write(buf[:n]); werr != nil {
				return nil, werr
			}
			c.Writer.Flush()
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}

	// sidecar 聚合 usage（非关键路径）。
	if inAdapter != nil && rawBuf.Len() > 0 {
		decoder := httpclient.NewDefaultSSEDecoder(ctx, io.NopCloser(bytes.NewReader(rawBuf.Bytes())))
		var events []*httpclient.StreamEvent
		for decoder.Next() {
			events = append(events, decoder.Current())
		}
		if len(events) > 0 {
			_, meta, err := inAdapter.AggregateStreamChunks(ctx, events)
			if err == nil {
				return meta.Usage, nil
			}
		}
	}
	return nil, nil
}

// copyPassthroughHeaders 复制客户端 header（过滤 hop-by-hop）并叠加渠道自定义 header。
// 敏感头（X-API-Key/Authorization）由直通构造逻辑预置，客户端无法覆盖。
func copyPassthroughHeaders(c *gin.Context, req *http.Request, channel *dbmodel.Channel) {
	if c != nil {
		for key, values := range c.Request.Header {
			if hopByHopHeaders[strings.ToLower(key)] {
				continue
			}
			for _, value := range values {
				req.Header.Set(key, value)
			}
		}
	}
	for _, h := range channel.CustomHeader {
		if h.HeaderKey != "" {
			req.Header.Set(h.HeaderKey, h.HeaderValue)
		}
	}
}

// rewriteRawRequestModel 字节级改写顶层 model 字段（仅替换值，保持其余字节不变）。
func rewriteRawRequestModel(rawBody []byte, modelName string) ([]byte, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return rawBody, nil
	}
	valueStart, valueEnd, ok := findTopLevelStringField(rawBody, "model")
	if ok {
		return replaceRawStringField(rawBody, valueStart, valueEnd, modelName), nil
	}
	// 顶层无 model 字段时回退整包重编码（理论不该发生）。
	var payload map[string]any
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		return nil, fmt.Errorf("failed to decode raw request: %w", err)
	}
	payload["model"] = modelName
	return json.Marshal(payload)
}

// replaceRawStringField 替换原始字节中指定 value 范围（含首尾引号）。
func replaceRawStringField(rawBody []byte, valueStart, valueEnd int, newValue string) []byte {
	encoded, err := json.Marshal(newValue)
	if err != nil {
		return rawBody
	}
	result := make([]byte, 0, len(rawBody)-(valueEnd-valueStart)+len(encoded))
	result = append(result, rawBody[:valueStart]...)
	result = append(result, encoded...)
	result = append(result, rawBody[valueEnd:]...)
	return result
}

// findTopLevelStringField 定位顶层对象中指定字符串字段的 value 起止 offset。
func findTopLevelStringField(raw []byte, field string) (int, int, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return 0, 0, false
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return 0, 0, false
	}

	depth := 1
	expectKey := true
	var currentKey string

	for dec.More() || depth > 0 {
		tok, err = dec.Token()
		if err != nil {
			return 0, 0, false
		}

		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				depth++
				expectKey = false
			case '}', ']':
				depth--
				if depth == 0 {
					return 0, 0, false
				}
				expectKey = depth == 1
			}
		case string:
			if depth == 1 && expectKey {
				currentKey = v
				expectKey = false
			} else {
				if depth == 1 && currentKey == field {
					valueEnd := int(dec.InputOffset())
					valueStart := findPrecedingStringStart(raw, valueEnd-1)
					if valueStart >= 0 {
						return valueStart, valueEnd, true
					}
					return 0, 0, false
				}
				if depth == 1 {
					expectKey = true
				}
			}
		default:
			if depth == 1 {
				expectKey = true
			}
		}
	}
	return 0, 0, false
}

// findPrecedingStringStart 从闭合引号向前定位字符串值的起始引号（含转义处理）。
func findPrecedingStringStart(raw []byte, closingQuoteIdx int) int {
	if closingQuoteIdx < 0 || closingQuoteIdx >= len(raw) || raw[closingQuoteIdx] != '"' {
		return -1
	}
	for i := closingQuoteIdx - 1; i >= 0; i-- {
		if raw[i] == '"' {
			backslashes := 0
			for j := i - 1; j >= 0 && raw[j] == '\\'; j-- {
				backslashes++
			}
			if backslashes%2 == 0 {
				return i
			}
		}
	}
	return -1
}
