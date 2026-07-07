package controlplane

import (
	"net/http"
	"time"
)

const DefaultTimeout = 5 * time.Second

// Config contains process-level settings for authenticated control-plane API calls.
type Config struct {
	BaseURL        string
	UserAgent      string
	DefaultHeaders map[string]string
	AuthHeaders    map[string]string
	Timeout        time.Duration
	HTTPClient     *http.Client
}

// CallOptions controls one control-plane API call without changing client config.
type CallOptions struct {
	Timeout time.Duration
	Headers map[string]string
}

// CallOption mutates call-level options.
type CallOption func(*CallOptions)

// WithTimeout sets a call-specific timeout.
func WithTimeout(timeout time.Duration) CallOption {
	return func(opts *CallOptions) {
		opts.Timeout = timeout
	}
}

// WithHeader adds one call-specific header.
func WithHeader(name string, value string) CallOption {
	return func(opts *CallOptions) {
		if opts.Headers == nil {
			opts.Headers = map[string]string{}
		}
		opts.Headers[name] = value
	}
}

// ApplyOptions returns concrete call options from functional options.
func ApplyOptions(options ...CallOption) CallOptions {
	var opts CallOptions
	for _, option := range options {
		if option != nil {
			option(&opts)
		}
	}
	return opts
}
