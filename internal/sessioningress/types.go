package sessioningress

import (
	"net/http"
	"os"
	"time"
)

const (
	// DefaultBaseURL is the production OAuth BASE_API_URL used by session ingress endpoints.
	DefaultBaseURL       = "https://api.anthropic.com"
	DefaultTimeout       = 20 * time.Second
	DefaultMaxRetries    = 10
	DefaultBaseDelay     = 500 * time.Millisecond
	DefaultTeleportLimit = 1000
	DefaultMaxPages      = 100

	// EnvSessionAccessToken is the primary session ingress token environment variable.
	EnvSessionAccessToken = "CLAUDE_CODE_SESSION_ACCESS_TOKEN"
	// EnvOrganizationUUID supplies the organization UUID for session-key auth and OAuth calls.
	EnvOrganizationUUID = "CLAUDE_CODE_ORGANIZATION_UUID"
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
	TeleportLimit    int
	MaxTeleportPages int
}

// ConfigFromEnv returns session ingress auth configuration from the primary runtime environment variables.
func ConfigFromEnv() Config {
	return Config{
		AuthToken: os.Getenv(EnvSessionAccessToken),
		OrgUUID:   os.Getenv(EnvOrganizationUUID),
	}
}

// TranscriptSource identifies which read path returned transcript entries.
type TranscriptSource string

const (
	// TranscriptSourceTeleportEvents indicates entries came from the CCR v2 Teleport Events API.
	TranscriptSourceTeleportEvents TranscriptSource = "teleport_events"
	// TranscriptSourceSessionIngress indicates entries came from the legacy session ingress API.
	TranscriptSourceSessionIngress TranscriptSource = "session_ingress"
)

// Entry is one raw transcript entry returned by session ingress.
type Entry []byte

// TranscriptEntry is the minimum shape required for append ordering.
type TranscriptEntry struct {
	UUID string
	Body []byte
}
