package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchGroveSettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/oauth/account/settings" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`{"grove_enabled":null,"grove_notice_viewed_at":"2026-07-01T00:00:00Z"}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.FetchGroveSettings(context.Background())
	if err != nil {
		t.Fatalf("FetchGroveSettings() error = %v", err)
	}
	if got.GroveEnabled != nil || got.GroveNoticeViewedAt == nil || *got.GroveNoticeViewedAt != "2026-07-01T00:00:00Z" {
		t.Fatalf("settings = %#v", got)
	}
}

func TestMarkGroveNoticeViewed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/oauth/account/grove_notice_viewed" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if len(body) != 0 {
			t.Fatalf("body = %#v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	if err := client.MarkGroveNoticeViewed(context.Background()); err != nil {
		t.Fatalf("MarkGroveNoticeViewed() error = %v", err)
	}
}

func TestUpdateGroveSettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/oauth/account/settings" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		var body struct {
			GroveEnabled bool `json:"grove_enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if !body.GroveEnabled {
			t.Fatalf("body = %#v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	if err := client.UpdateGroveSettings(context.Background(), true); err != nil {
		t.Fatalf("UpdateGroveSettings() error = %v", err)
	}
}

func TestFetchGroveNoticeConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/claude_code_grove" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`{"grove_enabled":true,"domain_excluded":true,"notice_is_grace_period":false,"notice_reminder_frequency":7}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.FetchGroveNoticeConfig(context.Background())
	if err != nil {
		t.Fatalf("FetchGroveNoticeConfig() error = %v", err)
	}
	if !got.GroveEnabled || !got.DomainExcluded || got.NoticeIsGracePeriod || got.NoticeReminderFrequency == nil || *got.NoticeReminderFrequency != 7 {
		t.Fatalf("config = %#v", got)
	}
}

func TestFetchGroveNoticeConfigAppliesDefaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"grove_enabled":true,"notice_reminder_frequency":null}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.FetchGroveNoticeConfig(context.Background())
	if err != nil {
		t.Fatalf("FetchGroveNoticeConfig() error = %v", err)
	}
	if !got.GroveEnabled || got.DomainExcluded || !got.NoticeIsGracePeriod || got.NoticeReminderFrequency != nil {
		t.Fatalf("config = %#v", got)
	}
}

func TestCalculateShouldShowGrove(t *testing.T) {
	now := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	viewedRecently := "2026-07-01T00:00:00Z"
	viewedLongAgo := "2026-06-01T00:00:00Z"
	chosen := true
	frequency := 14

	tests := []struct {
		name                string
		settings            *AccountSettings
		config              *GroveConfig
		showIfAlreadyViewed bool
		want                bool
	}{
		{name: "api failure settings", config: &GroveConfig{NoticeIsGracePeriod: true}, want: false},
		{name: "api failure config", settings: &AccountSettings{}, want: false},
		{name: "already chosen", settings: &AccountSettings{GroveEnabled: &chosen}, config: &GroveConfig{NoticeIsGracePeriod: true}, want: false},
		{name: "force show", settings: &AccountSettings{GroveNoticeViewedAt: &viewedRecently}, config: &GroveConfig{NoticeIsGracePeriod: true}, showIfAlreadyViewed: true, want: true},
		{name: "grace period ended", settings: &AccountSettings{GroveNoticeViewedAt: &viewedRecently}, config: &GroveConfig{NoticeIsGracePeriod: false}, want: true},
		{name: "never viewed during grace", settings: &AccountSettings{}, config: &GroveConfig{NoticeIsGracePeriod: true}, want: true},
		{name: "viewed recently", settings: &AccountSettings{GroveNoticeViewedAt: &viewedRecently}, config: &GroveConfig{NoticeIsGracePeriod: true, NoticeReminderFrequency: &frequency}, want: false},
		{name: "viewed long ago", settings: &AccountSettings{GroveNoticeViewedAt: &viewedLongAgo}, config: &GroveConfig{NoticeIsGracePeriod: true, NoticeReminderFrequency: &frequency}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := CalculateShouldShowGrove(test.settings, test.config, test.showIfAlreadyViewed, now)
			if got != test.want {
				t.Fatalf("CalculateShouldShowGrove() = %v, want %v", got, test.want)
			}
		})
	}
}
