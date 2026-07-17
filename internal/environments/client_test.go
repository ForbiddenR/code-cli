package environments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"code-cli/internal/core"
	"code-cli/internal/oauthstorage"
	"code-cli/internal/sessionsapi"
	"code-cli/internal/teleportauth"
)

func TestFetchEnvironmentsPreparesAuthAndParsesResponse(t *testing.T) {
	var sawHeaders bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/environment_providers" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != sessionsapi.AnthropicVersion {
			t.Fatalf("anthropic-version = %q", got)
		}
		if got := r.Header.Get("x-organization-uuid"); got != "org_env" {
			t.Fatalf("x-organization-uuid = %q", got)
		}
		if got := r.Header.Get("anthropic-beta"); got != "" {
			t.Fatalf("unexpected anthropic-beta = %q", got)
		}
		sawHeaders = true
		fmt.Fprint(w, `{"environments":[{"kind":"anthropic_cloud","environment_id":"env_1","name":"Cloud","created_at":"2026-01-01T00:00:00Z","state":"active"},{"kind":"bridge","environment_id":"env_2","name":"Bridge","created_at":"2026-01-02T00:00:00Z","state":"active"}],"has_more":false,"first_id":"env_1","last_id":"env_2"}`)
	}))
	defer server.Close()

	getter := &countingTokenGetter{tokens: &oauthstorage.Tokens{AccessToken: "access"}}
	client := newPreparedClient(t, server.URL, getter)
	environments, err := client.FetchEnvironments(context.Background())
	if err != nil {
		t.Fatalf("FetchEnvironments() error = %v", err)
	}
	if !sawHeaders || len(environments) != 2 || environments[0].EnvironmentID != "env_1" || environments[1].Kind != KindBridge {
		t.Fatalf("environments = %#v sawHeaders = %v", environments, sawHeaders)
	}
	if getter.calls != 1 {
		t.Fatalf("token getter calls = %d", getter.calls)
	}
}

func TestCreateDefaultCloudEnvironmentRequest(t *testing.T) {
	var sawRequest bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/environment_providers/cloud/create" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("anthropic-beta"); got != CCRBYOCBeta {
			t.Fatalf("anthropic-beta = %q", got)
		}
		var request CreateDefaultCloudRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Name != "Default" || request.Kind != KindAnthropicCloud || request.Description != "" {
			t.Fatalf("request = %#v", request)
		}
		if request.Config.EnvironmentType != "anthropic" || request.Config.CWD != "/home/user" || request.Config.InitScript != nil {
			t.Fatalf("config = %#v", request.Config)
		}
		if len(request.Config.Environment) != 0 || len(request.Config.NetworkConfig.AllowedHosts) != 0 || !request.Config.NetworkConfig.AllowDefaultHosts {
			t.Fatalf("environment/network = %#v", request.Config)
		}
		if len(request.Config.Languages) != 2 || request.Config.Languages[0] != (Language{Name: "python", Version: "3.11"}) || request.Config.Languages[1] != (Language{Name: "node", Version: "20"}) {
			t.Fatalf("languages = %#v", request.Config.Languages)
		}
		sawRequest = true
		fmt.Fprint(w, `{"kind":"anthropic_cloud","environment_id":"env_new","name":"Default","created_at":"2026-01-01T00:00:00Z","state":"active"}`)
	}))
	defer server.Close()

	client := newPreparedClient(t, server.URL, &countingTokenGetter{tokens: &oauthstorage.Tokens{AccessToken: "access"}})
	environment, err := client.CreateDefaultCloudEnvironment(context.Background(), "Default")
	if err != nil {
		t.Fatalf("CreateDefaultCloudEnvironment() error = %v", err)
	}
	if !sawRequest || environment.EnvironmentID != "env_new" || environment.Name != "Default" {
		t.Fatalf("environment = %#v sawRequest = %v", environment, sawRequest)
	}
}

func TestDefaultCloudEnvironmentRequestJSONShape(t *testing.T) {
	request := DefaultCloudEnvironmentRequest("Default")
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	assertJSONEqual(t, string(data), `{"name":"Default","kind":"anthropic_cloud","description":"","config":{"environment_type":"anthropic","cwd":"/home/user","init_script":null,"environment":{},"languages":[{"name":"python","version":"3.11"},{"name":"node","version":"20"}],"network_config":{"allowed_hosts":[],"allow_default_hosts":true}}}`)
}

