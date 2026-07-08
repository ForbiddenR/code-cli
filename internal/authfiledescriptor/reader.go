package authfiledescriptor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	// RemoteTokenDir is the CCR well-known credential directory.
	RemoteTokenDir = "/home/claude/.claude/remote"
	// DefaultOAuthTokenPath is the CCR OAuth token fallback path.
	DefaultOAuthTokenPath = RemoteTokenDir + "/.oauth_token"
	// DefaultAPIKeyPath is the CCR API key fallback path.
	DefaultAPIKeyPath = RemoteTokenDir + "/.api_key"
	// DefaultSessionIngressTokenPath is the CCR session ingress token fallback path.
	DefaultSessionIngressTokenPath = RemoteTokenDir + "/.session_ingress_token"

	// EnvRemote gates best-effort persistence of FD credentials for CCR subprocesses.
	EnvRemote = "CLAUDE_CODE_REMOTE"
	// EnvOAuthTokenFileDescriptor points to an inherited OAuth token file descriptor.
	EnvOAuthTokenFileDescriptor = "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR"
	// EnvAPIKeyFileDescriptor points to an inherited API key file descriptor.
	EnvAPIKeyFileDescriptor = "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR"
	// EnvWebsocketAuthFileDescriptor points to the legacy session ingress token file descriptor.
	EnvWebsocketAuthFileDescriptor = "CLAUDE_CODE_WEBSOCKET_AUTH_FILE_DESCRIPTOR"
)

// CredentialSource describes one FD-backed credential and its disk fallback.
type CredentialSource struct {
	EnvVar        string
	WellKnownPath string
	Label         string
}

var (
	// OAuthTokenSource is the CCR-injected OAuth token credential source.
	OAuthTokenSource = CredentialSource{EnvVar: EnvOAuthTokenFileDescriptor, WellKnownPath: DefaultOAuthTokenPath, Label: "OAuth token"}
	// APIKeySource is the CCR-injected API key credential source.
	APIKeySource = CredentialSource{EnvVar: EnvAPIKeyFileDescriptor, WellKnownPath: DefaultAPIKeyPath, Label: "API key"}
	// SessionIngressTokenSource is the CCR-injected legacy session ingress token source.
	SessionIngressTokenSource = CredentialSource{EnvVar: EnvWebsocketAuthFileDescriptor, WellKnownPath: DefaultSessionIngressTokenPath, Label: "session ingress token"}
)

// Reader reads FD-backed credentials with TypeScript-compatible fallback and cache behavior.
type Reader struct {
	Getenv    func(string) string
	ReadFile  func(string) ([]byte, error)
	MkdirAll  func(string, os.FileMode) error
	WriteFile func(string, []byte, os.FileMode) error
	GOOS      string
	cache     map[string]cachedCredential
}

type cachedCredential struct {
	value string
	set   bool
}

// DefaultReader returns a reader backed by process environment and OS filesystem calls.
func DefaultReader() *Reader {
	return &Reader{
		Getenv:    os.Getenv,
		ReadFile:  os.ReadFile,
		MkdirAll:  os.MkdirAll,
		WriteFile: os.WriteFile,
		GOOS:      runtime.GOOS,
	}
}

// OAuthTokenFromEnv returns the CCR-injected OAuth token from FD or well-known file.
func OAuthTokenFromEnv() string {
	return DefaultReader().Credential(OAuthTokenSource)
}

// APIKeyFromEnv returns the CCR-injected API key from FD or well-known file.
func APIKeyFromEnv() string {
	return DefaultReader().Credential(APIKeySource)
}

// Credential returns one credential using the TypeScript discovery order:
// cached result, file descriptor, then well-known file fallback.
func (r *Reader) Credential(source CredentialSource) string {
	r.normalize()
	cacheKey := source.EnvVar + "\x00" + source.WellKnownPath
	if cached, ok := r.cache[cacheKey]; ok {
		return cached.value
	}

	fdEnv := r.Getenv(source.EnvVar)
	if fdEnv == "" {
		return r.cacheCredential(cacheKey, r.readTokenFile(source.WellKnownPath))
	}

	fd, err := strconv.Atoi(fdEnv)
	if err != nil {
		return r.cacheCredential(cacheKey, "")
	}

	token := r.readTokenFile(r.fdPath(fd))
	if token != "" {
		r.maybePersistTokenForSubprocesses(source.WellKnownPath, token)
		return r.cacheCredential(cacheKey, token)
	}
	return r.cacheCredential(cacheKey, r.readTokenFile(source.WellKnownPath))
}

// ReadTokenFile reads and trims a token file, returning an empty string for missing, unreadable, or empty files.
func (r *Reader) ReadTokenFile(path string) string {
	r.normalize()
	return r.readTokenFile(path)
}

// MaybePersistTokenForSubprocesses writes a credential to its well-known path when running in CCR remote mode.
func (r *Reader) MaybePersistTokenForSubprocesses(path string, token string) {
	r.normalize()
	r.maybePersistTokenForSubprocesses(path, token)
}

func (r *Reader) cacheCredential(key string, value string) string {
	r.cache[key] = cachedCredential{value: value, set: true}
	return value
}

func (r *Reader) normalize() {
	if r.Getenv == nil {
		r.Getenv = os.Getenv
	}
	if r.ReadFile == nil {
		r.ReadFile = os.ReadFile
	}
	if r.MkdirAll == nil {
		r.MkdirAll = os.MkdirAll
	}
	if r.WriteFile == nil {
		r.WriteFile = os.WriteFile
	}
	if r.GOOS == "" {
		r.GOOS = runtime.GOOS
	}
	if r.cache == nil {
		r.cache = map[string]cachedCredential{}
	}
}

func (r *Reader) fdPath(fd int) string {
	if r.GOOS == "darwin" || r.GOOS == "freebsd" {
		return fmt.Sprintf("/dev/fd/%d", fd)
	}
	return fmt.Sprintf("/proc/self/fd/%d", fd)
}

func (r *Reader) readTokenFile(path string) string {
	content, err := r.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(content))
}

func (r *Reader) maybePersistTokenForSubprocesses(path string, token string) {
	if !isEnvTruthy(r.Getenv(EnvRemote)) || token == "" {
		return
	}
	if err := r.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = r.WriteFile(path, []byte(token), 0o600)
}

func isEnvTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "t", "yes", "y", "on":
		return true
	default:
		return false
	}
}
