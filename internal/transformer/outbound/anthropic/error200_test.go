package anthropic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/transformer/model"
)

// TestMessageTransformResponse200ErrorBody verifies that a 200 response
// carrying an Anthropic-style {"error": {...}} body (common from aggregators /
// relay stations) is treated as failure so the relay can retry another channel
// instead of returning the error to the client as a success.
func TestMessageTransformResponse200ErrorBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)),
	}
	outbound := &MessageOutbound{}
	_, err := outbound.TransformResponse(context.Background(), resp)
	if err == nil {
		t.Fatal("expected error for 200 + error body")
	}
	var re *model.ResponseError
	if !errors.As(err, &re) {
		t.Fatalf("expected ResponseError, got %T", err)
	}
	if re.Detail.Message != "Overloaded" {
		t.Fatalf("unexpected error detail: %+v", re.Detail)
	}
}
