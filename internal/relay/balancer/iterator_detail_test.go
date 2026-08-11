package balancer

import "testing"

func TestAttemptSpanSetRequestResponseBody(t *testing.T) {
	span := &AttemptSpan{}

	span.SetRequestBody([]byte(`{"model":"deepseek-v4-flash"}`))
	if span.attempt.RequestBody != `{"model":"deepseek-v4-flash"}` {
		t.Fatalf("request body not recorded: %q", span.attempt.RequestBody)
	}
	span.SetResponseBody([]byte(`{"error":{"message":"boom"}}`))
	if span.attempt.ResponseBody != `{"error":{"message":"boom"}}` {
		t.Fatalf("response body not recorded: %q", span.attempt.ResponseBody)
	}

	// 空数据不写入
	span.SetRequestBody(nil)
	span.SetResponseBody([]byte(""))
	if span.attempt.RequestBody != `{"model":"deepseek-v4-flash"}` {
		t.Fatalf("nil request body must be ignored: %q", span.attempt.RequestBody)
	}

	// End 后不再写入
	span.ended = true
	span.SetRequestBody([]byte("late"))
	span.SetResponseBody([]byte("late"))
	if span.attempt.RequestBody != `{"model":"deepseek-v4-flash"}` || span.attempt.ResponseBody != `{"error":{"message":"boom"}}` {
		t.Fatalf("writes after End must be ignored: %+v", span.attempt)
	}
}
