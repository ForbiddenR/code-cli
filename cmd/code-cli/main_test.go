package main

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
	"code-cli/internal/query"
	"code-cli/internal/session"
	"code-cli/internal/settings"
	"code-cli/internal/tui"
)

type recordingClient struct {
	request anthropicapi.MessageRequest
	calls   int
	err     error
}

func (client *recordingClient) StreamMessage(_ context.Context, request anthropicapi.MessageRequest, _ ...anthropicapi.CallOption) (anthropicapi.Stream, error) {
	client.calls++
	client.request = request
	return nil, client.err
}

func TestRunAppliesUserSettingsBeforeModelBackedTUI(t *testing.T) {
	requestErr := errors.New("offline request stopped")
	client := &recordingClient{err: requestErr}
	environment := map[string]string{
		"ANTHROPIC_MODEL":    "inherited-model",
		"ANTHROPIC_BASE_URL": "https://inherited.example.test",
		"SHELL":              "/bin/sh",
		"HOME":               "/original/home",
	}
	settingsEnvironment := map[string]string{
		"ANTHROPIC_MODEL":                "  claude-settings-model  ",
		"ANTHROPIC_API_KEY":              "settings-api-secret",
		"ANTHROPIC_BASE_URL":             "  https://settings.example.test/anthropic  ",
		"ANTHROPIC_AUTH_TOKEN":           "settings-auth-secret",
		"ANTHROPIC_PROFILE":              "settings-profile-secret",
		"ANTHROPIC_USE_GCP_VERTEX":       "true",
		"ANTHROPIC_VERTEX_PROJECT_ID":    "settings-project-secret",
		"GOOGLE_APPLICATION_CREDENTIALS": "settings-wif-secret",
		"SHELL":                          "  /bin/zsh  ",
		"HOME":                           "/redirected/home",
	}
	var loadedPath string
	var applied bool
	var getenvBeforeApply bool
	var getwdBeforeApply bool
	var clientSawCredentials bool
	var clientConfig core.APIConfig
	var tuiConfig tui.Config
	var tuiSession *session.Session

	deps := dependencies{
		getenv: func(name string) string {
			if !applied {
				getenvBeforeApply = true
			}
			return environment[name]
		},
		getwd: func() (string, error) {
			if !applied {
				getwdBeforeApply = true
			}
			return "/workspace/project", nil
		},
		userHomeDir: func() (string, error) {
			return environment["HOME"], nil
		},
		loadUserSettings: func(path string) (settings.User, error) {
			loadedPath = path
			return settings.User{Env: settingsEnvironment}, nil
		},
		applyEnvironment: func(values map[string]string) error {
			maps.Copy(environment, values)
			applied = true
			return nil
		},
		newClient: func(config core.APIConfig) (query.ModelClient, error) {
			clientConfig = config
			clientSawCredentials = environment["ANTHROPIC_API_KEY"] == "settings-api-secret" &&
				environment["ANTHROPIC_AUTH_TOKEN"] == "settings-auth-secret" &&
				environment["ANTHROPIC_PROFILE"] == "settings-profile-secret" &&
				environment["GOOGLE_APPLICATION_CREDENTIALS"] == "settings-wif-secret"
			return client, nil
		},
		runTUI: func(state *session.Session, config tui.Config) error {
			tuiSession = state
			tuiConfig = config
			events := config.Responder.SubmitEvents(context.Background(), core.UserMessage("hello"), 4)
			for range events {
			}
			return nil
		},
		platform:  "test-platform",
		osVersion: "TestOS 1.2.3",
	}

	if err := run(deps); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if loadedPath != "/original/home/.claude/settings.json" {
		t.Fatalf("settings path = %q, want original-home path", loadedPath)
	}
	if getenvBeforeApply || getwdBeforeApply {
		t.Fatalf("startup read environment before settings apply: getenv=%v getwd=%v", getenvBeforeApply, getwdBeforeApply)
	}
	if !clientSawCredentials {
		t.Fatal("client construction did not observe settings credentials")
	}
	if clientConfig.APIKey != "" {
		t.Fatalf("client API key = %q, want SDK environment resolution", clientConfig.APIKey)
	}
	if got, want := clientConfig.BaseURL, "https://settings.example.test/anthropic"; got != want {
		t.Fatalf("client base URL = %q, want %q", got, want)
	}
	if tuiSession == nil || tuiConfig.Responder == nil {
		t.Fatalf("TUI composition = session %v, responder %v", tuiSession != nil, tuiConfig.Responder != nil)
	}
	if got, want := tuiConfig.Model, "claude-settings-model"; got != want {
		t.Fatalf("TUI model = %q, want %q", got, want)
	}
	if got, want := tuiConfig.WorkingDirectory, "/workspace/project"; got != want {
		t.Fatalf("TUI working directory = %q, want %q", got, want)
	}
	if client.calls != 1 {
		t.Fatalf("model client calls = %d, want one injected offline call", client.calls)
	}
	if got, want := client.request.Model, core.ModelID("claude-settings-model"); got != want {
		t.Fatalf("request model = %q, want %q", got, want)
	}
	if len(client.request.Tools) != 0 || len(client.request.ServerTools) != 0 {
		t.Fatalf("request tools = %#v, server tools = %#v, want none", client.request.Tools, client.request.ServerTools)
	}
	if len(client.request.System) != 2 {
		t.Fatalf("system blocks = %d, want 2", len(client.request.System))
	}
	var prompt strings.Builder
	for _, block := range client.request.System {
		prompt.WriteString(block.Text)
	}
	for _, want := range []string{"/workspace/project", "test-platform", "/bin/zsh", "TestOS 1.2.3", "claude-settings-model"} {
		if !strings.Contains(prompt.String(), want) {
			t.Fatalf("system prompt does not contain %q: %q", want, prompt.String())
		}
	}
	for _, secret := range []string{
		"settings-api-secret",
		"settings-auth-secret",
		"settings-profile-secret",
		"settings-project-secret",
		"settings-wif-secret",
	} {
		if strings.Contains(prompt.String(), secret) ||
			strings.Contains(tuiConfig.Model, secret) ||
			strings.Contains(tuiConfig.WorkingDirectory, secret) {
			t.Fatalf("credential %q leaked into model-visible or TUI configuration", secret)
		}
	}
}

