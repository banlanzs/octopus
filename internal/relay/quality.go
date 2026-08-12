package relay

import (
	"time"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/relay/balancer"
	transformerModel "github.com/bestruirui/octopus/internal/transformer/model"
	"github.com/bestruirui/octopus/internal/utils/log"
)

// ---- 质量失败检测（成功但输出异常）----
// 背景：部分中转站在工具循环场景返回"极短输出 + end_turn"（HTTP 200 被当作
// 成功），熔断器/AutoRank 完全无感知，请求永远停留在劣质渠道。
// 方案：检测"工具循环中输出过短" → 记 AutoRank 失败样本 + key 级冷却 →
// 后续请求自动优先其他渠道（无感切换）。冷却结束后经 HalfOpen 自动探测恢复。

func qualityFailEnabled() bool {
	enabled, err := op.SettingGetBool(dbmodel.SettingKeyQualityFailEnabled)
	return err == nil && enabled
}

func qualityFailMinOutput() int64 {
	v, err := op.SettingGetInt(dbmodel.SettingKeyQualityFailMinOutput)
	if err != nil || v < 0 {
		return 100
	}
	return int64(v)
}

func qualityFailMinMaxTokens() int64 {
	v, err := op.SettingGetInt(dbmodel.SettingKeyQualityFailMinMaxTokens)
	if err != nil || v < 0 {
		return 1024
	}
	return int64(v)
}

func qualityFailCooldown() time.Duration {
	v, err := op.SettingGetInt(dbmodel.SettingKeyQualityFailCooldown)
	if err != nil || v < 0 {
		return 60 * time.Second
	}
	return time.Duration(v) * time.Second
}

// isQualityFailureResponse 判定"成功但输出异常"：
//   - 输出 token 低于阈值（0 表示不限制）
//   - 请求 max_tokens 足够大（排除 guardrail/短输出请求的正常短响应）
//   - 请求带 tools 且历史含 tool 消息或 tool_calls（工具循环中被提前终止）
func isQualityFailureResponse(internalRequest *transformerModel.InternalLLMRequest, outputTokens int64) bool {
	if internalRequest == nil || !qualityFailEnabled() {
		return false
	}
	minOutput := qualityFailMinOutput()
	if minOutput <= 0 || outputTokens >= minOutput {
		return false
	}
	// 排除 guardrail 等小 max_tokens 请求（正常短输出）
	mt := internalRequest.MaxTokens
	if mt == nil || *mt < qualityFailMinMaxTokens() {
		return false
	}
	// 工具循环上下文：请求带 tools 且历史含 tool 消息或 tool_calls
	if len(internalRequest.Tools) == 0 {
		return false
	}
	hasToolLoop := false
	for i := range internalRequest.Messages {
		m := &internalRequest.Messages[i]
		if m.Role == "tool" || len(m.ToolCalls) > 0 {
			hasToolLoop = true
			break
		}
	}
	return hasToolLoop
}

// recordQualityFailure 执行质量失败惩罚：key 级冷却 + AutoRank 失败样本。
// 冷却结束后自动 HalfOpen 探测恢复；AutoRank 失败样本使该 (渠道,模型) 排序降权。
func recordQualityFailure(group dbmodel.Group, channelID, keyID int, modelName string, outputTokens, durationMS, ttfbMS int64) {
	cd := qualityFailCooldown()
	if cd > 0 {
		balancer.RecordQualityFailure(channelID, keyID, modelName, cd)
	}
	// AutoRank 失败样本（statusCode=500 仅为计数标签，不影响已下发的成功响应；
	// 500 在 isAutoRankCountableFailure 白名单内，样本可计入失败率）
	recordAutoRankResult(group, channelID, modelName, false, 500, durationMS, ttfbMS)
	log.Warnf("quality failure detected: channel=%d key=%d model=%s output=%d (cooldown=%v)",
		channelID, keyID, modelName, outputTokens, cd)
}
