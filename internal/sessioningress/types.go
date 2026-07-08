package sessioningress

import (
	"net/http"
	"os"
	"time"

	"code-cli/internal/authfiledescriptor"
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
	// EnvWebsocketAuthFileDescriptor is the legacy CCR file descriptor token variable.
	EnvWebsocketAuthFileDescriptor = authfiledescriptor.EnvWebsocketAuthFileDescriptor
	// EnvSessionIngressTokenFile overrides the well-known session ingress token file path.
	EnvSessionIngressTokenFile = "CLAUDE_SESSION_INGRESS_TOKEN_FILE"
	// EnvOrganizationUUID supplies the organization UUID for session-key auth and OAuth calls.
	EnvOrganizationUUID = "CLAUDE_CODE_ORGANIZATION_UUID"
	// EnvRemote gates token persistence for subprocesses inside CCR remote environments.
	EnvRemote = authfiledescriptor.EnvRemote
	// DefaultSessionIngressTokenPath is the CCR well-known token file fallback path.
	DefaultSessionIngressTokenPath = authfiledescriptor.DefaultSessionIngressTokenPath
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

// ConfigFromEnv returns session ingress auth configuration from runtime environment token sources.
func ConfigFromEnv() Config {
	return Config{
		AuthToken: SessionIngressAuthTokenFromEnv(),
		OrgUUID:   os.Getenv(EnvOrganizationUUID),
	}
}

// SessionIngressAuthTokenFromEnv returns the session ingress token using the TypeScript discovery order.
func SessionIngressAuthTokenFromEnv() string {
	if token := os.Getenv(EnvSessionAccessToken); token != "" {
		return token
	}
	source := authfiledescriptor.SessionIngressTokenSource
	source.WellKnownPath = sessionIngressTokenFilePath()
	return authfiledescriptor.DefaultReader().Credential(source)
}

func sessionIngressTokenFilePath() string {
	if path := os.Getenv(EnvSessionIngressTokenFile); path != "" {
		return path
	}
	return DefaultSessionIngressTokenPath
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
