package relay

import (
	"math/rand/v2"
	"strconv"
	"time"
)

// isRetryableStatus 判断 HTTP 状态码是否可重试
// 429(限流)、503(服务不可用)、>=500(服务端错误)、0(连接错误) 可重试
// 400/401/403/404 等客户端错误不可重试
func isRetryableStatus(code int) bool {
	return code == 0 || code == 429 || code >= 500
}

// isPassthroughStatus 判断是否应透传给下游客户端
// 429 和 503 透传，让客户端 SDK 的重试机制接管
func isPassthroughStatus(code int) bool {
	return code == 429 || code == 503
}

// isAutoRankCountableFailure 判断一次失败是否计入 AutoRank 失败样本（影响健康度）。
// 统计口径对齐 ccLoad：只统计能反映渠道/Key 质量的结果，排除客户端噪音——
//   - 客户端误用（404/415/422 等非 401/402/403 的 4xx）、客户端取消(499)、408：不计
//   - 限流(429)：不计（交给熔断 Soft 分支与重试语义，避免个别坏 Key 拉低渠道）
//   - 配额类(596)：不计（不反映渠道质量）
// 纳入：连接错误(0)、Key 级认证(401/402/403/405)、渠道级 5xx(500/502/503/504)、
// Cloudflare(520/521/524)、流式异常(597/598/599)。
func isAutoRankCountableFailure(statusCode int) bool {
	switch statusCode {
	case 0, 401, 402, 403, 405, 500, 502, 503, 504, 520, 521, 524, 597, 598, 599:
		return true
	default:
		return false
	}
}

// parseRetryAfter 解析 Retry-After 响应头（仅支持秒数格式），上限 60s
func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	secs, err := strconv.Atoi(header)
	if err != nil || secs <= 0 {
		return 0
	}
	d := time.Duration(secs) * time.Second
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

// computeBackoff 计算退避时间
// 优先使用 retryAfter（上游指定的等待时间），否则使用指数退避 + jitter
// retryNum 从 1 开始（第1次重试）
func computeBackoff(retryNum int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}

	// 指数退避: 1s * 2^(retryNum-1)
	base := time.Second
	shift := retryNum - 1
	if shift > 5 {
		shift = 5
	}
	delay := base << shift

	if delay > 60*time.Second {
		delay = 60 * time.Second
	}

	// 添加 10%-50% 的 jitter 防止惊群
	jitter := time.Duration(float64(delay) * (0.1 + rand.Float64()*0.4))
	return delay + jitter
}
