package oauthclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"code-cli/internal/core"
	"code-cli/internal/oauthconfig"
)

func TestBuildAuthURL(t *testing.T) {
	config := testOAuthConfig("https://api.example.test")
	got, err := BuildAuthURL(config, BuildAuthURLOptions{
		CodeChallenge:     "challenge",
		State:             "state",
		Port:              4321,
		LoginWithClaudeAI: true,
		OrgUUID:           "org_123",
		LoginHint:         "user@example.com",
		LoginMethod:       "sso",
	})
	if err != nil {
		t.Fatalf("BuildAuthURL() error = %v", err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != config.ClaudeAIAuthorizeURL {
		t.Fatalf("auth url base = %q", parsed.Scheme+"://"+parsed.Host+parsed.Path)
	}
	query := parsed.Query()
	checks := map[string]string{
		"code":                  "true",
		"client_id":             config.ClientID,
		"response_type":         "code",
		"redirect_uri":          "http://localhost:4321/callback",
		"scope":                 strings.Join(oauthconfig.AllOAuthScopes, " "),
		"code_challenge":        "challenge",
		"code_challenge_method": "S256",
		"state":                 "state",
		"orgUUID":               "org_123",
		"login_hint":            "user@example.com",
		"login_method":          "sso",
	}
	for key, want := range checks {
		if got := query.Get(key); got != want {
			t.Fatalf("query %s = %q, want %q", key, got, want)
		}
	}
}

func TestBuildAuthURLManualInferenceOnly(t *testing.T) {
	config := testOAuthConfig("https://api.example.test")
	got, err := BuildAuthURL(config, BuildAuthURLOptions{
		CodeChallenge: "challenge",
		State:         "state",
		Port:          4321,
		IsManual:      true,
		InferenceOnly: true,
	})
	if err != nil {
		t.Fatalf("BuildAuthURL() error = %v", err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	query := parsed.Query()
	if query.Get("redirect_uri") != config.ManualRedirectURL {
		t.Fatalf("redirect_uri = %q", query.Get("redirect_uri"))
	}
	if query.Get("scope") != oauthconfig.ClaudeAIInferenceScope {
		t.Fatalf("scope = %q", query.Get("scope"))
	}
}

func TestExchangeCodeForTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/oauth/token" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		want := map[string]any{
			"grant_type":    "authorization_code",
			"code":          "code_123",
			"redirect_uri":  "http://localhost:7654/callback",
			"client_id":     "client_123",
			"code_verifier": "verifier",
			"state":         "state_123",
			"expires_in":    float64(3600),
		}
		if !reflect.DeepEqual(body, want) {
			t.Fatalf("body = %#v, want %#v", body, want)
		}
		_, _ = w.Write([]byte(`{"access_token":"access","refresh_token":"refresh","expires_in":3600,"scope":"user:profile user:inference","account":{"uuid":"acct_123","email_address":"user@example.com"},"organization":{"uuid":"org_123"}}`))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	expiresIn := 3600
	response, err := client.ExchangeCodeForTokens(context.Background(), "code_123", "state_123", "verifier", 7654, false, ExchangeCodeOptions{ExpiresIn: &expiresIn})
	if err != nil {
		t.Fatalf("ExchangeCodeForTokens() error = %v", err)
	}
	if response.AccessToken != "access" || response.RefreshToken != "refresh" || response.Account.UUID != "acct_123" || response.Organization.UUID != "org_123" {
		t.Fatalf("response = %#v", response)
	}
}

func TestExchangeCodeForTokensUnauthorizedMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad code"}}`))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	_, err := client.ExchangeCodeForTokens(context.Background(), "code", "state", "verifier", 1234, false, ExchangeCodeOptions{})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiErr.Kind != core.APIErrorAuth || apiErr.Message != "authentication failed: invalid authorization code" {
		t.Fatalf("api error = %#v", apiErr)
	}
}

