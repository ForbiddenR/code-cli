package oauthstorage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"code-cli/internal/authfiledescriptor"
	"code-cli/internal/oauthclient"
	"code-cli/internal/oauthconfig"
)

func TestGetClaudeAIOAuthTokensPrefersEnvToken(t *testing.T) {
	store := newTestStore(t)
	store.Getenv = func(key string) string {
		if key == EnvOAuthToken {
			return "env_access"
		}
		return ""
	}
	if err := os.WriteFile(store.Path, []byte(`{"claudeAiOauth":{"accessToken":"stored","refreshToken":"refresh","expiresAt":1783512000000,"scopes":["user:profile"]}}`), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	tokens, err := store.GetClaudeAIOAuthTokens()
	if err != nil {
		t.Fatalf("GetClaudeAIOAuthTokens() error = %v", err)
	}
	if tokens.AccessToken != "env_access" || tokens.RefreshToken != "" || tokens.ExpiresAt != nil || !reflect.DeepEqual(tokens.Scopes, []string{oauthconfig.ClaudeAIInferenceScope}) {
		t.Fatalf("tokens = %#v", tokens)
	}
}

func TestGetClaudeAIOAuthTokensUsesFileDescriptorBeforeStorage(t *testing.T) {
	store := newTestStore(t)
	store.DescriptorReader = &authfiledescriptor.Reader{
		Getenv: func(key string) string { return "" },
		ReadFile: func(path string) ([]byte, error) {
			if path == authfiledescriptor.DefaultOAuthTokenPath {
				return []byte("fd_access\n"), nil
			}
			return nil, os.ErrNotExist
		},
	}
	if err := os.WriteFile(store.Path, []byte(`{"claudeAiOauth":{"accessToken":"stored","refreshToken":"refresh","expiresAt":1783512000000,"scopes":["user:profile"]}}`), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	tokens, err := store.GetClaudeAIOAuthTokens()
	if err != nil {
		t.Fatalf("GetClaudeAIOAuthTokens() error = %v", err)
	}
	if tokens.AccessToken != "fd_access" || tokens.RefreshToken != "" || tokens.ExpiresAt != nil {
		t.Fatalf("tokens = %#v", tokens)
	}
}

func TestGetClaudeAIOAuthTokensReadsStorage(t *testing.T) {
	store := newTestStore(t)
	expiresAt := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	if err := os.WriteFile(store.Path, []byte(`{"claudeAiOauth":{"accessToken":"stored_access","refreshToken":"stored_refresh","expiresAt":1783512000000,"scopes":["user:profile","user:inference"],"subscriptionType":"max","rateLimitTier":"default_claude_max_5x"}}`), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	tokens, err := store.GetClaudeAIOAuthTokens()
	if err != nil {
		t.Fatalf("GetClaudeAIOAuthTokens() error = %v", err)
	}
	if tokens.AccessToken != "stored_access" || tokens.RefreshToken != "stored_refresh" || tokens.ExpiresAt == nil || !tokens.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("tokens = %#v", tokens)
	}
	if tokens.SubscriptionType == nil || *tokens.SubscriptionType != oauthclient.SubscriptionMax {
		t.Fatalf("subscription = %#v", tokens.SubscriptionType)
	}
	if tokens.RateLimitTier == nil || *tokens.RateLimitTier != "default_claude_max_5x" {
		t.Fatalf("rateLimitTier = %#v", tokens.RateLimitTier)
	}
}

func TestGetClaudeAIOAuthTokensBareMode(t *testing.T) {
	store := newTestStore(t)
	store.BareMode = true
	store.Getenv = func(key string) string {
		if key == EnvOAuthToken {
			return "env_access"
		}
		return ""
	}
	tokens, err := store.GetClaudeAIOAuthTokens()
	if err != nil {
		t.Fatalf("GetClaudeAIOAuthTokens() error = %v", err)
	}
	if tokens != nil {
		t.Fatalf("tokens = %#v", tokens)
	}
}

func TestSaveOAuthTokensIfNeeded(t *testing.T) {
	store := newTestStore(t)
	expiresAt := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	subscription := oauthclient.SubscriptionMax
	tier := oauthclient.RateLimitTier("default_claude_max_5x")
	status := store.SaveOAuthTokensIfNeeded(oauthclient.OAuthTokens{
		AccessToken:      "access",
		RefreshToken:     "refresh",
		ExpiresAt:        expiresAt,
		Scopes:           []string{oauthconfig.ClaudeAIProfileScope, oauthconfig.ClaudeAIInferenceScope},
		SubscriptionType: &subscription,
		RateLimitTier:    &tier,
	})
	if !status.Success || status.Warning != PlainTextWarning {
		t.Fatalf("status = %#v", status)
	}

	content, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(content, &raw); err != nil {
		t.Fatalf("decode credentials: %v", err)
	}
	oauthData := raw["claudeAiOauth"].(map[string]any)
	if oauthData["accessToken"] != "access" || oauthData["refreshToken"] != "refresh" || oauthData["expiresAt"] != float64(expiresAt.UnixMilli()) {
		t.Fatalf("oauthData = %#v", oauthData)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatalf("stat credentials: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %v", info.Mode().Perm())
	}
}

func TestSaveOAuthTokensIfNeededSkipsNonClaudeAIAndInferenceOnly(t *testing.T) {
	store := newTestStore(t)
	for _, tokens := range []oauthclient.OAuthTokens{
		{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now(), Scopes: []string{oauthconfig.ClaudeAIProfileScope}},
		{AccessToken: "access", Scopes: []string{oauthconfig.ClaudeAIInferenceScope}},
	} {
		status := store.SaveOAuthTokensIfNeeded(tokens)
		if !status.Success || status.Warning != "" {
			t.Fatalf("status = %#v", status)
		}
	}
	if _, err := os.Stat(store.Path); !os.IsNotExist(err) {
		t.Fatalf("credentials stat error = %v", err)
	}
}

func TestSaveOAuthTokensPreservesExistingSubscriptionData(t *testing.T) {
	store := newTestStore(t)
	existingSub := oauthclient.SubscriptionPro
	existingTier := oauthclient.RateLimitTier("tier_a")
	status := store.Update(Data{ClaudeAIOAuth: &StoredTokens{
		AccessToken:      "old_access",
		RefreshToken:     "old_refresh",
		Scopes:           []string{oauthconfig.ClaudeAIInferenceScope},
		SubscriptionType: &existingSub,
		RateLimitTier:    &existingTier,
	}})
	if !status.Success {
		t.Fatalf("Update() = %#v", status)
	}

	store.SaveOAuthTokensIfNeeded(oauthclient.OAuthTokens{
		AccessToken:  "new_access",
		RefreshToken: "new_refresh",
		ExpiresAt:    time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC),
		Scopes:       []string{oauthconfig.ClaudeAIInferenceScope},
	})
	data, err := store.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if data.ClaudeAIOAuth.SubscriptionType == nil || *data.ClaudeAIOAuth.SubscriptionType != existingSub {
		t.Fatalf("subscription = %#v", data.ClaudeAIOAuth.SubscriptionType)
	}
	if data.ClaudeAIOAuth.RateLimitTier == nil || *data.ClaudeAIOAuth.RateLimitTier != existingTier {
		t.Fatalf("tier = %#v", data.ClaudeAIOAuth.RateLimitTier)
	}
}

func TestUpdatePreservesUnknownFieldsAndDelete(t *testing.T) {
	store := newTestStore(t)
	if err := os.WriteFile(store.Path, []byte(`{"other":{"value":true}}`), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	data, err := store.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	data.ClaudeAIOAuth = &StoredTokens{AccessToken: "access", Scopes: []string{oauthconfig.ClaudeAIInferenceScope}}
	if status := store.Update(data); !status.Success {
		t.Fatalf("Update() = %#v", status)
	}
	content, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	if !reflect.DeepEqual(jsonKeys(t, content), []string{"claudeAiOauth", "other"}) {
		t.Fatalf("content = %s", content)
	}
	if !store.Delete() {
		t.Fatalf("Delete() returned false")
	}
	if !store.Delete() {
		t.Fatalf("Delete() missing file returned false")
	}
}

func TestCredentialsPath(t *testing.T) {
	if got := CredentialsPath("/tmp/claude"); got != filepath.Join("/tmp/claude", CredentialsFileName) {
		t.Fatalf("CredentialsPath() = %q", got)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), CredentialsFileName))
	store.Getenv = func(string) string { return "" }
	store.DescriptorReader = &authfiledescriptor.Reader{
		Getenv:   func(string) string { return "" },
		ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
	}
	return store
}

func jsonKeys(t *testing.T, content []byte) []string {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(content, &raw); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	if len(keys) == 2 && keys[0] > keys[1] {
		keys[0], keys[1] = keys[1], keys[0]
	}
	return keys
}
