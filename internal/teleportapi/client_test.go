package teleportapi

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

func TestListCodeSessionsPreparesAuthForSessionsAPI(t *testing.T) {
	var sawHeaders bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sessions" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("x-organization-uuid"); got != "org_env" {
			t.Fatalf("x-organization-uuid = %q", got)
		}
		sawHeaders = true
		fmt.Fprint(w, `{"data":[{"type":"session","id":"sess_1","title":"Title","session_status":"idle","environment_id":"env_1","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","session_context":{"sources":[],"cwd":"/workspace","outcomes":[],"custom_system_prompt":null,"append_system_prompt":null,"model":null}}],"has_more":false,"first_id":"sess_1","last_id":"sess_1"}`)
	}))
	defer server.Close()

	getter := &countingTokenGetter{tokens: &oauthstorage.Tokens{AccessToken: "access"}}
	client := NewClient(Config{
		BaseURL: server.URL,
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

	sessions, err := client.ListCodeSessions(context.Background())
	if err != nil {
		t.Fatalf("ListCodeSessions() error = %v", err)
	}
	if !sawHeaders || len(sessions) != 1 || sessions[0].ID != "sess_1" || sessions[0].Title != "Title" {
		t.Fatalf("sessions = %#v, sawHeaders = %v", sessions, sawHeaders)
	}
	if getter.calls != 1 {
		t.Fatalf("token getter calls = %d", getter.calls)
	}
}

func TestFetchSessionPreparesAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sessions/sess_1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Fatalf("Authorization = %q", got)
		}
		fmt.Fprint(w, `{"type":"session","id":"sess_1","title":"Title","session_status":"running","environment_id":"env_1","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","session_context":{"sources":[],"cwd":"/workspace","outcomes":[],"custom_system_prompt":null,"append_system_prompt":null,"model":null}}`)
	}))
	defer server.Close()

	client := newPreparedClient(server.URL, &countingTokenGetter{tokens: &oauthstorage.Tokens{AccessToken: "access"}})
	session, err := client.FetchSession(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("FetchSession() error = %v", err)
	}
	if session.ID != "sess_1" || session.Title == nil || *session.Title != "Title" {
		t.Fatalf("session = %#v", session)
	}
}

func TestSendEventToRemoteSessionPreparesAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sessions/sess_1/events" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("x-organization-uuid"); got != "org_env" {
			t.Fatalf("x-organization-uuid = %q", got)
		}
		var request struct {
			Events []struct {
				UUID    string `json:"uuid"`
				Message struct {
					Content any `json:"content"`
				} `json:"message"`
			} `json:"events"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(request.Events) != 1 || request.Events[0].UUID != "event_uuid" || request.Events[0].Message.Content != "hello" {
			t.Fatalf("request = %#v", request)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := newPreparedClient(server.URL, &countingTokenGetter{tokens: &oauthstorage.Tokens{AccessToken: "access"}})
	ok, err := client.SendEventToRemoteSession(context.Background(), "sess_1", "hello", sessionsapi.SendEventOptions{UUID: "event_uuid"})
	if err != nil {
		t.Fatalf("SendEventToRemoteSession() error = %v", err)
	}
	if !ok {
		t.Fatal("SendEventToRemoteSession() returned false")
	}
}

func TestUpdateSessionTitlePreparesAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/v1/sessions/sess_1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Title != "New title" {
			t.Fatalf("title = %q", body.Title)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newPreparedClient(server.URL, &countingTokenGetter{tokens: &oauthstorage.Tokens{AccessToken: "access"}})
	ok, err := client.UpdateSessionTitle(context.Background(), "sess_1", "New title")
	if err != nil {
		t.Fatalf("UpdateSessionTitle() error = %v", err)
	}
	if !ok {
		t.Fatal("UpdateSessionTitle() returned false")
	}
}

func TestClientPreparesAuthOnEachCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[],"has_more":false,"first_id":null,"last_id":null}`)
	}))
	defer server.Close()
	getter := &countingTokenGetter{tokens: &oauthstorage.Tokens{AccessToken: "access"}}
	client := newPreparedClient(server.URL, getter)

	if _, err := client.ListCodeSessions(context.Background()); err != nil {
		t.Fatalf("first ListCodeSessions() error = %v", err)
	}
	if _, err := client.ListCodeSessions(context.Background()); err != nil {
		t.Fatalf("second ListCodeSessions() error = %v", err)
	}
	if getter.calls != 2 {
		t.Fatalf("token getter calls = %d", getter.calls)
	}
}

func TestClientPropagatesPreparationErrorsBeforeHTTP(t *testing.T) {
	serverCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalled = true
	}))
	defer server.Close()

	client := newPreparedClient(server.URL, &countingTokenGetter{tokens: nil})
	_, err := client.ListCodeSessions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Claude Code web sessions require authentication") {
		t.Fatalf("err = %v", err)
	}
	if serverCalled {
		t.Fatal("server was called after preparation error")
	}
}

func TestClientReturnsMissingPreparerError(t *testing.T) {
	client := NewClient(Config{})
	_, err := client.ListCodeSessions(context.Background())
	if !errors.Is(err, teleportauth.ErrMissingPreparer) {
		t.Fatalf("err = %v", err)
	}
}

func TestClientPropagatesSessionsAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"message":"forbidden"}}`)
	}))
	defer server.Close()

	client := newPreparedClient(server.URL, &countingTokenGetter{tokens: &oauthstorage.Tokens{AccessToken: "access"}})
	_, err := client.ListCodeSessions(context.Background())
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != core.APIErrorPermission || apiErr.Message != "forbidden" {
		t.Fatalf("err = %#v", err)
	}
}

func TestClientPassesRetryConfiguration(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"data":[],"has_more":false,"first_id":null,"last_id":null}`)
	}))
	defer server.Close()
	var sleeps []time.Duration
	client := NewClient(Config{
		BaseURL:     server.URL,
		RetryDelays: []time.Duration{time.Millisecond},
		Sleep:       func(delay time.Duration) { sleeps = append(sleeps, delay) },
		Preparer: teleportauth.NewPreparer(teleportauth.Config{
			TokenGetter: &countingTokenGetter{tokens: &oauthstorage.Tokens{AccessToken: "access"}},
			Getenv: func(key string) string {
				if key == teleportauth.EnvOrganizationUUID {
					return "org_env"
				}
				return ""
			},
		}),
	})

	if _, err := client.ListCodeSessions(context.Background()); err != nil {
		t.Fatalf("ListCodeSessions() error = %v", err)
	}
	if attempts != 2 || len(sleeps) != 1 || sleeps[0] != time.Millisecond {
		t.Fatalf("attempts = %d, sleeps = %v", attempts, sleeps)
	}
}

func newPreparedClient(baseURL string, getter *countingTokenGetter) *Client {
	return NewClient(Config{
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
