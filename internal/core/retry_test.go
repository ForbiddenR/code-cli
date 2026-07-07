package core

import (
	"testing"
	"time"
)

func TestDefaultRetryConfig(t *testing.T) {
	got := DefaultRetryConfig()
	if got.MaxRetries != DefaultMaxRetries {
		t.Fatalf("max retries = %d", got.MaxRetries)
	}
	if got.BaseDelay != 500*time.Millisecond {
		t.Fatalf("base delay = %s", got.BaseDelay)
	}
	if got.MaxDelay != 32*time.Second {
		t.Fatalf("max delay = %s", got.MaxDelay)
	}
	if got.JitterFraction != 0.25 {
		t.Fatalf("jitter fraction = %f", got.JitterFraction)
	}
}

func TestRetryConfigWithDefaultsPreservesExplicitZeroRetriesAndJitter(t *testing.T) {
	got := RetryConfig{MaxRetries: 0, JitterFraction: 0}.WithDefaults()
	if got.MaxRetries != 0 {
		t.Fatalf("max retries = %d", got.MaxRetries)
	}
	if got.JitterFraction != 0 {
		t.Fatalf("jitter fraction = %f", got.JitterFraction)
	}
	if got.BaseDelay != DefaultRetryBaseDelay || got.MaxDelay != DefaultRetryMaxDelay {
		t.Fatalf("delays = %s/%s", got.BaseDelay, got.MaxDelay)
	}
}

func TestAPIConfigWithDefaultsRetryPolicy(t *testing.T) {
	defaulted := APIConfig{}.WithDefaults()
	if defaulted.Retry == nil {
		t.Fatalf("expected default retry config")
	}
	if defaulted.Retry.MaxRetries != DefaultMaxRetries {
		t.Fatalf("default max retries = %d", defaulted.Retry.MaxRetries)
	}

	disabled := APIConfig{Retry: &RetryConfig{MaxRetries: 0}}.WithDefaults()
	if disabled.Retry == nil {
		t.Fatalf("expected retry config")
	}
	if disabled.Retry.MaxRetries != 0 {
		t.Fatalf("disabled max retries = %d", disabled.Retry.MaxRetries)
	}
	if disabled.Retry.BaseDelay != DefaultRetryBaseDelay {
		t.Fatalf("disabled base delay = %s", disabled.Retry.BaseDelay)
	}
}