func TestRefreshOAuthToken(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		switch r.URL.Path {
		case "/v1/oauth/token":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode refresh body: %v", err)
			}
			if body["grant_type"] != "refresh_token" || body["refresh_token"] != "old_refresh" || body["scope"] != strings.Join(oauthconfig.ClaudeAIOAuthScopes, " ") {
				t.Fatalf("refresh body = %#v", body)
			}
			_, _ = w.Write([]byte(`{"access_token":"new_access","expires_in":60,"scope":"user:profile user:inference","account":{"uuid":"acct_123","email_address":"user@example.com"},"organization":{"uuid":"org_123"}}`))
		case "/api/oauth/profile":
			if r.Header.Get("Authorization") != "Bearer new_access" || r.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("profile headers = %#v", r.Header)
			}
			_, _ = w.Write([]byte(`{"account":{"uuid":"acct_123","email":"user@example.com","display_name":"Ada","created_at":"2026-01-01T00:00:00Z"},"organization":{"uuid":"org_123","organization_type":"claude_max","rate_limit_tier":"default_claude_max_5x","has_extra_usage_enabled":true,"billing_type":"stripe_subscription","subscription_created_at":"2026-02-01T00:00:00Z"}}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	client.now = func() time.Time { return now }
	tokens, err := client.RefreshOAuthToken(context.Background(), "old_refresh", RefreshOptions{})
	if err != nil {
		t.Fatalf("RefreshOAuthToken() error = %v", err)
	}
	if !reflect.DeepEqual(requests, []string{"/v1/oauth/token", "/api/oauth/profile"}) {
		t.Fatalf("requests = %#v", requests)
	}
	if tokens.AccessToken != "new_access" || tokens.RefreshToken != "old_refresh" || !tokens.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("tokens = %#v", tokens)
	}
	if tokens.SubscriptionType == nil || *tokens.SubscriptionType != SubscriptionMax {
		t.Fatalf("subscription = %#v", tokens.SubscriptionType)
	}
	if tokens.TokenAccount == nil || tokens.TokenAccount.OrganizationUUID != "org_123" {
		t.Fatalf("token account = %#v", tokens.TokenAccount)
	}
	if !reflect.DeepEqual(tokens.Scopes, []string{"user:profile", "user:inference"}) {
		t.Fatalf("scopes = %#v", tokens.Scopes)
	}
}

func TestFetchOAuthProfileFromAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/claude_cli_profile" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("account_uuid") != "acct_123" {
			t.Fatalf("account_uuid = %q", r.URL.Query().Get("account_uuid"))
		}
		if r.Header.Get("x-api-key") != "sk-ant" || r.Header.Get("anthropic-beta") != oauthconfig.OAuthBetaHeader {
			t.Fatalf("headers = %#v", r.Header)
		}
		_, _ = w.Write([]byte(`{"account":{"uuid":"acct_123"},"organization":{"uuid":"org_123"}}`))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	profile, err := client.FetchOAuthProfileFromAPIKey(context.Background(), "sk-ant", "acct_123")
	if err != nil {
		t.Fatalf("FetchOAuthProfileFromAPIKey() error = %v", err)
	}
	if profile.Account.UUID != "acct_123" || profile.Organization.UUID != "org_123" {
		t.Fatalf("profile = %#v", profile)
	}
}

func TestFetchUserRolesAndCreateAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/oauth/claude_cli/roles":
			_, _ = w.Write([]byte(`{"organization_role":"admin","workspace_role":"developer","organization_name":"Anthropic"}`))
		case "/api/oauth/claude_cli/create_api_key":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %q", r.Method)
			}
			_, _ = w.Write([]byte(`{"raw_key":"sk-ant-123"}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	roles, err := client.FetchUserRoles(context.Background(), "access")
	if err != nil {
		t.Fatalf("FetchUserRoles() error = %v", err)
	}
	if roles.OrganizationRole != "admin" || roles.WorkspaceRole != "developer" || roles.OrganizationName != "Anthropic" {
		t.Fatalf("roles = %#v", roles)
	}
	apiKey, ok, err := client.CreateAPIKey(context.Background(), "access")
	if err != nil {
		t.Fatalf("CreateAPIKey() error = %v", err)
	}
	if !ok || apiKey != "sk-ant-123" {
		t.Fatalf("api key = %q ok=%v", apiKey, ok)
	}
}

func TestHelpers(t *testing.T) {
	if !reflect.DeepEqual(ParseScopes(" user:profile  user:inference "), []string{"user:profile", "user:inference"}) {
		t.Fatalf("ParseScopes mismatch")
	}
	if !ShouldUseClaudeAIAuth([]string{"user:profile", "user:inference"}) {
		t.Fatalf("ShouldUseClaudeAIAuth = false")
	}
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	soon := now.Add(4 * time.Minute)
	later := now.Add(6 * time.Minute)
	if !IsOAuthTokenExpired(&soon, now) {
		t.Fatalf("soon token should be expired")
	}
	if IsOAuthTokenExpired(&later, now) {
		t.Fatalf("later token should not be expired")
	}
	if IsOAuthTokenExpired(nil, now) {
		t.Fatalf("nil expiration should not be expired")
	}
}

func TestProfileInfoFromProfile(t *testing.T) {
	for orgType, want := range map[string]*SubscriptionType{
		"claude_max":        new(SubscriptionMax),
		"claude_pro":        new(SubscriptionPro),
		"claude_enterprise": new(SubscriptionEnterprise),
		"claude_team":       new(SubscriptionTeam),
		"unknown":           nil,
	} {
		t.Run(orgType, func(t *testing.T) {
			info := ProfileInfoFromProfile(&OAuthProfileResponse{Organization: ProfileOrganization{OrganizationType: orgType}})
			if want == nil {
				if info.SubscriptionType != nil {
					t.Fatalf("SubscriptionType = %#v", info.SubscriptionType)
				}
				return
			}
			if info.SubscriptionType == nil || *info.SubscriptionType != *want {
				t.Fatalf("SubscriptionType = %#v, want %s", info.SubscriptionType, *want)
			}
		})
	}
}

func TestErrorNormalization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-request-id", "req_123")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	_, err := client.FetchUserRoles(context.Background(), "access")
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiErr.Kind != core.APIErrorRateLimit || apiErr.StatusCode != http.StatusTooManyRequests || apiErr.RequestID != "req_123" || !apiErr.Retryable || apiErr.Message != "slow down" {
		t.Fatalf("api error = %#v", apiErr)
	}
}

