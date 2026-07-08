package oauthstorage

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"time"

	"code-cli/internal/authfiledescriptor"
	"code-cli/internal/oauthclient"
	"code-cli/internal/oauthconfig"
)

const (
	// CredentialsFileName is the plaintext secure-storage fallback filename used by Claude Code.
	CredentialsFileName = ".credentials.json"
	// EnvOAuthToken is the force-set inference-only OAuth token environment variable.
	EnvOAuthToken = "CLAUDE_CODE_OAUTH_TOKEN"
	// PlainTextWarning matches the TypeScript plaintext secure-storage warning.
	PlainTextWarning = "Warning: Storing credentials in plaintext."
)

// Store reads and writes Claude Code OAuth credentials.
type Store struct {
	Path             string
	Getenv           func(string) string
	DescriptorReader *authfiledescriptor.Reader
	BareMode         bool
}

// UpdateStatus mirrors the TypeScript secure-storage update result.
type UpdateStatus struct {
	Success bool
	Warning string
}

// Data is the top-level .credentials.json shape used for OAuth storage.
type Data struct {
	ClaudeAIOAuth *StoredTokens              `json:"claudeAiOauth,omitempty"`
	Extra         map[string]json.RawMessage `json:"-"`
}

// StoredTokens is the persisted Claude.ai OAuth token shape.
type StoredTokens struct {
	AccessToken      string                        `json:"accessToken"`
	RefreshToken     string                        `json:"refreshToken,omitempty"`
	ExpiresAt        *MillisTime                   `json:"expiresAt,omitempty"`
	Scopes           []string                      `json:"scopes"`
	SubscriptionType *oauthclient.SubscriptionType `json:"subscriptionType"`
	RateLimitTier    *oauthclient.RateLimitTier    `json:"rateLimitTier"`
}

// Tokens is the runtime OAuth token shape returned by storage discovery.
type Tokens struct {
	AccessToken      string
	RefreshToken     string
	ExpiresAt        *time.Time
	Scopes           []string
	SubscriptionType *oauthclient.SubscriptionType
	RateLimitTier    *oauthclient.RateLimitTier
}

// MillisTime stores timestamps as Unix milliseconds, matching TypeScript Date.now() values.
type MillisTime struct {
	time.Time
}

// NewStore creates an OAuth credential store at a specific credentials path.
func NewStore(path string) *Store {
	return &Store{Path: path}
}

// CredentialsPath returns the plaintext secure-storage path for a Claude config home directory.
func CredentialsPath(configHome string) string {
	return filepath.Join(configHome, CredentialsFileName)
}

// GetClaudeAIOAuthTokens returns tokens using the TypeScript discovery order: env, file descriptor, storage.
func (s *Store) GetClaudeAIOAuthTokens() (*Tokens, error) {
	s.normalize()
	if s.BareMode {
		return nil, nil
	}
	if token := s.Getenv(EnvOAuthToken); token != "" {
		return inferenceOnlyTokens(token), nil
	}
	if token := s.DescriptorReader.Credential(authfiledescriptor.OAuthTokenSource); token != "" {
		return inferenceOnlyTokens(token), nil
	}
	data, err := s.Read()
	if err != nil {
		return nil, err
	}
	if data.ClaudeAIOAuth == nil || data.ClaudeAIOAuth.AccessToken == "" {
		return nil, nil
	}
	return data.ClaudeAIOAuth.RuntimeTokens(), nil
}

// SaveOAuthTokensIfNeeded stores Claude.ai OAuth tokens when they are durable refreshable tokens.
func (s *Store) SaveOAuthTokensIfNeeded(tokens oauthclient.OAuthTokens) UpdateStatus {
	s.normalize()
	if !oauthclient.ShouldUseClaudeAIAuth(tokens.Scopes) {
		return UpdateStatus{Success: true}
	}
	if tokens.RefreshToken == "" || tokens.ExpiresAt.IsZero() {
		return UpdateStatus{Success: true}
	}
	data, err := s.Read()
	if err != nil {
		data = Data{}
	}
	var existingSub *oauthclient.SubscriptionType
	var existingTier *oauthclient.RateLimitTier
	if data.ClaudeAIOAuth != nil {
		existingSub = data.ClaudeAIOAuth.SubscriptionType
		existingTier = data.ClaudeAIOAuth.RateLimitTier
	}
	subscriptionType := tokens.SubscriptionType
	if subscriptionType == nil {
		subscriptionType = existingSub
	}
	rateLimitTier := tokens.RateLimitTier
	if rateLimitTier == nil {
		rateLimitTier = existingTier
	}
	data.ClaudeAIOAuth = &StoredTokens{
		AccessToken:      tokens.AccessToken,
		RefreshToken:     tokens.RefreshToken,
		ExpiresAt:        &MillisTime{Time: tokens.ExpiresAt},
		Scopes:           append([]string(nil), tokens.Scopes...),
		SubscriptionType: subscriptionType,
		RateLimitTier:    rateLimitTier,
	}
	return s.Update(data)
}

