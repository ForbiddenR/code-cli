package sessionsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"code-cli/internal/core"
)

func TestListCodeSessions(t *testing.T) {
	var sawHeaders bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access_token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != AnthropicVersion {
			t.Fatalf("anthropic-version = %q", got)
		}
		if got := r.Header.Get("anthropic-beta"); got != CCRBYOCBeta {
			t.Fatalf("anthropic-beta = %q", got)
		}
		if got := r.Header.Get("x-organization-uuid"); got != "org_uuid" {
			t.Fatalf("x-organization-uuid = %q", got)
		}
		sawHeaders = true
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"data":[{
				"type":"session",
				"id":"sess_1",
				"title":"Example session",
				"session_status":"idle",
				"environment_id":"env_1",
				"created_at":"2026-01-01T00:00:00Z",
				"updated_at":"2026-01-02T00:00:00Z",
				"session_context":{
					"sources":[{"type":"git_repository","url":"https://github.com/anthropics/claude-code.git","revision":"main"}],
					"cwd":"/workspace",
					"outcomes":null,
					"custom_system_prompt":null,
					"append_system_prompt":null,
					"model":null
				}
			},{
				"type":"session",
				"id":"sess_2",
				"title":null,
				"session_status":"running",
				"environment_id":"env_2",
				"created_at":"2026-01-03T00:00:00Z",
				"updated_at":"2026-01-04T00:00:00Z",
				"session_context":{
					"sources":[{"type":"knowledge_base","knowledge_base_id":"kb_1"}],
					"cwd":"/workspace",
					"outcomes":[],
					"custom_system_prompt":null,
					"append_system_prompt":null,
					"model":null
				}
			}],
			"has_more":false,
			"first_id":"sess_1",
			"last_id":"sess_2"
		}`)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, AccessToken: "access_token", OrgUUID: "org_uuid"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	sessions, err := client.ListCodeSessions(context.Background())
	if err != nil {
		t.Fatalf("ListCodeSessions: %v", err)
	}
	if !sawHeaders {
		t.Fatalf("server did not see headers")
	}
	if len(sessions) != 2 {
		t.Fatalf("len(sessions) = %d", len(sessions))
	}
	first := sessions[0]
	if first.ID != "sess_1" || first.Title != "Example session" || first.Status != SessionStatusIdle || first.Description != "" {
		t.Fatalf("first session = %#v", first)
	}
	if first.Repo == nil || first.Repo.Owner.Login != "anthropics" || first.Repo.Name != "claude-code" || first.Repo.DefaultBranch != "main" {
		t.Fatalf("first repo = %#v", first.Repo)
	}
	if sessions[1].Title != "Untitled" || sessions[1].Repo != nil {
		t.Fatalf("second session = %#v", sessions[1])
	}
}

func TestCodeSessionFromResource(t *testing.T) {
	title := ""
	revision := "feature-branch"
	session := SessionResource{
		ID:            "sess_1",
		Title:         &title,
		SessionStatus: SessionStatusRunning,
		CreatedAt:     "2026-01-01T00:00:00Z",
		UpdatedAt:     "2026-01-02T00:00:00Z",
		SessionContext: SessionContext{Sources: []SessionContextSource{{
			Type:     "git_repository",
			URL:      "git@github.com:owner/repo.git",
			Revision: &revision,
		}}},
	}

	codeSession := CodeSessionFromResource(session)
	if codeSession.ID != "sess_1" || codeSession.Title != "Untitled" || codeSession.Status != SessionStatusRunning {
		t.Fatalf("code session = %#v", codeSession)
	}
	if len(codeSession.Turns) != 0 {
		t.Fatalf("turns = %#v", codeSession.Turns)
	}
	if codeSession.Repo == nil || codeSession.Repo.Owner.Login != "owner" || codeSession.Repo.Name != "repo" || codeSession.Repo.DefaultBranch != "feature-branch" {
		t.Fatalf("repo = %#v", codeSession.Repo)
	}
}

func TestGetBranchFromSession(t *testing.T) {
	session := SessionResource{SessionContext: SessionContext{Outcomes: []Outcome{
		{Type: "other", GitInfo: OutcomeGitInfo{Branches: []string{"ignored"}}},
		{Type: "git_repository", GitInfo: OutcomeGitInfo{Branches: []string{"main", "dev"}}},
	}}}

	branch, ok := GetBranchFromSession(session)
	if !ok || branch != "main" {
		t.Fatalf("GetBranchFromSession = (%q, %v)", branch, ok)
	}
}

func TestGetBranchFromSessionMissing(t *testing.T) {
	tests := []struct {
		name    string
		session SessionResource
	}{
		{name: "nil outcomes"},
		{name: "no git outcome", session: SessionResource{SessionContext: SessionContext{Outcomes: []Outcome{{Type: "other"}}}}},
		{name: "empty branches", session: SessionResource{SessionContext: SessionContext{Outcomes: []Outcome{{Type: "git_repository"}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			branch, ok := GetBranchFromSession(test.session)
			if ok || branch != "" {
				t.Fatalf("GetBranchFromSession = (%q, %v)", branch, ok)
			}
		})
	}
}

func TestListCodeSessionsRetriesServerErrors(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"data":[],"has_more":false,"first_id":null,"last_id":null}`)
	}))
	defer server.Close()

	var sleeps []time.Duration
	client, err := NewClient(Config{
		BaseURL:     server.URL,
		RetryDelays: []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond},
		Sleep: func(delay time.Duration) {
			sleeps = append(sleeps, delay)
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.ListCodeSessions(context.Background())
	if err != nil {
		t.Fatalf("ListCodeSessions: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d", attempts)
	}
	wantSleeps := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}
	if !reflect.DeepEqual(sleeps, wantSleeps) {
		t.Fatalf("sleeps = %v, want %v", sleeps, wantSleeps)
	}
}

