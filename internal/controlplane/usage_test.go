package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchUtilization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/usage" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`{
			"five_hour":{"utilization":42.5,"resets_at":"2026-07-07T10:00:00Z"},
			"seven_day":null,
			"extra_usage":{"is_enabled":true,"monthly_limit":100,"used_credits":12.25,"utilization":12.25}
		}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.FetchUtilization(context.Background())
	if err != nil {
		t.Fatalf("FetchUtilization() error = %v", err)
	}
	if got.FiveHour == nil || got.FiveHour.Utilization == nil || *got.FiveHour.Utilization != 42.5 {
		t.Fatalf("five_hour = %#v", got.FiveHour)
	}
	if got.SevenDay != nil {
		t.Fatalf("seven_day = %#v", got.SevenDay)
	}
	if got.ExtraUsage == nil || !got.ExtraUsage.IsEnabled || got.ExtraUsage.MonthlyLimit == nil || *got.ExtraUsage.MonthlyLimit != 100 {
		t.Fatalf("extra_usage = %#v", got.ExtraUsage)
	}
}
