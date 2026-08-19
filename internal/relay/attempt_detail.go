package relay

import (
	"errors"
	"net/http"

	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/looplj/axonhub/llm/httpclient"
)

// 失败尝试详情记录。
//
// 自研路径（relay.go）与 axon 文本路径（textrelay.go）共用同一套开关
// （relay_log_failed_detail_enabled）、截断（maxFailedDetailBytes）与脱敏口径。
// 抽到这里是因为此前两条路径的落盘能力并不对等：axon 路径完全没有调用
// 记录逻辑，导致 relay_log_failed_detail_enabled 对已迁移的主力路径实际失效，
// 前端只能回落到日志级字段展示，失败详情长期具有误导性。
//
// 注意：AttemptSpan 的 Set* 在 End 之后无效，所有调用点必须位于
// span.End(...) 之前。

// recordSpanRequestBody 记录该尝试的出站请求体（转换后形态）。
func recordSpanRequestBody(span *balancer.AttemptSpan, body []byte) {
	if span == nil || len(body) == 0 || !failedDetailEnabled() {
		return
	}
	if len(body) > maxFailedDetailBytes {
		body = body[:maxFailedDetailBytes]
	}
	span.SetRequestBody(body)
}

// recordSpanResponseBody 记录该尝试的失败响应体。
func recordSpanResponseBody(span *balancer.AttemptSpan, body []byte) {
	if span == nil || len(body) == 0 || !failedDetailEnabled() {
		return
	}
	if len(body) > maxFailedDetailBytes {
		body = body[:maxFailedDetailBytes]
	}
	span.SetResponseBody(body)
}

// recordSpanOutboundHeaders 记录该尝试的出站请求头（脱敏 JSON）。
func recordSpanOutboundHeaders(span *balancer.AttemptSpan, h http.Header) {
	if span == nil || len(h) == 0 || !failedDetailEnabled() {
		return
	}
	encoded := serializeRequestHeadersForLog(h)
	if len(encoded) == 0 {
		return
	}
	if len(encoded) > maxFailedDetailBytes {
		encoded = encoded[:maxFailedDetailBytes]
	}
	span.SetOutboundHeaders([]byte(encoded))
}

// recordAxonAttemptRequest 记录 axon 路径一次尝试的出站请求（体 + 头）。
// 出站体优先取 JSONBody——部分 transformer（如图片编辑）的 Body 非 JSON，
// 以 JSONBody 作为入日志形态；为空时回落 Body。
func recordAxonAttemptRequest(span *balancer.AttemptSpan, outReq *httpclient.Request) {
	if span == nil || outReq == nil {
		return
	}
	body := outReq.JSONBody
	if len(body) == 0 {
		body = outReq.Body
	}
	recordSpanRequestBody(span, body)
	recordSpanOutboundHeaders(span, outReq.Headers)
}

// recordAxonAttemptFailureBody 从 axonhub httpclient 错误中提取上游响应体并记入 span。
// 非 httpclient.Error（本地转换错误、连接错误、首 token 超时等）本就没有上游
// 响应体，跳过即可——此时 attempt 的 msg 才是唯一的诊断信息。
func recordAxonAttemptFailureBody(span *balancer.AttemptSpan, err error) {
	if span == nil || err == nil {
		return
	}
	var he *httpclient.Error
	if errors.As(err, &he) && len(he.Body) > 0 {
		recordSpanResponseBody(span, he.Body)
	}
}

// recordAxonAttemptFailureDetail 在失败分支入口一次性记录本次尝试的出站请求
// （体 + 头）与上游响应体。
//
// 放在分支入口而非各 span.End 之前，是因为同一个失败分支里存在多条 End 路径
// （auto 渠道的协议能力缺失分支会先 End 再 break），而 Set* 在 End 之后无效。
// 只在失败时记录：成功尝试的请求体已由日志级 request_content 承载，重复落盘
// 会让 relay_logs 无谓膨胀。
func recordAxonAttemptFailureDetail(span *balancer.AttemptSpan, outReq *httpclient.Request, err error) {
	recordAxonAttemptRequest(span, outReq)
	recordAxonAttemptFailureBody(span, err)
}
