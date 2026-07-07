package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchBootstrapTransformsModelOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/claude_cli/bootstrap" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`{
			"client_data":{"feature":true},
			"additional_model_options":[{"model":"claude-opus-4-8","name":"Opus","description":"Fast"}]
		}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.FetchBootstrap(context.Background())
	if err != nil {
		t.Fatalf("FetchBootstrap() error = %v", err)
	}
	var clientData map[string]bool
	if err := json.Unmarshal(got.ClientData, &clientData); err != nil {
		t.Fatalf("unmarshal client data: %v", err)
	}
	if !clientData["feature"] {
		t.Fatalf("client data = %#v", clientData)
	}
	if len(got.AdditionalModelOptions) != 1 || got.AdditionalModelOptions[0].Value != "claude-opus-4-8" || got.AdditionalModelOptions[0].Label != "Opus" {
		t.Fatalf("options = %#v", got.AdditionalModelOptions)
	}
}

func TestFetchBootstrapNullClientData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"client_data":null}`))
	}))
	defer server.Close()

	client := newTestClient(t, server, Config{})
	got, err := client.FetchBootstrap(context.Background())
	if err != nil {
		t.Fatalf("FetchBootstrap() error = %v", err)
	}
	if got.ClientData != nil {
		t.Fatalf("client data = %s", got.ClientData)
	}
}