func TestListCodeSessionsDoesNotRetryClientErrors(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, `{"error":{"message":"bad org"}}`, http.StatusForbidden)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, RetryDelays: []time.Duration{time.Millisecond}})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.ListCodeSessions(context.Background())
	if err == nil {
		t.Fatalf("ListCodeSessions returned nil error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d", attempts)
	}
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != core.APIErrorPermission || apiErr.Retryable {
		t.Fatalf("err = %#v", err)
	}
}

func TestFetchSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/sess_1" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		fmt.Fprint(w, `{
			"type":"session",
			"id":"sess_1",
			"title":"Example",
			"session_status":"idle",
			"environment_id":"env_1",
			"created_at":"2026-01-01T00:00:00Z",
			"updated_at":"2026-01-02T00:00:00Z",
			"session_context":{
				"sources":[],
				"cwd":"/workspace",
				"outcomes":[],
				"custom_system_prompt":null,
				"append_system_prompt":null,
				"model":null
			}
		}`)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	session, err := client.FetchSession(context.Background(), "sess_1")
	if err != nil {
		t.Fatalf("FetchSession: %v", err)
	}
	if session.ID != "sess_1" || session.Title == nil || *session.Title != "Example" {
		t.Fatalf("session = %#v", session)
	}
}

func TestFetchSessionNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.FetchSession(context.Background(), "missing")
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound || !strings.Contains(apiErr.Message, "missing") {
		t.Fatalf("err = %#v", err)
	}
}

func TestFetchSessionUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "expired", http.StatusUnauthorized)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.FetchSession(context.Background(), "sess_1")
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != core.APIErrorAuth || !strings.Contains(apiErr.Message, "/login") {
		t.Fatalf("err = %#v", err)
	}
}

func TestFetchSessionUsesAPIErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-request-id", "req_123")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"bad session"}}`)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.FetchSession(context.Background(), "sess_1")
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "bad session" || apiErr.RequestID != "req_123" {
		t.Fatalf("err = %#v", err)
	}
}

