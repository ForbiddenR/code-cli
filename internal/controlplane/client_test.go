package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"code-cli/internal/core"
)

func TestClientMergesHeadersWithoutMutatingInputs(t *testing.T) {
	defaultHeaders := map[string]string{"x-default": "1"}
	authHeaders := map[string]string{"authorization": "Bearer token"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-default") != "1" {
			t.Fatalf("x-default = %q", r.Header.Get("x-default"))
		}
		if r.Header.Get("authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", r.Header.Get("authorization"))
		}
		if r.Header.Get("x-call") != "2" {
			t.Fatalf("x-call = %q", r.Header.Get("x-call"))
		}
		if r.Header.Get("user-agent") != "code-cli-test" {
			t.Fatalf("user-agent = %q", r.Header.Get("user-agent"))
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{
		UserAgent:      "code-cli-test",
		DefaultHeaders: defaultHeaders,
		AuthHeaders:    authHeaders,
	})
	var out map[string]bool
	if err := client.doJSON(context.Background(), http.MethodGet, "/test", nil, nil, &out, WithHeader("x-call", "2")); err != nil {
		t.Fatalf("doJSON() error = %v", err)
	}
	if !out["ok"] {
		t.Fatalf("out = %#v", out)
	}
	if _, ok := defaultHeaders["x-call"]; ok {
		t.Fatalf("default headers mutated: %#v", defaultHeaders)
	}
}

func TestClientErrorNormalization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("request-id", "req_123")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	err := client.doJSON(context.Background(), http.MethodGet, "/test", nil, nil, nil)
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiErr.Kind != core.APIErrorRateLimit || apiErr.StatusCode != http.StatusTooManyRequests || apiErr.RequestID != "req_123" || !apiErr.Retryable {
		t.Fatalf("api error = %#v", apiErr)
	}
	if apiErr.Message != "slow down" {
		t.Fatalf("message = %q", apiErr.Message)
	}
}

func TestNewClientDefaultBaseURL(t *testing.T) {
	client, err := NewClient(Config{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if got := client.endpoint("/api/oauth/usage", nil); got != DefaultBaseURL+"/api/oauth/usage" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestNewClientRejectsInvalidBaseURL(t *testing.T) {
	if _, err := NewClient(Config{BaseURL: "://bad"}); err == nil {
		t.Fatalf("expected invalid base URL error")
	}
}

func TestClientEndpointJoinsBasePath(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "https://example.test/base/"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if got := client.endpoint("/api/oauth/usage", nil); got != "https://example.test/base/api/oauth/usage" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestClientRequestIDFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-request-id", "req_fallback")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	err := client.doJSON(context.Background(), http.MethodGet, "/test", nil, nil, nil)
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiErr.RequestID != "req_fallback" || apiErr.Kind != core.APIErrorServer {
		t.Fatalf("api error = %#v", apiErr)
	}
}

func TestClientMalformedSuccessJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	var out map[string]any
	if err := client.doJSON(context.Background(), http.MethodGet, "/test", nil, nil, &out); err == nil {
		t.Fatalf("expected decode error")
	}
}

func TestClientEmptySuccessBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	var out map[string]any
	if err := client.doJSON(context.Background(), http.MethodGet, "/test", nil, nil, &out); err == nil {
		t.Fatalf("expected decode error")
	}
}

func newTestClient(t *testing.T, server *httptest.Server, config Config) *Client {
	t.Helper()
	config.BaseURL = server.URL
	config.HTTPClient = server.Client()
	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}