// Read reads credentials from plaintext storage, returning empty data for a missing file.
func (s *Store) Read() (Data, error) {
	s.normalize()
	content, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return Data{}, nil
		}
		return Data{}, err
	}
	if len(content) == 0 {
		return Data{}, nil
	}
	var data Data
	if err := json.Unmarshal(content, &data); err != nil {
		return Data{}, err
	}
	return data, nil
}

// Update writes credentials to plaintext storage with 0600 permissions.
func (s *Store) Update(data Data) UpdateStatus {
	s.normalize()
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return UpdateStatus{Success: false}
	}
	content, err := json.Marshal(data)
	if err != nil {
		return UpdateStatus{Success: false}
	}
	if err := os.WriteFile(s.Path, content, 0o600); err != nil {
		return UpdateStatus{Success: false}
	}
	if err := os.Chmod(s.Path, 0o600); err != nil {
		return UpdateStatus{Success: false}
	}
	return UpdateStatus{Success: true, Warning: PlainTextWarning}
}

// Delete removes the plaintext credentials file and treats missing files as success.
func (s *Store) Delete() bool {
	s.normalize()
	if err := os.Remove(s.Path); err != nil {
		return os.IsNotExist(err)
	}
	return true
}

// MarshalJSON preserves unknown top-level credentials fields while updating claudeAiOauth.
func (d Data) MarshalJSON() ([]byte, error) {
	fields := map[string]json.RawMessage{}
	maps.Copy(fields, d.Extra)
	if d.ClaudeAIOAuth != nil {
		content, err := json.Marshal(d.ClaudeAIOAuth)
		if err != nil {
			return nil, err
		}
		fields["claudeAiOauth"] = content
	}
	return json.Marshal(fields)
}

// UnmarshalJSON preserves unknown top-level credentials fields.
func (d *Data) UnmarshalJSON(content []byte) error {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(content, &fields); err != nil {
		return err
	}
	d.Extra = map[string]json.RawMessage{}
	for key, value := range fields {
		if key == "claudeAiOauth" {
			if string(value) != "null" {
				var tokens StoredTokens
				if err := json.Unmarshal(value, &tokens); err != nil {
					return err
				}
				d.ClaudeAIOAuth = &tokens
			}
			continue
		}
		d.Extra[key] = value
	}
	return nil
}

// RuntimeTokens converts persisted tokens to the runtime shape.
func (t StoredTokens) RuntimeTokens() *Tokens {
	var expiresAt *time.Time
	if t.ExpiresAt != nil {
		value := t.ExpiresAt.Time
		expiresAt = &value
	}
	return &Tokens{
		AccessToken:      t.AccessToken,
		RefreshToken:     t.RefreshToken,
		ExpiresAt:        expiresAt,
		Scopes:           append([]string(nil), t.Scopes...),
		SubscriptionType: t.SubscriptionType,
		RateLimitTier:    t.RateLimitTier,
	}
}

// MarshalJSON writes a timestamp as Unix milliseconds.
func (t MillisTime) MarshalJSON() ([]byte, error) {
	return fmt.Appendf(nil, "%d", t.UnixMilli()), nil
}

// UnmarshalJSON reads a timestamp from Unix milliseconds or null.
func (t *MillisTime) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var millis int64
	if err := json.Unmarshal(data, &millis); err != nil {
		return err
	}
	t.Time = time.UnixMilli(millis)
	return nil
}

func (s *Store) normalize() {
	if s.Getenv == nil {
		s.Getenv = os.Getenv
	}
	if s.DescriptorReader == nil {
		s.DescriptorReader = authfiledescriptor.DefaultReader()
	}
}

func inferenceOnlyTokens(accessToken string) *Tokens {
	return &Tokens{
		AccessToken:      accessToken,
		Scopes:           []string{oauthconfig.ClaudeAIInferenceScope},
		SubscriptionType: nil,
		RateLimitTier:    nil,
	}
}