func TestRunUsesDefaultsWhenUserSettingsAreMissing(t *testing.T) {
	client := &recordingClient{err: errors.New("offline")}
	var config tui.Config
	deps := successfulDependencies()
	deps.getenv = func(string) string { return "" }
	deps.newClient = func(core.APIConfig) (query.ModelClient, error) {
		return client, nil
	}
	deps.runTUI = func(_ *session.Session, value tui.Config) error {
		config = value
		for range value.Responder.SubmitEvents(context.Background(), core.UserMessage("hello"), 1) {
		}
		return nil
	}

	if err := run(deps); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got, want := config.Model, core.DefaultModel.String(); got != want {
		t.Fatalf("TUI model = %q, want %q", got, want)
	}
	if got, want := client.request.Model, core.DefaultModel; got != want {
		t.Fatalf("request model = %q, want %q", got, want)
	}
	var prompt strings.Builder
	for _, block := range client.request.System {
		prompt.WriteString(block.Text)
	}
	if !strings.Contains(prompt.String(), `Shell: "unknown"`) {
		t.Fatalf("system prompt missing unknown shell fallback: %q", prompt.String())
	}
}

func TestRunStopsStartupOnCompositionErrors(t *testing.T) {
	homeErr := errors.New("home unavailable")
	loadErr := errors.New("settings unavailable")
	applyErr := errors.New("environment unavailable")
	getwdErr := errors.New("cwd unavailable")
	clientErr := errors.New("client unavailable")
	tuiErr := errors.New("terminal unavailable")

	tests := []struct {
		name   string
		want   error
		mutate func(*dependencies)
	}{
		{
			name: "home",
			want: homeErr,
			mutate: func(deps *dependencies) {
				deps.userHomeDir = func() (string, error) { return "", homeErr }
			},
		},
		{
			name: "load settings",
			want: loadErr,
			mutate: func(deps *dependencies) {
				deps.loadUserSettings = func(string) (settings.User, error) { return settings.User{}, loadErr }
			},
		},
		{
			name: "apply environment",
			want: applyErr,
			mutate: func(deps *dependencies) {
				deps.applyEnvironment = func(map[string]string) error { return applyErr }
			},
		},
		{
			name: "working directory",
			want: getwdErr,
			mutate: func(deps *dependencies) {
				deps.getwd = func() (string, error) { return "", getwdErr }
			},
		},
		{
			name: "client",
			want: clientErr,
			mutate: func(deps *dependencies) {
				deps.newClient = func(core.APIConfig) (query.ModelClient, error) { return nil, clientErr }
			},
		},
		{
			name: "TUI",
			want: tuiErr,
			mutate: func(deps *dependencies) {
				deps.runTUI = func(*session.Session, tui.Config) error { return tuiErr }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := successfulDependencies()
			clientCalled := false
			tuiCalled := false
			baseClient := deps.newClient
			baseTUI := deps.runTUI
			deps.newClient = func(config core.APIConfig) (query.ModelClient, error) {
				clientCalled = true
				return baseClient(config)
			}
			deps.runTUI = func(state *session.Session, config tui.Config) error {
				tuiCalled = true
				return baseTUI(state, config)
			}
			test.mutate(&deps)

			err := run(deps)
			if !errors.Is(err, test.want) {
				t.Fatalf("run() error = %v, want %v", err, test.want)
			}
			if test.name != "client" && test.name != "TUI" && clientCalled {
				t.Fatal("client constructed after earlier startup failure")
			}
			if test.name != "TUI" && tuiCalled {
				t.Fatal("TUI started after earlier startup failure")
			}
		})
	}
}

func successfulDependencies() dependencies {
	return dependencies{
		getenv:      func(string) string { return "" },
		getwd:       func() (string, error) { return "/tmp/project", nil },
		userHomeDir: func() (string, error) { return "/home/test", nil },
		loadUserSettings: func(string) (settings.User, error) {
			return settings.User{}, nil
		},
		applyEnvironment: func(map[string]string) error { return nil },
		newClient: func(core.APIConfig) (query.ModelClient, error) {
			return &recordingClient{}, nil
		},
		runTUI:    func(*session.Session, tui.Config) error { return nil },
		platform:  "linux",
		osVersion: "Linux test",
	}
}
