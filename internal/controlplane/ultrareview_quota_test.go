package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchUltrareviewQuota(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/ultrareview/quota" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("x-organization-uuid") != "org-123" {
			t.Fatalf("org header = %q", r.Header.Get("x-organization-uuid"))
		}
		_, _ = w.Write([]byte(`{"reviews_used":2,"reviews_limit":5,"reviews_remaining":3,"is_overage":false}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.FetchUltrareviewQuota(context.Background(), "org-123")
	if err != nil {
		t.Fatalf("FetchUltrareviewQuota() error = %v", err)
	}
	if got.ReviewsUsed != 2 || got.ReviewsLimit != 5 || got.ReviewsRemaining != 3 || got.IsOverage {
		t.Fatalf("quota = %#v", got)
	}
}

func TestFetchUltrareviewQuotaRejectsEmptyOrgUUID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("server should not be called")
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	_, err := client.FetchUltrareviewQuota(context.Background(), " ")
	if err == nil || !strings.Contains(err.Error(), "organization uuid is required") {
		t.Fatalf("FetchUltrareviewQuota() error = %v", err)
	}
}
