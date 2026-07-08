package sessioningress

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
	// EnvWebsocketAuthFileDescriptor is the legacy CCR file descriptor token variable.
	EnvWebsocketAuthFileDescriptor = "CLAUDE_CODE_WEBSOCKET_AUTH_FILE_DESCRIPTOR"
	// EnvSessionIngressTokenFile overrides the well-known session ingress token file path.
	EnvSessionIngressTokenFile = "CLAUDE_SESSION_INGRESS_TOKEN_FILE"
	// EnvOrganizationUUID supplies the organization UUID for session-key auth and OAuth calls.
	EnvOrganizationUUID = "CLAUDE_CODE_ORGANIZATION_UUID"
	// EnvRemote gates token persistence for subprocesses inside CCR remote environments.
	EnvRemote = "CLAUDE_CODE_REMOTE"
	// DefaultSessionIngressTokenPath is the CCR well-known token file fallback path.
	DefaultSessionIngressTokenPath = "/home/claude/.claude/remote/.session_ingress_token"
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
	if fdEnv := os.Getenv(EnvWebsocketAuthFileDescriptor); fdEnv != "" {
		fd, err := strconv.Atoi(fdEnv)
		if err != nil {
			return ""
		}
		if token := readTokenFile(fdPath(fd)); token != "" {
			maybePersistTokenForSubprocesses(token)
			return token
		}
	}
	return readTokenFile(sessionIngressTokenFilePath())
}

func sessionIngressTokenFilePath() string {
	if path := os.Getenv(EnvSessionIngressTokenFile); path != "" {
		return path
	}
	return DefaultSessionIngressTokenPath
}

func fdPath(fd int) string {
	if runtime.GOOS == "darwin" || runtime.GOOS == "freebsd" {
		return fmt.Sprintf("/dev/fd/%d", fd)
	}
	return fmt.Sprintf("/proc/self/fd/%d", fd)
}

func readTokenFile(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}

func maybePersistTokenForSubprocesses(token string) {
	if !isEnvTruthy(os.Getenv(EnvRemote)) {
		return
	}
	path := sessionIngressTokenFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(token), 0o600)
}

func isEnvTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "t", "yes", "y", "on":
		return true
	default:
		return false
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
