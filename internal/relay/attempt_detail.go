package relay

import (
	"errors"
	"fmt"
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

// truncationMarkerBudget 为截断标记预留的字节数（实际标记远短于此，
// 预留量避免标记长度随字节数位数变化而影响头尾切分的计算）。
const truncationMarkerBudget = 96

// truncateFailedDetail 把超长的失败详情压缩到 maxFailedDetailBytes 以内：
// 保留头部与尾部，中间用显式标记替代。
//
// 为什么必须保留尾部：Anthropic/OpenAI 请求体的顶层参数（thinking、
// temperature、metadata、reasoning_effort 等）排在 messages 之后，而 messages
// 往往占 99% 的体积。只截头部会让这些参数全部不可见——排查上游"参数非法"类
// 错误时恰恰只需要它们，头部反而没有信息量。
//
// 为什么必须标记：不加标记的截断在导出文件里与完整请求体无法区分，会让人把
// 残缺内容当成网关实际发出的形态。
//
// 截断点对齐 UTF-8 字符边界，避免半个多字节字符变成乱码。
func truncateFailedDetail(body []byte) []byte {
	total := len(body)
	if total <= maxFailedDetailBytes {
		return body
	}
	budget := maxFailedDetailBytes - truncationMarkerBudget
	if budget < 2 {
		return body[:alignUTF8Left(body, maxFailedDetailBytes)]
	}

	head := alignUTF8Left(body, budget/2)
	tailStart := alignUTF8Right(body, total-(budget-head))
	if tailStart <= head {
		return body[:alignUTF8Left(body, maxFailedDetailBytes)]
	}

	marker := fmt.Sprintf("\n...[octopus: omitted %d of %d bytes]...\n", tailStart-head, total)
	out := make([]byte, 0, head+len(marker)+total-tailStart)
	out = append(out, body[:head]...)
	out = append(out, marker...)
	out = append(out, body[tailStart:]...)
	return out
}

// alignUTF8Left 把切分位置向前退到 UTF-8 字符起始边界（用作 body[:pos] 的上界）。
func alignUTF8Left(body []byte, pos int) int {
	if pos <= 0 {
		return 0
	}
	if pos >= len(body) {
		return len(body)
	}
	for pos > 0 && body[pos]&0xC0 == 0x80 {
		pos--
	}
	return pos
}

// alignUTF8Right 把切分位置向后推到 UTF-8 字符起始边界（用作 body[pos:] 的下界）。
func alignUTF8Right(body []byte, pos int) int {
	if pos <= 0 {
		return 0
	}
	if pos >= len(body) {
		return len(body)
	}
	for pos < len(body) && body[pos]&0xC0 == 0x80 {
		pos++
	}
	return pos
}

// recordSpanRequestBody 记录该尝试的出站请求体（转换后形态）。
func recordSpanRequestBody(span *balancer.AttemptSpan, body []byte) {
	if span == nil || len(body) == 0 || !failedDetailEnabled() {
		return
	}
	span.SetRequestBody(truncateFailedDetail(body))
}

// recordSpanResponseBody 记录该尝试的失败响应体。
func recordSpanResponseBody(span *balancer.AttemptSpan, body []byte) {
	if span == nil || len(body) == 0 || !failedDetailEnabled() {
		return
	}
	span.SetResponseBody(truncateFailedDetail(body))
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
	span.SetOutboundHeaders(truncateFailedDetail([]byte(encoded)))
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
