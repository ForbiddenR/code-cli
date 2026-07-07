package core

import "time"

const (
	// DefaultMaxRetries matches the TypeScript API retry loop's default retry count.
	DefaultMaxRetries = 10
	// DefaultRetryBaseDelay is the first retry delay before exponential backoff.
	DefaultRetryBaseDelay = 500 * time.Millisecond
	// DefaultRetryMaxDelay caps computed exponential backoff delays.
	DefaultRetryMaxDelay = 32 * time.Second
	// DefaultRetryJitterFraction adds up to 25% jitter to computed delays.
	DefaultRetryJitterFraction = 0.25
)

// RetryConfig controls retry behavior for transient Claude API failures.
type RetryConfig struct {
	// MaxRetries is the number of retries after the initial attempt.
	MaxRetries int
	// BaseDelay is the delay before the first retry.
	BaseDelay time.Duration
	// MaxDelay caps computed exponential backoff delays.
	MaxDelay time.Duration
	// JitterFraction adds bounded positive jitter to computed delays.
	JitterFraction float64
}

// DefaultRetryConfig returns the default bounded retry policy.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:     DefaultMaxRetries,
		BaseDelay:      DefaultRetryBaseDelay,
		MaxDelay:       DefaultRetryMaxDelay,
		JitterFraction: DefaultRetryJitterFraction,
	}
}

// WithDefaults returns a copy with timing defaults applied.
//
// MaxRetries and JitterFraction intentionally keep explicit zero values: zero
// retries disables retrying, and zero jitter makes tests deterministic.
func (c RetryConfig) WithDefaults() RetryConfig {
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	if c.BaseDelay <= 0 {
		c.BaseDelay = DefaultRetryBaseDelay
	}
	if c.MaxDelay <= 0 {
		c.MaxDelay = DefaultRetryMaxDelay
	}
	if c.MaxDelay < c.BaseDelay {
		c.MaxDelay = c.BaseDelay
	}
	if c.JitterFraction < 0 {
		c.JitterFraction = 0
	}
	return c
}
