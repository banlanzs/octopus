// Package axonadapter 提供 octopus 文本转发路径接入 axonhub/llm 所需的适配层：
//
//   - 本地数字渠道类型 (outbound.OutboundType) 与 axonhub 渠道类型标识 (llm.APIFormat) 的双向映射；
//   - 入口/出口 transformer 工厂（newInbound / newOutbound），收敛请求类型 × 渠道类型的兼容性判定。
//
// 本次迁移为「保守核心替换」：仅文本 HTTP 路径（chat/completions、messages、responses、embeddings）
// 使用本包；WebSocket、responses/compact、图片编辑、replay、route-learning 仍走自研 transformer。
//
// 关键取舍（相对 97d6a04 的差异）：
//   - Channel.Type 仍为数字枚举，运行时经 OutboundTypeToAPIFormat 映射，不触发 DB 字符串化迁移；
//   - doubao（火山引擎）在 llm 包中无独立 APIFormat 常量，沿用自定义 ChannelTypeDoubao 标识路由，
//     doubao transformer 对外暴露 openai/chat_completions 格式，但其行为包含火山特化处理。
package axonadapter

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/transformer/outbound"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/transformer"
	"github.com/looplj/axonhub/llm/transformer/anthropic"
	"github.com/looplj/axonhub/llm/transformer/doubao"
	"github.com/looplj/axonhub/llm/transformer/gemini"
	"github.com/looplj/axonhub/llm/transformer/openai"
	"github.com/looplj/axonhub/llm/transformer/openai/responses"
)

// ChannelTypeDoubao 标识本地 volcengine 渠道在 axonhub 侧的渠道类型。
// llm 包未为 doubao 定义 APIFormat 常量（doubao transformer 对外暴露
// openai/chat_completions 格式），此处自定义常量用于 NewOutbound 路由。
const ChannelTypeDoubao llm.APIFormat = "doubao"

// OutboundTypeToAPIFormat 将本地数字渠道类型映射为 axonhub 渠道类型标识。
// 返回的 ok 为 false 表示该渠道类型不在文本路径支持范围内。
func OutboundTypeToAPIFormat(t outbound.OutboundType) (llm.APIFormat, bool) {
	switch t {
	case outbound.OutboundTypeOpenAIChat:
		return llm.APIFormatOpenAIChatCompletion, true
	case outbound.OutboundTypeOpenAIResponse:
		return llm.APIFormatOpenAIResponse, true
	case outbound.OutboundTypeAnthropic:
		return llm.APIFormatAnthropicMessage, true
	case outbound.OutboundTypeGemini:
		return llm.APIFormatGeminiContents, true
	case outbound.OutboundTypeVolcengine:
		return ChannelTypeDoubao, true
	case outbound.OutboundTypeOpenAIEmbedding:
		return llm.APIFormatOpenAIEmbedding, true
	default:
		return "", false
	}
}

// NewInbound 按入口 API 格式返回 axonhub inbound transformer；不支持时返回 nil。
func NewInbound(format llm.APIFormat) transformer.Inbound {
	switch format {
	case llm.APIFormatOpenAIChatCompletion:
		return openai.NewInboundTransformer()
	case llm.APIFormatOpenAIResponse:
		return responses.NewInboundTransformer()
	case llm.APIFormatOpenAIEmbedding:
		return openai.NewEmbeddingInboundTransformer()
	case llm.APIFormatAnthropicMessage:
		return anthropic.NewInboundTransformer()
	default:
		return nil
	}
}

// NewOutbound 按请求类型 × 渠道类型返回 axonhub outbound transformer。
// 兼容性判定收敛于此（不再使用本地 IsChatChannelType/IsEmbeddingChannelType 二次拦截），
// 使 axonhub 已支持的组合（如 Gemini/Doubao 的 embedding）不被本地旧判断挡住。
func NewOutbound(channelType llm.APIFormat, requestType llm.RequestType, baseURL, key string) (transformer.Outbound, error) {
	switch requestType {
	case llm.RequestTypeEmbedding:
		switch channelType {
		case llm.APIFormatOpenAIChatCompletion, llm.APIFormatOpenAIResponse, llm.APIFormatOpenAIEmbedding:
			return openai.NewOutboundTransformer(baseURL, key)
		case llm.APIFormatGeminiContents:
			return gemini.NewOutboundTransformer(baseURL, key)
		case ChannelTypeDoubao:
			return doubao.NewOutboundTransformer(baseURL, key)
		default:
			return nil, fmt.Errorf("channel type %s is not compatible with %s request", channelType, requestType)
		}
	case llm.RequestTypeChat:
		switch channelType {
		case llm.APIFormatOpenAIChatCompletion:
			return openai.NewOutboundTransformer(baseURL, key)
		case llm.APIFormatOpenAIResponse:
			return responses.NewOutboundTransformer(baseURL, key)
		case llm.APIFormatAnthropicMessage:
			return anthropic.NewOutboundTransformer(baseURL, key)
		case llm.APIFormatGeminiContents:
			return gemini.NewOutboundTransformer(baseURL, key)
		case ChannelTypeDoubao:
			return doubao.NewOutboundTransformer(baseURL, key)
		default:
			return nil, fmt.Errorf("channel type %s is not compatible with %s request", channelType, requestType)
		}
	default:
		return nil, fmt.Errorf("request type %s is not supported by text relay", requestType)
	}
}
