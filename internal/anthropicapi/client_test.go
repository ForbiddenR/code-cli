package anthropicapi

import (
	"testing"

	"code-cli/internal/core"
)

func TestNewSDKClientAppliesDefaults(t *testing.T) {
	client, err := NewSDKClient(core.APIConfig{})
	if err != nil {
		t.Fatalf("NewSDKClient() error = %v", err)
	}
	if client.config.BaseURL != core.DefaultBaseURL {
		t.Fatalf("base URL = %q", client.config.BaseURL)
	}
}

func TestNewSDKClientPreservesConfig(t *testing.T) {
	client, err := NewSDKClient(core.APIConfig{
		APIKey:    "key",
		BaseURL:   "https://example.invalid",
		UserAgent: "code-cli-test",
		DefaultHeaders: map[string]string{
			"x-test": "1",
		},
		Betas: []string{"beta-a", "beta-b"},
	})
	if err != nil {
		t.Fatalf("NewSDKClient() error = %v", err)
	}
	if client.config.BaseURL != "https://example.invalid" {
		t.Fatalf("base URL = %q", client.config.BaseURL)
	}
	if client.config.UserAgent != "code-cli-test" {
		t.Fatalf("user agent = %q", client.config.UserAgent)
	}
}
