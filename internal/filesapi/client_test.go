package filesapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"code-cli/internal/core"
)

func TestDownloadFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/files/file_123/content" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("anthropic-version") != AnthropicVersion {
			t.Fatalf("anthropic-version = %q", r.Header.Get("anthropic-version"))
		}
		if r.Header.Get("anthropic-beta") != FilesAPIBetaHeader {
			t.Fatalf("anthropic-beta = %q", r.Header.Get("anthropic-beta"))
		}
		_, _ = w.Write([]byte("hello file"))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got, err := client.DownloadFile(context.Background(), "file_123")
	if err != nil {
		t.Fatalf("DownloadFile() error = %v", err)
	}
	if string(got) != "hello file" {
		t.Fatalf("content = %q", string(got))
	}
}

func TestDownloadFileRejectsEmptyID(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "https://example.test"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.DownloadFile(context.Background(), " ")
	if err == nil || err.Error() != "file id is required" {
		t.Fatalf("DownloadFile() error = %v", err)
	}
}

func TestDownloadFileNonRetryableError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	_, err := client.DownloadFile(context.Background(), "file_404")
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T", err)
	}
	if apiErr.Kind != core.APIErrorInvalidRequest || apiErr.StatusCode != http.StatusNotFound || apiErr.Retryable {
		t.Fatalf("api error = %#v", apiErr)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestDownloadFileRetriesServerError(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "temporary", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got, err := client.DownloadFile(context.Background(), "file_retry")
	if err != nil {
		t.Fatalf("DownloadFile() error = %v", err)
	}
	if string(got) != "ok" || calls != 2 {
		t.Fatalf("content = %q, calls = %d", string(got), calls)
	}
}

func TestListFilesCreatedAfter(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/files" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.URL.Query().Get("after_created_at") != "2026-07-07T00:00:00Z" {
			t.Fatalf("after_created_at = %q", r.URL.Query().Get("after_created_at"))
		}
		paths = append(paths, r.URL.RawQuery)
		if r.URL.Query().Get("after_id") == "" {
			_, _ = w.Write([]byte(`{"data":[{"filename":"a.txt","id":"file_a","size_bytes":10}],"has_more":true}`))
			return
		}
		if r.URL.Query().Get("after_id") != "file_a" {
			t.Fatalf("after_id = %q", r.URL.Query().Get("after_id"))
		}
		_, _ = w.Write([]byte(`{"data":[{"filename":"b.txt","id":"file_b","size_bytes":20}],"has_more":false}`))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got, err := client.ListFilesCreatedAfter(context.Background(), "2026-07-07T00:00:00Z")
	if err != nil {
		t.Fatalf("ListFilesCreatedAfter() error = %v", err)
	}
	if len(got) != 2 || got[0].FileID != "file_a" || got[1].Filename != "b.txt" || got[1].Size != 20 {
		t.Fatalf("files = %#v", got)
	}
	if len(paths) != 2 {
		t.Fatalf("queries = %#v", paths)
	}
}

func TestListFilesCreatedAfterRejectsEmptyTimestamp(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "https://example.test"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.ListFilesCreatedAfter(context.Background(), "")
	if err == nil || err.Error() != "after created at is required" {
		t.Fatalf("ListFilesCreatedAfter() error = %v", err)
	}
}

func TestNewClientDefaultsAndValidation(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("CLAUDE_CODE_API_BASE_URL", "")
	client, err := NewClient(Config{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if got := client.endpoint("/v1/files", nil); got != DefaultBaseURL+"/v1/files" {
		t.Fatalf("endpoint = %q", got)
	}
	if client.timeout != DefaultTimeout || client.uploadTimeout != DefaultUploadTimeout {
		t.Fatalf("timeouts = %s, %s", client.timeout, client.uploadTimeout)
	}
	if _, err := NewClient(Config{BaseURL: "://bad"}); err == nil {
		t.Fatalf("expected invalid base URL error")
	}
}

func TestDefaultBaseURLFromEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://primary.example.test")
	t.Setenv("CLAUDE_CODE_API_BASE_URL", "https://secondary.example.test")
	if got := DefaultBaseURLFromEnv(); got != "https://primary.example.test" {
		t.Fatalf("DefaultBaseURLFromEnv() = %q", got)
	}

	t.Setenv("ANTHROPIC_BASE_URL", "")
	if got := DefaultBaseURLFromEnv(); got != "https://secondary.example.test" {
		t.Fatalf("DefaultBaseURLFromEnv() = %q", got)
	}
}

func TestNewClientConfiguresUploadTimeout(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "https://example.test", Timeout: time.Second, UploadTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client.timeout != time.Second || client.uploadTimeout != 2*time.Second {
		t.Fatalf("timeouts = %s, %s", client.timeout, client.uploadTimeout)
	}
}

func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(Config{
		OAuthToken: "token",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Sleep:      func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}