func TestNewClientValidation(t *testing.T) {
	config := testOAuthConfig("https://api.example.test")
	config.TokenURL = "://bad"
	if _, err := NewClient(Config{OAuthConfig: config}); err == nil {
		t.Fatalf("expected invalid token URL error")
	}
	config = testOAuthConfig("https://api.example.test")
	config.ClientID = ""
	if _, err := NewClient(Config{OAuthConfig: config}); err == nil {
		t.Fatalf("expected missing client ID error")
	}
}

func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(Config{OAuthConfig: testOAuthConfig(server.URL), HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func testOAuthConfig(baseURL string) oauthconfig.Config {
	return oauthconfig.Config{
		BaseAPIURL:           baseURL,
		ConsoleAuthorizeURL:  baseURL + "/oauth/authorize",
		ClaudeAIAuthorizeURL: baseURL + "/cai/oauth/authorize",
		ClaudeAIOrigin:       baseURL,
		TokenURL:             baseURL + "/v1/oauth/token",
		APIKeyURL:            baseURL + "/api/oauth/claude_cli/create_api_key",
		RolesURL:             baseURL + "/api/oauth/claude_cli/roles",
		ConsoleSuccessURL:    baseURL + "/oauth/code/success?app=claude-code",
		ClaudeAISuccessURL:   baseURL + "/oauth/code/success?app=claude-code",
		ManualRedirectURL:    baseURL + "/oauth/code/callback",
		ClientID:             "client_123",
	}
}

//go:fix inline
func ptr[T any](value T) *T {
	return new(value)
}
