package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchOverageCreditGrant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/organizations/org-123/overage_credit_grant" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("x-organization-uuid") != "" {
			t.Fatalf("unexpected org header = %q", r.Header.Get("x-organization-uuid"))
		}
		_, _ = w.Write([]byte(`{"available":true,"eligible":true,"granted":false,"amount_minor_units":2500,"currency":"USD"}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.FetchOverageCreditGrant(context.Background(), "org-123")
	if err != nil {
		t.Fatalf("FetchOverageCreditGrant() error = %v", err)
	}
	if !got.Available || !got.Eligible || got.Granted || got.AmountMinorUnits == nil || *got.AmountMinorUnits != 2500 || got.Currency == nil || *got.Currency != "USD" {
		t.Fatalf("grant = %#v", got)
	}
}

func TestFetchOverageCreditGrantRejectsEmptyOrgUUID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("server should not be called")
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	_, err := client.FetchOverageCreditGrant(context.Background(), "\t")
	if err == nil || !strings.Contains(err.Error(), "organization uuid is required") {
		t.Fatalf("FetchOverageCreditGrant() error = %v", err)
	}
}

func TestFormatGrantAmount(t *testing.T) {
	usd := "USD"
	minorUnits := int64(2500)
	got, ok := FormatGrantAmount(OverageCreditGrantInfo{AmountMinorUnits: &minorUnits, Currency: &usd})
	if !ok || got != "$25" {
		t.Fatalf("FormatGrantAmount() = %q, %v", got, ok)
	}

	minorUnits = 1234
	got, ok = FormatGrantAmount(OverageCreditGrantInfo{AmountMinorUnits: &minorUnits, Currency: &usd})
	if !ok || got != "$12.34" {
		t.Fatalf("FormatGrantAmount() = %q, %v", got, ok)
	}
}

func TestFormatGrantAmountUnsupported(t *testing.T) {
	eur := "EUR"
	minorUnits := int64(2500)
	if got, ok := FormatGrantAmount(OverageCreditGrantInfo{AmountMinorUnits: &minorUnits, Currency: &eur}); ok || got != "" {
		t.Fatalf("FormatGrantAmount() = %q, %v", got, ok)
	}
	if got, ok := FormatGrantAmount(OverageCreditGrantInfo{AmountMinorUnits: nil, Currency: &eur}); ok || got != "" {
		t.Fatalf("FormatGrantAmount() = %q, %v", got, ok)
	}
}