func TestCreateDefaultCloudEnvironmentValidatesName(t *testing.T) {
	client := newPreparedClient(t, "https://api.example.com", &countingTokenGetter{tokens: &oauthstorage.Tokens{AccessToken: "access"}})
	_, err := client.CreateDefaultCloudEnvironment(context.Background(), "  ")
	if err == nil || err.Error() != "environment name is required" {
		t.Fatalf("err = %v", err)
	}
}

func TestClientPreparationErrorsStopBeforeHTTP(t *testing.T) {
	serverCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalled = true
	}))
	defer server.Close()

	client := newPreparedClient(t, server.URL, &countingTokenGetter{})
	_, err := client.FetchEnvironments(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Claude Code web sessions require authentication") {
		t.Fatalf("err = %v", err)
	}
	if serverCalled {
		t.Fatal("server was called after preparation error")
	}
}

func TestClientMissingPreparer(t *testing.T) {
	client, err := NewClient(Config{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.FetchEnvironments(context.Background())
	if !errors.Is(err, teleportauth.ErrMissingPreparer) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewClientValidationAndDefaults(t *testing.T) {
	client, err := NewClient(Config{Preparer: teleportauth.NewPreparer(teleportauth.Config{})})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client.baseURL.String() != DefaultBaseURL || client.timeout != DefaultTimeout {
		t.Fatalf("client = %#v", client)
	}
	customClient := &http.Client{Timeout: time.Second}
	client, err = NewClient(Config{BaseURL: "https://example.com/base", HTTPClient: customClient, Timeout: time.Second, Preparer: teleportauth.NewPreparer(teleportauth.Config{})})
	if err != nil {
		t.Fatalf("NewClient(custom) error = %v", err)
	}
	if client.baseURL.String() != "https://example.com/base" || client.httpClient != customClient || client.timeout != time.Second {
		t.Fatalf("custom client = %#v", client)
	}
	_, err = NewClient(Config{BaseURL: "api.anthropic.com"})
	if err == nil || !strings.Contains(err.Error(), "missing scheme or host") {
		t.Fatalf("err = %v", err)
	}
}

func TestEndpointPreservesBasePath(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "https://example.com/base/", Preparer: teleportauth.NewPreparer(teleportauth.Config{})})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if got := client.endpoint("/v1/environment_providers"); got != "https://example.com/base/v1/environment_providers" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestFetchEnvironmentsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-request-id", "req_1")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"message":"forbidden"}}`)
	}))
	defer server.Close()

	client := newPreparedClient(t, server.URL, &countingTokenGetter{tokens: &oauthstorage.Tokens{AccessToken: "access"}})
	_, err := client.FetchEnvironments(context.Background())
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != core.APIErrorPermission || apiErr.Message != "forbidden" || apiErr.RequestID != "req_1" {
		t.Fatalf("err = %#v", err)
	}
}

func TestFetchEnvironmentsDecodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{`)
	}))
	defer server.Close()

	client := newPreparedClient(t, server.URL, &countingTokenGetter{tokens: &oauthstorage.Tokens{AccessToken: "access"}})
	_, err := client.FetchEnvironments(context.Background())
	if err == nil || !strings.Contains(err.Error(), "decode environment response") {
		t.Fatalf("err = %v", err)
	}
}

func newPreparedClient(t *testing.T, baseURL string, getter *countingTokenGetter) *Client {
	t.Helper()
	client, err := NewClient(Config{
		BaseURL: baseURL,
		Preparer: teleportauth.NewPreparer(teleportauth.Config{
			TokenGetter: getter,
			Getenv: func(key string) string {
				if key == teleportauth.EnvOrganizationUUID {
					return "org_env"
				}
				return ""
			},
		}),
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

type countingTokenGetter struct {
	tokens *oauthstorage.Tokens
	err    error
	calls  int
}

func (g *countingTokenGetter) GetClaudeAIOAuthTokens() (*oauthstorage.Tokens, error) {
	g.calls++
	return g.tokens, g.err
}

func assertJSONEqual(t *testing.T, got string, want string) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	var wantValue any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	gotData, err := json.Marshal(gotValue)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	wantData, err := json.Marshal(wantValue)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if string(gotData) != string(wantData) {
		t.Fatalf("json = %s, want %s", gotData, wantData)
	}
}
