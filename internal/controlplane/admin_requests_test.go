package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateAdminRequest(t *testing.T) {
	message := "please upgrade"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/oauth/organizations/org-123/admin_requests" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("x-organization-uuid") != "org-123" {
			t.Fatalf("org header = %q", r.Header.Get("x-organization-uuid"))
		}
		var body AdminRequestCreateParams
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.RequestType != AdminRequestSeatUpgrade || body.Details == nil || body.Details.Message == nil || *body.Details.Message != message {
			t.Fatalf("body = %#v", body)
		}
		_, _ = w.Write([]byte(`{"uuid":"req-1","status":"pending","created_at":"2026-07-07T00:00:00Z","request_type":"seat_upgrade","details":{"message":"please upgrade"}}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.CreateAdminRequest(context.Background(), "org-123", AdminRequestCreateParams{
		RequestType: AdminRequestSeatUpgrade,
		Details:     &AdminRequestSeatUpgradeDetails{Message: &message},
	})
	if err != nil {
		t.Fatalf("CreateAdminRequest() error = %v", err)
	}
	if got.UUID != "req-1" || got.RequestType != AdminRequestSeatUpgrade || got.Details == nil {
		t.Fatalf("request = %#v", got)
	}
}

func TestGetMyAdminRequestsQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/organizations/org-123/admin_requests/me" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.URL.Query().Get("request_type") != "limit_increase" {
			t.Fatalf("request_type = %q", r.URL.Query().Get("request_type"))
		}
		statuses := r.URL.Query()["statuses"]
		if len(statuses) != 2 || statuses[0] != "pending" || statuses[1] != "approved" {
			t.Fatalf("statuses = %#v", statuses)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.GetMyAdminRequests(context.Background(), "org-123", AdminRequestLimitIncrease, []AdminRequestStatus{AdminRequestPending, AdminRequestApproved})
	if err != nil {
		t.Fatalf("GetMyAdminRequests() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("requests = %#v", got)
	}
}

func TestGetMyAdminRequestsNullResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`null`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.GetMyAdminRequests(context.Background(), "org-123", AdminRequestLimitIncrease, nil)
	if err != nil {
		t.Fatalf("GetMyAdminRequests() error = %v", err)
	}
	if got != nil {
		t.Fatalf("requests = %#v", got)
	}
}

func TestAdminRequestMethodsRejectEmptyOrgUUID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("server should not be called")
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	_, err := client.CreateAdminRequest(context.Background(), "  ", AdminRequestCreateParams{RequestType: AdminRequestLimitIncrease})
	if err == nil || !strings.Contains(err.Error(), "organization uuid is required") {
		t.Fatalf("CreateAdminRequest() error = %v", err)
	}
	_, err = client.GetMyAdminRequests(context.Background(), "", AdminRequestLimitIncrease, nil)
	if err == nil || !strings.Contains(err.Error(), "organization uuid is required") {
		t.Fatalf("GetMyAdminRequests() error = %v", err)
	}
	_, err = client.CheckAdminRequestEligibility(context.Background(), "\t", AdminRequestSeatUpgrade)
	if err == nil || !strings.Contains(err.Error(), "organization uuid is required") {
		t.Fatalf("CheckAdminRequestEligibility() error = %v", err)
	}
}

func TestCheckAdminRequestEligibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/organizations/org-123/admin_requests/eligibility" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.URL.Query().Get("request_type") != "seat_upgrade" {
			t.Fatalf("request_type = %q", r.URL.Query().Get("request_type"))
		}
		_, _ = w.Write([]byte(`{"request_type":"seat_upgrade","is_allowed":true}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.CheckAdminRequestEligibility(context.Background(), "org-123", AdminRequestSeatUpgrade)
	if err != nil {
		t.Fatalf("CheckAdminRequestEligibility() error = %v", err)
	}
	if got.RequestType != AdminRequestSeatUpgrade || !got.IsAllowed {
		t.Fatalf("eligibility = %#v", got)
	}
}
