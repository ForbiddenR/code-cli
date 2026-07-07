package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchMetricsEnabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/claude_code/organizations/metrics_enabled" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type = %q", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("User-Agent") != "code-cli-test" {
			t.Fatalf("user-agent = %q", r.Header.Get("User-Agent"))
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"metrics_logging_enabled":true}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{
		UserAgent:   "code-cli-test",
		AuthHeaders: map[string]string{"Authorization": "Bearer token"},
	})
	got, err := client.FetchMetricsEnabled(context.Background())
	if err != nil {
		t.Fatalf("FetchMetricsEnabled() error = %v", err)
	}
	if !got.MetricsLoggingEnabled {
		t.Fatalf("metrics = %#v", got)
	}
}

func TestFetchMetricsEnabledAllowsCallHeaderOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/vnd.test+json" {
			t.Fatalf("content-type = %q", r.Header.Get("Content-Type"))
		}
		_, _ = w.Write([]byte(`{"metrics_logging_enabled":false}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.FetchMetricsEnabled(context.Background(), WithHeader("Content-Type", "application/vnd.test+json"))
	if err != nil {
		t.Fatalf("FetchMetricsEnabled() error = %v", err)
	}
	if got.MetricsLoggingEnabled {
		t.Fatalf("metrics = %#v", got)
	}
}
