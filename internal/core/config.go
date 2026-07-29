package core

import "time"

const (
	DefaultBaseURL               = "https://api.anthropic.com"
	DefaultResponseHeaderTimeout = 30 * time.Second
	DefaultStreamReadIdleTimeout = 90 * time.Second
)

// APIConfig contains process-level Claude API configuration.
type APIConfig struct {
	APIKey                string
	BaseURL               string
	UserAgent             string
	DefaultHeaders        map[string]string
	Betas                 []string
	Retry                 *RetryConfig
	ResponseHeaderTimeout time.Duration
	StreamReadIdleTimeout time.Duration
}

// WithDefaults returns a copy with stable defaults applied.
func (c APIConfig) WithDefaults() APIConfig {
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.Retry == nil {
		retry := DefaultRetryConfig()
		c.Retry = &retry
	} else {
		retry := c.Retry.WithDefaults()
		c.Retry = &retry
	}
	if c.ResponseHeaderTimeout <= 0 {
		c.ResponseHeaderTimeout = DefaultResponseHeaderTimeout
	}
	if c.StreamReadIdleTimeout <= 0 {
		c.StreamReadIdleTimeout = DefaultStreamReadIdleTimeout
	}
	return c
}
