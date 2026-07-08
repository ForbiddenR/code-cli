package sessioningress

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
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

func TestConfigFromEnv(t *testing.T) {
	t.Setenv(EnvSessionAccessToken, "env_token")
	t.Setenv(EnvOrganizationUUID, "org_env")

	config := ConfigFromEnv()
	if config.AuthToken != "env_token" || config.OrgUUID != "org_env" {
		t.Fatalf("ConfigFromEnv() = %#v", config)
	}
}

func TestSessionIngressAuthTokenFromCustomFile(t *testing.T) {
	tokenPath := t.TempDir() + "/token"
	if err := os.WriteFile(tokenPath, []byte(" file_token\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	t.Setenv(EnvSessionIngressTokenFile, tokenPath)

	if got := SessionIngressAuthTokenFromEnv(); got != "file_token" {
		t.Fatalf("SessionIngressAuthTokenFromEnv() = %q", got)
	}
}

func TestSessionIngressAuthTokenPrefersEnvOverFile(t *testing.T) {
	tokenPath := t.TempDir() + "/token"
	if err := os.WriteFile(tokenPath, []byte("file_token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	t.Setenv(EnvSessionAccessToken, "env_token")
	t.Setenv(EnvSessionIngressTokenFile, tokenPath)

	if got := SessionIngressAuthTokenFromEnv(); got != "env_token" {
		t.Fatalf("SessionIngressAuthTokenFromEnv() = %q", got)
	}
}

func TestSessionIngressAuthTokenFromFileDescriptor(t *testing.T) {
	tokenPath := t.TempDir() + "/token"
	if err := os.WriteFile(tokenPath, []byte("fd_token\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	file, err := os.Open(tokenPath)
	if err != nil {
		t.Fatalf("open token: %v", err)
	}
	defer file.Close()
	t.Setenv(EnvWebsocketAuthFileDescriptor, strconv.Itoa(int(file.Fd())))

	if got := SessionIngressAuthTokenFromEnv(); got != "fd_token" {
		t.Fatalf("SessionIngressAuthTokenFromEnv() = %q", got)
	}
}

func TestSessionIngressAuthTokenPersistsFileDescriptorTokenForRemoteSubprocesses(t *testing.T) {
	tempDir := t.TempDir()
	sourcePath := tempDir + "/source-token"
	persistPath := tempDir + "/remote/.session_ingress_token"
	if err := os.WriteFile(sourcePath, []byte("fd_token"), 0o600); err != nil {
		t.Fatalf("write source token: %v", err)
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		t.Fatalf("open token: %v", err)
	}
	defer file.Close()
	t.Setenv(EnvRemote, "true")
	t.Setenv(EnvWebsocketAuthFileDescriptor, strconv.Itoa(int(file.Fd())))
	t.Setenv(EnvSessionIngressTokenFile, persistPath)

	if got := SessionIngressAuthTokenFromEnv(); got != "fd_token" {
		t.Fatalf("SessionIngressAuthTokenFromEnv() = %q", got)
	}
	content, err := os.ReadFile(persistPath)
	if err != nil {
		t.Fatalf("read persisted token: %v", err)
	}
	if string(content) != "fd_token" {
		t.Fatalf("persisted token = %q", string(content))
	}
}

func TestSessionIngressAuthTokenDoesNotPersistOutsideRemote(t *testing.T) {
	tempDir := t.TempDir()
	sourcePath := tempDir + "/source-token"
	persistPath := tempDir + "/remote/.session_ingress_token"
	if err := os.WriteFile(sourcePath, []byte("fd_token"), 0o600); err != nil {
		t.Fatalf("write source token: %v", err)
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		t.Fatalf("open token: %v", err)
	}
	defer file.Close()
	t.Setenv(EnvWebsocketAuthFileDescriptor, strconv.Itoa(int(file.Fd())))
	t.Setenv(EnvSessionIngressTokenFile, persistPath)

	if got := SessionIngressAuthTokenFromEnv(); got != "fd_token" {
		t.Fatalf("SessionIngressAuthTokenFromEnv() = %q", got)
	}
	if _, err := os.Stat(persistPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("persisted token stat error = %v", err)
	}
}

func TestSessionIngressAuthTokenFallsBackToFileWhenFileDescriptorFails(t *testing.T) {
	tokenPath := t.TempDir() + "/token"
	if err := os.WriteFile(tokenPath, []byte("fallback_token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	t.Setenv(EnvWebsocketAuthFileDescriptor, "999999")
	t.Setenv(EnvSessionIngressTokenFile, tokenPath)

	if got := SessionIngressAuthTokenFromEnv(); got != "fallback_token" {
		t.Fatalf("SessionIngressAuthTokenFromEnv() = %q", got)
	}
}

func TestSessionIngressAuthTokenRejectsInvalidFileDescriptor(t *testing.T) {
	tokenPath := t.TempDir() + "/token"
	if err := os.WriteFile(tokenPath, []byte("fallback_token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	t.Setenv(EnvWebsocketAuthFileDescriptor, "not-a-fd")
	t.Setenv(EnvSessionIngressTokenFile, tokenPath)

	if got := SessionIngressAuthTokenFromEnv(); got != "" {
		t.Fatalf("SessionIngressAuthTokenFromEnv() = %q", got)
	}
}

func TestNewClientUsesEnvAuthDefaults(t *testing.T) {
	t.Setenv(EnvSessionAccessToken, "env_token")
	t.Setenv(EnvOrganizationUUID, "org_env")

	client, err := NewClient(Config{BaseURL: "https://example.test"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client.authToken != "env_token" || client.orgUUID != "org_env" {
		t.Fatalf("auth = %q, org = %q", client.authToken, client.orgUUID)
	}
}

func TestAuthHeadersUseSessionKeyCookie(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "https://example.test", AuthToken: "sk-ant-sid-token", OrgUUID: "org_123"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	headers := client.authHeaders()
	if headers.Get("Cookie") != "sessionKey=sk-ant-sid-token" {
		t.Fatalf("cookie = %q", headers.Get("Cookie"))
	}
	if headers.Get("X-Organization-Uuid") != "org_123" {
		t.Fatalf("org = %q", headers.Get("X-Organization-Uuid"))
	}
	if headers.Get("Authorization") != "" {
		t.Fatalf("authorization = %q", headers.Get("Authorization"))
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

func TestAppendSessionLogSerializesSameSession(t *testing.T) {
	var mu sync.Mutex
	active := 0
	maxActive := 0
	var order []string
	var lastUUIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		mu.Lock()
		order = append(order, string(body))
		lastUUIDs = append(lastUUIDs, r.Header.Get("Last-Uuid"))
		mu.Unlock()

		time.Sleep(20 * time.Millisecond)

		mu.Lock()
		active--
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	entries := []TranscriptEntry{
		{UUID: "uuid_1", Body: []byte(`{"uuid":"uuid_1"}`)},
		{UUID: "uuid_2", Body: []byte(`{"uuid":"uuid_2"}`)},
		{UUID: "uuid_3", Body: []byte(`{"uuid":"uuid_3"}`)},
	}

	var wg sync.WaitGroup
	for _, entry := range entries {
		wg.Go(func() {
			ok, err := client.AppendSessionLog(context.Background(), "session_123", entry)
			if err != nil || !ok {
				t.Errorf("AppendSessionLog() = %v, %v", ok, err)
			}
		})
	}
	wg.Wait()

	if maxActive != 1 {
		t.Fatalf("max active requests = %d", maxActive)
	}
	if len(order) != len(entries) || len(lastUUIDs) != len(entries) {
		t.Fatalf("order = %#v, Last-Uuid = %#v", order, lastUUIDs)
	}
	for i := 1; i < len(order); i++ {
		previousUUID := findLastUUID([]Entry{Entry(order[i-1])})
		if previousUUID == "" || lastUUIDs[i] != previousUUID {
			t.Fatalf("request %d Last-Uuid = %q, previous body = %s", i, lastUUIDs[i], order[i-1])
		}
	}
}

func TestAppendSessionLogAllowsDifferentSessionsConcurrently(t *testing.T) {
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondDone := make(chan struct{})
	var firstOnce sync.Once
	var secondOnce sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/session_ingress/session/session_one":
			firstOnce.Do(func() { close(firstEntered) })
			<-releaseFirst
			w.WriteHeader(http.StatusCreated)
		case "/v1/session_ingress/session/session_two":
			w.WriteHeader(http.StatusCreated)
			secondOnce.Do(func() { close(secondDone) })
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	go func() {
		ok, err := client.AppendSessionLog(context.Background(), "session_one", TranscriptEntry{UUID: "uuid_1", Body: []byte(`{"uuid":"uuid_1"}`)})
		if err != nil || !ok {
			t.Errorf("first AppendSessionLog() = %v, %v", ok, err)
		}
	}()

	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first session append did not start")
	}

	go func() {
		ok, err := client.AppendSessionLog(context.Background(), "session_two", TranscriptEntry{UUID: "uuid_2", Body: []byte(`{"uuid":"uuid_2"}`)})
		if err != nil || !ok {
			t.Errorf("second AppendSessionLog() = %v, %v", ok, err)
		}
	}()

	select {
	case <-secondDone:
	case <-time.After(time.Second):
		close(releaseFirst)
		t.Fatal("second session append was blocked by first session")
	}
	close(releaseFirst)
}

func TestFetchSessionTranscriptUsesTeleportEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/code/sessions/session_123/teleport-events" {
			t.Fatalf("unexpected fallback request = %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`{"data":[{"payload":{"uuid":"uuid_1"}}]}`))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got, source, err := client.FetchSessionTranscript(context.Background(), "session_123")
	if err != nil {
		t.Fatalf("FetchSessionTranscript() error = %v", err)
	}
	if source != TranscriptSourceTeleportEvents {
		t.Fatalf("source = %q", source)
	}
	if len(got) != 1 || !strings.Contains(string(got[0]), "uuid_1") {
		t.Fatalf("entries = %#v", got)
	}
}

func TestFetchSessionTranscriptFallsBackToSessionIngressOnTeleportNotFound(t *testing.T) {
	var sawFallback bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/code/sessions/session_123/teleport-events":
			http.NotFound(w, nil)
		case "/v1/session_ingress/session/session_123":
			sawFallback = true
			_, _ = w.Write([]byte(`{"loglines":[{"uuid":"uuid_legacy"}]}`))
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got, source, err := client.FetchSessionTranscript(context.Background(), "session_123")
	if err != nil {
		t.Fatalf("FetchSessionTranscript() error = %v", err)
	}
	if source != TranscriptSourceSessionIngress || !sawFallback {
		t.Fatalf("source = %q, saw fallback = %v", source, sawFallback)
	}
	if len(got) != 1 || !strings.Contains(string(got[0]), "uuid_legacy") {
		t.Fatalf("entries = %#v", got)
	}
}

func TestFetchSessionTranscriptFallsBackToSessionIngressOnTeleportServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/code/sessions/session_123/teleport-events":
			http.Error(w, "temporary", http.StatusInternalServerError)
		case "/v1/session_ingress/session/session_123":
			_, _ = w.Write([]byte(`{"loglines":[{"uuid":"uuid_legacy"}]}`))
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got, source, err := client.FetchSessionTranscript(context.Background(), "session_123")
	if err != nil {
		t.Fatalf("FetchSessionTranscript() error = %v", err)
	}
	if source != TranscriptSourceSessionIngress || len(got) != 1 {
		t.Fatalf("source = %q, entries = %#v", source, got)
	}
}

func TestFetchSessionTranscriptDoesNotFallBackOnTeleportAuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/code/sessions/session_123/teleport-events" {
			t.Fatalf("unexpected fallback request = %s %s", r.Method, r.URL.String())
		}
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	_, source, err := client.FetchSessionTranscript(context.Background(), "session_123")
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != core.APIErrorAuth {
		t.Fatalf("error = %#v", err)
	}
	if source != TranscriptSourceTeleportEvents {
		t.Fatalf("source = %q", source)
	}
}

func TestFetchTeleportEvents(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/code/sessions/session_123/teleport-events" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("cursor") == "" {
			if r.URL.Query().Get("limit") != "1000" {
				t.Fatalf("limit = %q", r.URL.Query().Get("limit"))
			}
			_, _ = w.Write([]byte(`{"data":[{"payload":{"uuid":"uuid_1"}},{"payload":null}],"next_cursor":"next_page"}`))
			return
		}
		if r.URL.Query().Get("cursor") != "next_page" {
			t.Fatalf("cursor = %q", r.URL.Query().Get("cursor"))
		}
		_, _ = w.Write([]byte(`{"data":[{"payload":{"uuid":"uuid_2"}}]}`))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got, err := client.FetchTeleportEvents(context.Background(), "session_123")
	if err != nil {
		t.Fatalf("FetchTeleportEvents() error = %v", err)
	}
	if len(got) != 2 || !strings.Contains(string(got[0]), "uuid_1") || !strings.Contains(string(got[1]), "uuid_2") {
		t.Fatalf("events = %#v", got)
	}
	if len(queries) != 2 {
		t.Fatalf("queries = %#v", queries)
	}
}

func TestFetchTeleportEventsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got, err := client.FetchTeleportEvents(context.Background(), "session_missing")
	if err != nil {
		t.Fatalf("FetchTeleportEvents() error = %v", err)
	}
	if got != nil {
		t.Fatalf("events = %#v", got)
	}
}

func TestFetchTeleportEventsNotFoundAfterFirstPageReturnsPartial(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":[{"payload":{"uuid":"uuid_1"}}],"next_cursor":"next_page"}`))
			return
		}
		http.NotFound(w, nil)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	got, err := client.FetchTeleportEvents(context.Background(), "session_123")
	if err != nil {
		t.Fatalf("FetchTeleportEvents() error = %v", err)
	}
	if len(got) != 1 || !strings.Contains(string(got[0]), "uuid_1") {
		t.Fatalf("events = %#v", got)
	}
}

func TestFetchTeleportEventsUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	defer server.Close()

	client := newTestClient(t, server)
	_, err := client.FetchTeleportEvents(context.Background(), "session_123")
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != core.APIErrorAuth {
		t.Fatalf("error = %#v", err)
	}
}

func TestFetchTeleportEventsPageCap(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"data":[{"payload":{"uuid":"uuid_1"}}],"next_cursor":"again"}`))
	}))
	defer server.Close()

	client := newTestClient(t, server)
	client.maxTeleportPages = 2
	got, err := client.FetchTeleportEvents(context.Background(), "session_123")
	if err != nil {
		t.Fatalf("FetchTeleportEvents() error = %v", err)
	}
	if len(got) != 2 || calls != 2 {
		t.Fatalf("events = %#v, calls = %d", got, calls)
	}
}

func TestClearSessions(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "https://example.test"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	client.lastUUIDBySession["one"] = "uuid_1"
	client.lastUUIDBySession["two"] = "uuid_2"
	client.appendLocks["one"] = &sync.Mutex{}
	client.appendLocks["two"] = &sync.Mutex{}
	client.ClearSession("one")
	if _, ok := client.lastUUIDBySession["one"]; ok {
		t.Fatalf("session one was not cleared")
	}
	if _, ok := client.appendLocks["one"]; ok {
		t.Fatalf("session one append lock was not cleared")
	}
	client.ClearAllSessions()
	if len(client.lastUUIDBySession) != 0 {
		t.Fatalf("last uuid map = %#v", client.lastUUIDBySession)
	}
	if len(client.appendLocks) != 0 {
		t.Fatalf("append locks = %#v", client.appendLocks)
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
