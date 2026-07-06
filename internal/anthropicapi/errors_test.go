package anthropicapi

import (
	"context"
	"errors"
	"testing"

	"code-cli/internal/core"

	"github.com/anthropics/anthropic-sdk-go/shared"
)

type fakeSDKError struct {
	errorType  shared.ErrorType
	statusCode int
	requestID  string
	raw        string
}

func (e *fakeSDKError) Error() string              { return "sdk error" }
func (e *fakeSDKError) Type() shared.ErrorType     { return e.errorType }
func (e *fakeSDKError) RawJSON() string            { return e.raw }
func (e *fakeSDKError) StatusCodeValue() int       { return e.statusCode }
func (e *fakeSDKError) RequestIDValue() string     { return e.requestID }
func (e *fakeSDKError) exportedFieldsForTestOnly() {}

func TestClassifyErrorAPIError(t *testing.T) {
	err := &struct {
		*fakeSDKError
		StatusCode int
		RequestID  string
	}{
		fakeSDKError: &fakeSDKError{
			errorType: shared.ErrorTypeRateLimitError,
			raw:       `{"error":{"message":"too many requests"},"request_id":"req_body"}`,
		},
		StatusCode: 429,
		RequestID:  "req_header",
	}

	got := ClassifyError(err)
	if got.Kind != core.APIErrorRateLimit {
		t.Fatalf("kind = %q", got.Kind)
	}
	if got.StatusCode != 429 || got.RequestID != "req_header" {
		t.Fatalf("status/request = %#v", got)
	}
	if got.Message != "too many requests" {
		t.Fatalf("message = %q", got.Message)
	}
	if !got.Retryable {
		t.Fatalf("expected retryable error")
	}
}

func TestClassifyErrorContextErrors(t *testing.T) {
	if got := ClassifyError(context.Canceled); got.Kind != core.APIErrorAbort || got.Retryable {
		t.Fatalf("context canceled classified as %#v", got)
	}
	if got := ClassifyError(context.DeadlineExceeded); got.Kind != core.APIErrorTimeout || !got.Retryable {
		t.Fatalf("context deadline classified as %#v", got)
	}
}

func TestClassifyErrorNetworkTimeout(t *testing.T) {
	got := ClassifyError(timeoutError{})
	if got.Kind != core.APIErrorTimeout || !got.Retryable {
		t.Fatalf("timeout classified as %#v", got)
	}
}

func TestClassifyErrorUnknown(t *testing.T) {
	got := ClassifyError(errors.New("boom"))
	if got.Kind != core.APIErrorUnknown || got.Retryable {
		t.Fatalf("unknown classified as %#v", got)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ interface {
	Timeout() bool
	Temporary() bool
	Error() string
} = timeoutError{}
