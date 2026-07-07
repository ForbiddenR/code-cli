package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchClaudeCodeFirstTokenDateValid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/organization/claude_code_first_token_date" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`{"first_token_date":"2026-07-07T00:00:00Z"}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.FetchClaudeCodeFirstTokenDate(context.Background())
	if err != nil {
		t.Fatalf("FetchClaudeCodeFirstTokenDate() error = %v", err)
	}
	if got.FirstTokenDate == nil || *got.FirstTokenDate != "2026-07-07T00:00:00Z" {
		t.Fatalf("first token date = %#v", got.FirstTokenDate)
	}
}

func TestFetchClaudeCodeFirstTokenDateNull(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"first_token_date":null}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.FetchClaudeCodeFirstTokenDate(context.Background())
	if err != nil {
		t.Fatalf("FetchClaudeCodeFirstTokenDate() error = %v", err)
	}
	if got.FirstTokenDate != nil {
		t.Fatalf("first token date = %#v", got.FirstTokenDate)
	}
}

func TestFetchClaudeCodeFirstTokenDateInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"first_token_date":"not-a-date"}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	if _, err := client.FetchClaudeCodeFirstTokenDate(context.Background()); err == nil {
		t.Fatalf("expected invalid date error")
	}
}