func TestNewClientValidation(t *testing.T) {
	if _, err := NewClient(Config{BaseURL: ":// bad"}); err == nil {
		t.Fatalf("NewClient accepted invalid base url")
	}
	if _, err := NewClient(Config{BaseURL: "api.anthropic.com"}); err == nil {
		t.Fatalf("NewClient accepted base url without scheme")
	}
}

func TestParseGitHubRepository(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantOK    bool
	}{
		{name: "https", url: "https://github.com/owner/repo.git", wantOwner: "owner", wantRepo: "repo", wantOK: true},
		{name: "ssh", url: "git@github.com:owner/repo.git", wantOwner: "owner", wantRepo: "repo", wantOK: true},
		{name: "not github", url: "https://example.com/owner/repo", wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owner, repo, ok := parseGitHubRepository(test.url)
			if owner != test.wantOwner || repo != test.wantRepo || ok != test.wantOK {
				t.Fatalf("parseGitHubRepository(%q) = (%q, %q, %v)", test.url, owner, repo, ok)
			}
		})
	}
}

func TestSendEventToRemoteSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		if r.URL.Path != "/v1/sessions/sess_1/events" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access_token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != AnthropicVersion {
			t.Fatalf("anthropic-version = %q", got)
		}
		if got := r.Header.Get("anthropic-beta"); got != CCRBYOCBeta {
			t.Fatalf("anthropic-beta = %q", got)
		}
		if got := r.Header.Get("x-organization-uuid"); got != "org_uuid" {
			t.Fatalf("x-organization-uuid = %q", got)
		}

		var request sendEventsRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(request.Events) != 1 {
			t.Fatalf("len(events) = %d", len(request.Events))
		}
		event := request.Events[0]
		if event.UUID != "event_uuid" || event.SessionID != "sess_1" || event.Type != "user" || event.ParentToolUseID != nil {
			t.Fatalf("event = %#v", event)
		}
		if event.Message.Role != "user" || event.Message.Content != "hello" {
			t.Fatalf("message = %#v", event.Message)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, AccessToken: "access_token", OrgUUID: "org_uuid"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ok, err := client.SendEventToRemoteSession(context.Background(), "sess_1", "hello", SendEventOptions{UUID: "event_uuid"})
	if err != nil {
		t.Fatalf("SendEventToRemoteSession: %v", err)
	}
	if !ok {
		t.Fatalf("SendEventToRemoteSession returned false")
	}
}

