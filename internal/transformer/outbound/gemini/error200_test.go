package gemini

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/transformer/model"
)

// TestGeminiTransformResponse200ErrorBody verifies that a 200 response
// carrying a Gemini-style {"error": {...}} body is treated as failure so the
// relay can retry another channel instead of returning the error to the
// client as a success.
func TestGeminiTransformResponse200ErrorBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"code":429,"message":"Rate limit exceeded","status":"RESOURCE_EXHAUSTED"}}`)),
	}
	outbound := &MessagesOutbound{}
	_, err := outbound.TransformResponse(context.Background(), resp)
	if err == nil {
		t.Fatal("expected error for 200 + error body")
	}
	var re *model.ResponseError
	if !errors.As(err, &re) {
		t.Fatalf("expected ResponseError, got %T", err)
	}
	if re.Detail.Message != "Rate limit exceeded" {
		t.Fatalf("unexpected error detail: %+v", re.Detail)
	}
}
