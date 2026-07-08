package sessioningress

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"code-cli/internal/core"
)

func TestFetchSessionLogs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/session_ingress/session/session_123" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("after_last_compact") != "true" {
			t.Fatalf("after_last_compact = %q", r.URL.Query().Get("after_last_compact"))
		}
		_, _ = w.Write([]byte(`{"loglines":[{"uuid":"uuid_1","type":"user"},{"type":"summary"},{"uuid":"uuid_2","type":"assistant"}]}`))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	client.afterLastCompact = true
	got, err := client.FetchSessionLogs(context.Background(), "session_123")
	if err != nil {
		t.Fatalf("FetchSessionLogs() error = %v", err)
	}
	if len(got) != 3 || !strings.Contains(string(got[0]), "uuid_1") {
		t.Fatalf("logs = %#v", got)
	}
	if client.lastUUIDBySession["session_123"] != "uuid_2" {
		t.Fatalf("last uuid = %q", client.lastUUIDBySession["session_123"])
	}
}

func TestFetchSessionLogsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got, err := client.FetchSessionLogs(context.Background(), "session_missing")
	if err != nil {
		t.Fatalf("FetchSessionLogs() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("logs = %#v", got)
	}
}

func TestFetchSessionLogsUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	_, err := client.FetchSessionLogs(context.Background(), "session_123")
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != core.APIErrorAuth || apiErr.Retryable {
		t.Fatalf("error = %#v", err)
	}
}

func TestAppendSessionLog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/session_ingress/session/session_123" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type = %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	ok, err := client.AppendSessionLog(context.Background(), "session_123", TranscriptEntry{UUID: "uuid_1", Body: []byte(`{"uuid":"uuid_1"}`)})
	if err != nil || !ok {
		t.Fatalf("AppendSessionLog() = %v, %v", ok, err)
	}
	if client.lastUUIDBySession["session_123"] != "uuid_1" {
		t.Fatalf("last uuid = %q", client.lastUUIDBySession["session_123"])
	}
}

func TestAppendSessionLogAdoptsConflictHeader(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("x-last-uuid", "server_uuid")
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		if r.Header.Get("Last-Uuid") != "server_uuid" {
			t.Fatalf("Last-Uuid = %q", r.Header.Get("Last-Uuid"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	ok, err := client.AppendSessionLog(context.Background(), "session_123", TranscriptEntry{UUID: "uuid_2", Body: []byte(`{"uuid":"uuid_2"}`)})
	if err != nil || !ok || calls != 2 {
		t.Fatalf("AppendSessionLog() = %v, %v, calls = %d", ok, err, calls)
	}
	if client.lastUUIDBySession["session_123"] != "uuid_2" {
		t.Fatalf("last uuid = %q", client.lastUUIDBySession["session_123"])
	}
}

func TestAppendSessionLogAlreadyPresentConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-last-uuid", "uuid_1")
		http.Error(w, "conflict", http.StatusConflict)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	ok, err := client.AppendSessionLog(context.Background(), "session_123", TranscriptEntry{UUID: "uuid_1", Body: []byte(`{"uuid":"uuid_1"}`)})
	if err != nil || !ok {
		t.Fatalf("AppendSessionLog() = %v, %v", ok, err)
	}
}

func TestAppendSessionLogRefetchesOnConflictWithoutHeader(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		if calls == 2 {
			if r.Method != http.MethodGet {
				t.Fatalf("second method = %s", r.Method)
			}
			_, _ = w.Write([]byte(`{"loglines":[{"uuid":"server_uuid"}]}`))
			return
		}
		if r.Header.Get("Last-Uuid") != "server_uuid" {
			t.Fatalf("Last-Uuid = %q", r.Header.Get("Last-Uuid"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	ok, err := client.AppendSessionLog(context.Background(), "session_123", TranscriptEntry{UUID: "uuid_3", Body: []byte(`{"uuid":"uuid_3"}`)})
	if err != nil || !ok || calls != 3 {
		t.Fatalf("AppendSessionLog() = %v, %v, calls = %d", ok, err, calls)
	}
}

func TestClearSessions(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "https://example.test"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	client.lastUUIDBySession["one"] = "uuid_1"
	client.lastUUIDBySession["two"] = "uuid_2"
	client.ClearSession("one")
	if _, ok := client.lastUUIDBySession["one"]; ok {
		t.Fatalf("session one was not cleared")
	}
	client.ClearAllSessions()
	if len(client.lastUUIDBySession) != 0 {
		t.Fatalf("last uuid map = %#v", client.lastUUIDBySession)
	}
}

func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(Config{
		BaseURL:    server.URL,
		AuthToken:  "token",
		HTTPClient: server.Client(),
		Sleep:      func(time.Duration) {},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}