func TestSendEventToRemoteSessionGeneratesUUID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request sendEventsRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(request.Events) != 1 {
			t.Fatalf("len(events) = %d", len(request.Events))
		}
		if got := request.Events[0].UUID; len(got) != 36 || got[14] != '4' {
			t.Fatalf("generated uuid = %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ok, err := client.SendEventToRemoteSession(context.Background(), "sess_1", []map[string]any{{"type": "text", "text": "hello"}}, SendEventOptions{})
	if err != nil {
		t.Fatalf("SendEventToRemoteSession: %v", err)
	}
	if !ok {
		t.Fatalf("SendEventToRemoteSession returned false")
	}
}

func TestSendEventToRemoteSessionReturnsFalseOnAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"bad event"}}`)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ok, err := client.SendEventToRemoteSession(context.Background(), "sess_1", "hello", SendEventOptions{UUID: "event_uuid"})
	if ok {
		t.Fatalf("SendEventToRemoteSession returned true")
	}
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "bad event" || apiErr.Kind != core.APIErrorInvalidRequest {
		t.Fatalf("err = %#v", err)
	}
}

func TestSendEventToRemoteSessionValidatesInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server should not be called")
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if ok, err := client.SendEventToRemoteSession(context.Background(), " ", "hello", SendEventOptions{}); ok || err == nil {
		t.Fatalf("empty session result = (%v, %v)", ok, err)
	}
	if ok, err := client.SendEventToRemoteSession(context.Background(), "sess_1", nil, SendEventOptions{}); ok || err == nil {
		t.Fatalf("nil content result = (%v, %v)", ok, err)
	}
}

func TestUpdateSessionTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("method = %q", r.Method)
		}
		if r.URL.Path != "/v1/sessions/sess_1" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access_token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != AnthropicVersion {
			t.Fatalf("anthropic-version = %q", got)
		}
		if got := r.Header.Get("anthropic-beta"); got != CCRBYOCBeta {
			t.Fatalf("anthropic-beta = %q", got)
		}
		if got := r.Header.Get("x-organization-uuid"); got != "org_uuid" {
			t.Fatalf("x-organization-uuid = %q", got)
		}

		var request updateSessionTitleRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.Title != "New title" {
			t.Fatalf("title = %q", request.Title)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, AccessToken: "access_token", OrgUUID: "org_uuid"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ok, err := client.UpdateSessionTitle(context.Background(), "sess_1", "New title")
	if err != nil {
		t.Fatalf("UpdateSessionTitle: %v", err)
	}
	if !ok {
		t.Fatalf("UpdateSessionTitle returned false")
	}
}

func TestUpdateSessionTitleReturnsFalseOnAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"message":"cannot update title"}}`)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ok, err := client.UpdateSessionTitle(context.Background(), "sess_1", "New title")
	if ok {
		t.Fatalf("UpdateSessionTitle returned true")
	}
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "cannot update title" || apiErr.Kind != core.APIErrorPermission {
		t.Fatalf("err = %#v", err)
	}
}

func TestUpdateSessionTitleValidatesSessionID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("server should not be called")
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if ok, err := client.UpdateSessionTitle(context.Background(), " ", "New title"); ok || err == nil {
		t.Fatalf("empty session result = (%v, %v)", ok, err)
	}
}

func TestIsSDKSessionEvent(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "assistant with session", raw: `{"type":"assistant","session_id":"s1"}`, want: true},
		{name: "missing session_id", raw: `{"type":"assistant"}`, want: false},
		{name: "env manager log", raw: `{"type":"env_manager_log","session_id":"s1"}`, want: false},
		{name: "control response", raw: `{"type":"control_response","session_id":"s1"}`, want: false},
		{name: "not object", raw: `"x"`, want: false},
		{name: "missing type", raw: `{"session_id":"s1"}`, want: false},
	}
	for _, test := range cases {
		if got := IsSDKSessionEvent(json.RawMessage(test.raw)); got != test.want {
			t.Fatalf("%s: got %v want %v", test.name, got, test.want)
		}
	}
}

