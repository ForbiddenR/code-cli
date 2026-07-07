package anthropicapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"code-cli/internal/core"
)

func TestRetryDelayNoJitterCapsAtMaxDelay(t *testing.T) {
	config := core.RetryConfig{
		BaseDelay: 500 * time.Millisecond,
		MaxDelay:  1 * time.Second,
	}.WithDefaults()

	if got := retryDelay(config, 1); got != 500*time.Millisecond {
		t.Fatalf("attempt 1 delay = %s", got)
	}
	if got := retryDelay(config, 2); got != 1*time.Second {
		t.Fatalf("attempt 2 delay = %s", got)
	}
	if got := retryDelay(config, 3); got != 1*time.Second {
		t.Fatalf("attempt 3 delay = %s", got)
	}
}

func TestRetryAPIRetryableErrorSucceeds(t *testing.T) {
	var attempts int
	var sleeps []time.Duration
	got, err := retryAPI(context.Background(), core.RetryConfig{
		MaxRetries: 2,
		BaseDelay:  10 * time.Millisecond,
		MaxDelay:   100 * time.Millisecond,
	}, func(_ context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return nil
	}, func(_ context.Context, attempt int) (string, error) {
		attempts = attempt
		if attempt < 3 {
			return "", &core.APIError{Kind: core.APIErrorRateLimit, Message: "retry me", Retryable: true}
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("retryAPI() error = %v", err)
	}
	if got != "ok" || attempts != 3 {
		t.Fatalf("result/attempts = %q/%d", got, attempts)
	}
	if len(sleeps) != 2 || sleeps[0] != 10*time.Millisecond || sleeps[1] != 20*time.Millisecond {
		t.Fatalf("sleeps = %#v", sleeps)
	}
}

func TestRetryAPINonRetryableErrorDoesNotRetry(t *testing.T) {
	var attempts int
	_, err := retryAPI(context.Background(), core.RetryConfig{MaxRetries: 3}, func(context.Context, time.Duration) error {
		t.Fatalf("sleep should not be called")
		return nil
	}, func(context.Context, int) (string, error) {
		attempts++
		return "", errors.New("boom")
	})
	if attempts != 1 {
		t.Fatalf("attempts = %d", attempts)
	}
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != core.APIErrorUnknown || apiErr.Retryable {
		t.Fatalf("error = %#v", err)
	}
}

func TestRetryAPIContextCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := retryAPI(ctx, core.RetryConfig{MaxRetries: 1}, func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}, func(context.Context, int) (string, error) {
		return "", &core.APIError{Kind: core.APIErrorTimeout, Message: "timeout", Retryable: true}
	})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != core.APIErrorAbort || apiErr.Retryable {
		t.Fatalf("error = %#v", err)
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	got, ok := parseRetryAfter("7")
	if !ok || got != 7*time.Second {
		t.Fatalf("retry after = %s, %v", got, ok)
	}
}

func TestRetryAfterDelayReadsHeaders(t *testing.T) {
	err := retryAfterHeaderError{Header: http.Header{"Retry-After": []string{"2"}}}
	got, ok := retryAfterDelay(err)
	if !ok || got != 2*time.Second {
		t.Fatalf("retry after = %s, %v", got, ok)
	}
}

type retryAfterHeaderError struct {
	Header http.Header
}

func (e retryAfterHeaderError) Error() string { return "retry later" }
