package outbound

import (
	"github.com/bestruirui/octopus/internal/transformer/model"
	outAnthropic "github.com/bestruirui/octopus/internal/transformer/outbound/anthropic"
	"github.com/bestruirui/octopus/internal/transformer/outbound/gemini"
	"github.com/bestruirui/octopus/internal/transformer/outbound/openai"
	"github.com/bestruirui/octopus/internal/transformer/outbound/volcengine"
)

type OutboundType int

const (
	OutboundTypeOpenAIChat OutboundType = iota
	OutboundTypeOpenAIResponse
	OutboundTypeAnthropic
	OutboundTypeGemini
	OutboundTypeVolcengine
	OutboundTypeOpenAIEmbedding
	// OutboundTypeAuto 自动检测渠道协议：运行期按客户端请求协议解析出站协议
	// （见 relay/protocol_detect.go）。不是具体协议，不入 outboundFactories——
	// 所有使用方必须先 ResolveOutboundType 再取适配器。
	OutboundTypeAuto
)

// EmbeddingChannelTypes 定义支持 embedding 请求的 channel 类型集合
var EmbeddingChannelTypes = map[OutboundType]bool{
	OutboundTypeOpenAIEmbedding: true,
}

// ChatChannelTypes 定义支持 chat 请求的 channel 类型集合
var ChatChannelTypes = map[OutboundType]bool{
	OutboundTypeOpenAIChat:     true,
	OutboundTypeOpenAIResponse: true,
	OutboundTypeAnthropic:      true,
	OutboundTypeGemini:         true,
	OutboundTypeVolcengine:     true,
}

// IsEmbeddingChannelType 判断 channel 类型是否支持 embedding 请求
func IsEmbeddingChannelType(channelType OutboundType) bool {
	return EmbeddingChannelTypes[channelType]
}

// IsChatChannelType 判断 channel 类型是否支持 chat 请求
func IsChatChannelType(channelType OutboundType) bool {
	return ChatChannelTypes[channelType]
}

// IsAutoType 判断渠道类型是否为「自动检测」。
// 自动检测渠道不声明固定协议，出站协议由客户端请求协议在运行期解析。
func IsAutoType(channelType OutboundType) bool {
	return channelType == OutboundTypeAuto
}

// ResolveOutboundType 解析渠道实际使用的出站协议：
//   - 非自动渠道：原样返回 channelType（权威声明）；
//   - 自动渠道：按客户端请求协议（clientFormat）映射出站协议；
//     Anthropic messages → Anthropic，chat/completions → OpenAIChat，
//     responses → OpenAIResponse，embeddings → OpenAIEmbedding。
//
// 返回 false 表示无法解析（未知格式），调用方应跳过该渠道。
func ResolveOutboundType(channelType OutboundType, clientFormat model.APIFormat) (OutboundType, bool) {
	if !IsAutoType(channelType) {
		return channelType, true
	}
	switch clientFormat {
	case model.APIFormatAnthropicMessage:
		return OutboundTypeAnthropic, true
	case model.APIFormatOpenAIChatCompletion:
		return OutboundTypeOpenAIChat, true
	case model.APIFormatOpenAIResponse:
		return OutboundTypeOpenAIResponse, true
	case model.APIFormatOpenAIEmbedding:
		return OutboundTypeOpenAIEmbedding, true
	default:
		return 0, false
	}
}

var outboundFactories = map[OutboundType]func() model.Outbound{
	OutboundTypeOpenAIChat:      func() model.Outbound { return &openai.ChatOutbound{} },
	OutboundTypeOpenAIResponse:  func() model.Outbound { return &openai.ResponseOutbound{} },
	OutboundTypeOpenAIEmbedding: func() model.Outbound { return &openai.EmbeddingOutbound{} },
	OutboundTypeAnthropic:       func() model.Outbound { return &outAnthropic.MessageOutbound{} },
	OutboundTypeGemini:          func() model.Outbound { return &gemini.MessagesOutbound{} },
	OutboundTypeVolcengine:      func() model.Outbound { return &volcengine.ResponseOutbound{} },
}

func Get(outboundType OutboundType) model.Outbound {
	if factory, ok := outboundFactories[outboundType]; ok {
		return factory()
	}
	return nil
}