func TestPollRemoteSessionEventsPagesAndFilters(t *testing.T) {
	var pages []string
	var sawMetadata bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1/events":
			pages = append(pages, r.URL.RawQuery)
			if r.URL.Query().Get("after_id") == "" {
				fmt.Fprint(w, `{"data":[{"type":"assistant","session_id":"sess_1","uuid":"a1"},{"type":"env_manager_log","session_id":"sess_1"},{"type":"control_response","session_id":"sess_1"},{"type":"user"}],"has_more":true,"first_id":"e1","last_id":"e2"}`)
				return
			}
			if r.URL.Query().Get("after_id") != "e2" {
				t.Fatalf("after_id = %q", r.URL.Query().Get("after_id"))
			}
			fmt.Fprint(w, `{"data":[{"type":"result","session_id":"sess_1","subtype":"success"}],"has_more":false,"first_id":"e3","last_id":"e3"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sessions/sess_1":
			sawMetadata = true
			fmt.Fprint(w, `{"type":"session","id":"sess_1","title":"Title","session_status":"running","environment_id":"env_1","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","session_context":{"sources":[],"cwd":"/workspace","outcomes":[{"type":"git_repository","git_info":{"type":"git_repository","repo":"anthropics/claude-code","branches":["claude/task"]}}],"custom_system_prompt":null,"append_system_prompt":null,"model":null}}`)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, AccessToken: "token", OrgUUID: "org"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := client.PollRemoteSessionEvents(context.Background(), "sess_1", PollEventsOptions{})
	if err != nil {
		t.Fatalf("PollRemoteSessionEvents: %v", err)
	}
	if len(pages) != 2 || pages[0] != "" || pages[1] != "after_id=e2" {
		t.Fatalf("pages = %#v", pages)
	}
	if !sawMetadata || len(result.NewEvents) != 2 {
		t.Fatalf("result = %#v sawMetadata=%v", result, sawMetadata)
	}
	if result.LastEventID == nil || *result.LastEventID != "e3" {
		t.Fatalf("last = %#v", result.LastEventID)
	}
	if result.Branch == nil || *result.Branch != "claude/task" {
		t.Fatalf("branch = %#v", result.Branch)
	}
	if result.SessionStatus == nil || *result.SessionStatus != SessionStatusRunning {
		t.Fatalf("status = %#v", result.SessionStatus)
	}
}

func TestPollRemoteSessionEventsSkipMetadata(t *testing.T) {
	var sawSession bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/sessions/sess_1" {
			sawSession = true
		}
		if r.URL.Path == "/v1/sessions/sess_1/events" {
			fmt.Fprint(w, `{"data":[{"type":"assistant","session_id":"sess_1"}],"has_more":false,"first_id":"e1","last_id":"e1"}`)
			return
		}
		t.Fatalf("unexpected %s", r.URL.Path)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := client.PollRemoteSessionEvents(context.Background(), "sess_1", PollEventsOptions{SkipMetadata: true, AfterID: "prev"})
	if err != nil {
		t.Fatalf("PollRemoteSessionEvents: %v", err)
	}
	if sawSession || result.Branch != nil || result.SessionStatus != nil || result.LastEventID == nil || *result.LastEventID != "e1" {
		t.Fatalf("result = %#v sawSession=%v", result, sawSession)
	}
}

func TestPollRemoteSessionEventsInvalidResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":null,"has_more":false}`)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.PollRemoteSessionEvents(context.Background(), "sess_1", PollEventsOptions{SkipMetadata: true})
	if err == nil || err.Error() != "invalid events response" {
		t.Fatalf("err = %v", err)
	}
}

func TestPollRemoteSessionEventsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"nope"}}`)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.PollRemoteSessionEvents(context.Background(), "sess_1", PollEventsOptions{SkipMetadata: true})
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != core.APIErrorAuth {
		t.Fatalf("err = %#v", err)
	}
}

func TestPollRemoteSessionEventsValidatesSessionID(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "https://example.test"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.PollRemoteSessionEvents(context.Background(), " ", PollEventsOptions{}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestArchiveRemoteSessionSuccessAndConflict(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusConflict} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/sessions/sess_1/archive" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("anthropic-beta"); got != CCRBYOCBeta {
				t.Fatalf("anthropic-beta = %q", got)
			}
			w.WriteHeader(status)
		}))
		client, err := NewClient(Config{BaseURL: server.URL, AccessToken: "token", OrgUUID: "org"})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		ok, err := client.ArchiveRemoteSession(context.Background(), "sess_1")
		server.Close()
		if err != nil || !ok {
			t.Fatalf("status %d: ok=%v err=%v", status, ok, err)
		}
	}
}

func TestArchiveRemoteSessionAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"message":"cannot archive"}}`)
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ok, err := client.ArchiveRemoteSession(context.Background(), "sess_1")
	if ok {
		t.Fatal("expected false")
	}
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "cannot archive" {
		t.Fatalf("err = %#v", err)
	}
}

func TestArchiveRemoteSessionValidatesSessionID(t *testing.T) {
	client, err := NewClient(Config{BaseURL: "https://example.test"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if ok, err := client.ArchiveRemoteSession(context.Background(), " "); ok || err == nil {
		t.Fatalf("result = (%v, %v)", ok, err)
	}
}
