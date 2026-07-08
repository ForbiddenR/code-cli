package sessioningress

import (
	"net/http"
	"time"
)

const (
	// DefaultBaseURL is the production OAuth BASE_API_URL used by session ingress endpoints.
	DefaultBaseURL    = "https://api.anthropic.com"
	DefaultTimeout    = 20 * time.Second
	DefaultMaxRetries = 10
	DefaultBaseDelay  = 500 * time.Millisecond
)

// Config contains process-level settings for session ingress calls.
type Config struct {
	BaseURL          string
	AuthToken        string
	OrgUUID          string
	HTTPClient       *http.Client
	Timeout          time.Duration
	MaxRetries       int
	BaseDelay        time.Duration
	Sleep            func(time.Duration)
	AfterLastCompact bool
}

// Entry is one raw transcript entry returned by session ingress.
type Entry []byte

// TranscriptEntry is the minimum shape required for append ordering.
type TranscriptEntry struct {
	UUID string
	Body []byte
}
