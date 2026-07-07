package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchReferralEligibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/organizations/org-123/referral/eligibility" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.URL.Query().Get("campaign") != string(DefaultReferralCampaign) {
			t.Fatalf("campaign = %q", r.URL.Query().Get("campaign"))
		}
		if r.Header.Get("x-organization-uuid") != "org-123" {
			t.Fatalf("org header = %q", r.Header.Get("x-organization-uuid"))
		}
		_, _ = w.Write([]byte(`{
			"eligible":true,
			"remaining_passes":2,
			"referral_code_details":{"referral_link":"https://claude.ai/invite/code","campaign":"claude_code_guest_pass"},
			"referrer_reward":{"amount_minor_units":2500,"currency":"USD"}
		}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.FetchReferralEligibility(context.Background(), "org-123", "")
	if err != nil {
		t.Fatalf("FetchReferralEligibility() error = %v", err)
	}
	if !got.Eligible || got.RemainingPasses == nil || *got.RemainingPasses != 2 {
		t.Fatalf("eligibility = %#v", got)
	}
	if got.ReferralCodeDetails == nil || got.ReferralCodeDetails.ReferralLink != "https://claude.ai/invite/code" || got.ReferralCodeDetails.Campaign != DefaultReferralCampaign {
		t.Fatalf("referral code details = %#v", got.ReferralCodeDetails)
	}
	if got.ReferrerReward == nil || got.ReferrerReward.AmountMinorUnits != 2500 || got.ReferrerReward.Currency != "USD" {
		t.Fatalf("referrer reward = %#v", got.ReferrerReward)
	}
}

func TestFetchReferralEligibilityCustomCampaign(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("campaign") != "custom_campaign" {
			t.Fatalf("campaign = %q", r.URL.Query().Get("campaign"))
		}
		_, _ = w.Write([]byte(`{"eligible":false}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.FetchReferralEligibility(context.Background(), "org-123", "custom_campaign")
	if err != nil {
		t.Fatalf("FetchReferralEligibility() error = %v", err)
	}
	if got.Eligible {
		t.Fatalf("eligibility = %#v", got)
	}
}

func TestFetchReferralRedemptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/organizations/org-123/referral/redemptions" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if r.URL.Query().Get("campaign") != "custom_campaign" {
			t.Fatalf("campaign = %q", r.URL.Query().Get("campaign"))
		}
		if r.Header.Get("x-organization-uuid") != "org-123" {
			t.Fatalf("org header = %q", r.Header.Get("x-organization-uuid"))
		}
		_, _ = w.Write([]byte(`{"redemptions":[{"uuid":"redemption-1"},null],"limit":3}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.FetchReferralRedemptions(context.Background(), "org-123", "custom_campaign")
	if err != nil {
		t.Fatalf("FetchReferralRedemptions() error = %v", err)
	}
	if got.Limit != 3 || len(got.Redemptions) != 2 || string(got.Redemptions[0]) != `{"uuid":"redemption-1"}` || string(got.Redemptions[1]) != `null` {
		t.Fatalf("redemptions = %#v", got)
	}
}

func TestReferralMethodsRejectEmptyOrgUUID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("server should not be called")
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	_, err := client.FetchReferralEligibility(context.Background(), " ", "")
	if err == nil || !strings.Contains(err.Error(), "organization uuid is required") {
		t.Fatalf("FetchReferralEligibility() error = %v", err)
	}
	_, err = client.FetchReferralRedemptions(context.Background(), "\t", "")
	if err == nil || !strings.Contains(err.Error(), "organization uuid is required") {
		t.Fatalf("FetchReferralRedemptions() error = %v", err)
	}
}

func TestFormatCreditAmount(t *testing.T) {
	tests := []struct {
		name   string
		reward ReferrerRewardInfo
		want   string
	}{
		{name: "integer dollars", reward: ReferrerRewardInfo{AmountMinorUnits: 2500, Currency: "USD"}, want: "$25"},
		{name: "cents", reward: ReferrerRewardInfo{AmountMinorUnits: 1234, Currency: "USD"}, want: "$12.34"},
		{name: "known non-usd", reward: ReferrerRewardInfo{AmountMinorUnits: 5000, Currency: "EUR"}, want: "€50"},
		{name: "unknown currency", reward: ReferrerRewardInfo{AmountMinorUnits: 999, Currency: "JPY"}, want: "JPY 9.99"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := FormatCreditAmount(test.reward); got != test.want {
				t.Fatalf("FormatCreditAmount() = %q, want %q", got, test.want)
			}
		})
	}
}
