package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/bestruirui/octopus/internal/helper"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/transformer"
)

// defaultAnthropicPassthroughBeta 与自研 transformer 的
// DefaultAnthropicPassthroughBeta 保持一致，确保直通请求命中 prompt-caching。
const defaultAnthropicPassthroughBeta = "prompt-caching-2024-07-31,extended-cache-ttl-2025-04-11"

// isAnthropicPassthrough 判断是否可做 anthropic→anthropic 同格式直通。
// 直通保留客户端请求字节（仅改写 model），对 Anthropic prompt caching 至关重要。
func isAnthropicPassthrough(format llm.APIFormat, channelType llm.APIFormat) bool {
	return format == llm.APIFormatAnthropicMessage && channelType == llm.APIFormatAnthropicMessage
}

// passthroughAnthropicNonStream 执行 anthropic 非流式直通：
// 字节稳定改写 model → 直发上游 → 响应体直通写回 → sidecar 解析 usage。
func passthroughAnthropicNonStream(ctx context.Context, channel *dbmodel.Channel, usedKey dbmodel.ChannelKey, rawBody []byte, modelName string, c *gin.Context, outAdapter transformer.Outbound) (int, *llm.Response, error) {
	body, err := rewriteRawRequestModel(rawBody, modelName)
	if err != nil {
		return 0, nil, err
	}

	baseURL := strings.TrimSuffix(channel.GetBaseUrl(), "/") + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("anthropic-beta", defaultAnthropicPassthroughBeta)
	req.Header.Set("X-API-Key", usedKey.ChannelKey)
	copyPassthroughHeaders(c, req, channel)

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
