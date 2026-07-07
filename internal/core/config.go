package core

const DefaultBaseURL = "https://api.anthropic.com"

// APIConfig contains process-level Claude API configuration.
type APIConfig struct {
	APIKey         string
	BaseURL        string
	UserAgent      string
	DefaultHeaders map[string]string
	Betas          []string
	Retry          *RetryConfig
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
	return c
}
