package anthropicapi

import (
	"testing"
	"time"

	"code-cli/internal/core"
)

func TestApplyOptionsRetryConfig(t *testing.T) {
	opts := ApplyOptions(
		WithTimeout(3*time.Second),
		WithRetryConfig(core.RetryConfig{MaxRetries: 2, BaseDelay: time.Second}),
	)
	if opts.Timeout != 3*time.Second {
		t.Fatalf("timeout = %s", opts.Timeout)
	}
	if opts.Retry == nil || opts.Retry.MaxRetries != 2 || opts.Retry.BaseDelay != time.Second {
		t.Fatalf("retry = %#v", opts.Retry)
	}
}

func TestWithoutRetries(t *testing.T) {
	opts := ApplyOptions(WithoutRetries())
	if opts.Retry == nil {
		t.Fatalf("expected retry override")
	}
	if got := opts.Retry.WithDefaults(); got.MaxRetries != 0 {
		t.Fatalf("max retries = %d", got.MaxRetries)
	}
}

func TestSDKClientRetryConfigUsesCallOverride(t *testing.T) {
	client := &SDKClient{config: core.APIConfig{Retry: &core.RetryConfig{MaxRetries: 5}}}
	if got := client.retryConfig(CallOptions{}); got.MaxRetries != 5 {
		t.Fatalf("client retry max = %d", got.MaxRetries)
	}
	if got := client.retryConfig(ApplyOptions(WithoutRetries())); got.MaxRetries != 0 {
		t.Fatalf("call retry max = %d", got.MaxRetries)
	}
}
