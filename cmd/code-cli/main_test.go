package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
	"code-cli/internal/query"
	"code-cli/internal/session"
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

func TestRunComposesModelBackedTUIWithoutLeakingCredentials(t *testing.T) {
	requestErr := errors.New("offline request stopped")
	client := &recordingClient{err: requestErr}
	var clientConfig core.APIConfig
	var tuiConfig tui.Config
	var tuiSession *session.Session

	getenv := func(name string) string {
		return map[string]string{
			"ANTHROPIC_MODEL":    "  claude-test-model  ",
			"ANTHROPIC_API_KEY":  "secret-must-not-leak",
			"ANTHROPIC_BASE_URL": "  https://proxy.example.test/anthropic  ",
			"SHELL":              "  /bin/zsh  ",
		}[name]
	}
	deps := dependencies{
		getenv: getenv,
		getwd:  func() (string, error) { return "/workspace/project", nil },
		newClient: func(config core.APIConfig) (query.ModelClient, error) {
			clientConfig = config
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
	if clientConfig.APIKey != "" {
		t.Fatalf("client API key = %q, want SDK environment resolution", clientConfig.APIKey)
	}
	if got, want := clientConfig.BaseURL, "https://proxy.example.test/anthropic"; got != want {
		t.Fatalf("client base URL = %q, want %q", got, want)
	}
	if tuiSession == nil || tuiConfig.Responder == nil {
		t.Fatalf("TUI composition = session %v, responder %v", tuiSession != nil, tuiConfig.Responder != nil)
	}
	if got, want := tuiConfig.Model, "claude-test-model"; got != want {
		t.Fatalf("TUI model = %q, want %q", got, want)
	}
	if got, want := tuiConfig.WorkingDirectory, "/workspace/project"; got != want {
		t.Fatalf("TUI working directory = %q, want %q", got, want)
	}
	if client.calls != 1 {
		t.Fatalf("model client calls = %d, want one injected offline call", client.calls)
	}
	if got, want := client.request.Model, core.ModelID("claude-test-model"); got != want {
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
	for _, want := range []string{"/workspace/project", "test-platform", "/bin/zsh", "TestOS 1.2.3", "claude-test-model"} {
		if !strings.Contains(prompt.String(), want) {
			t.Fatalf("system prompt does not contain %q: %q", want, prompt.String())
		}
	}
	if strings.Contains(prompt.String(), "secret-must-not-leak") || strings.Contains(tuiConfig.Model, "secret-must-not-leak") {
		t.Fatal("credential leaked into model-visible or TUI configuration")
	}
}

func TestRunUsesDefaultModelAndUnknownShell(t *testing.T) {
	client := &recordingClient{err: errors.New("offline")}
	var config tui.Config
	deps := dependencies{
		getenv: func(string) string { return "" },
		getwd:  func() (string, error) { return "/tmp/project", nil },
		newClient: func(core.APIConfig) (query.ModelClient, error) {
			return client, nil
		},
		runTUI: func(_ *session.Session, value tui.Config) error {
			config = value
			for range value.Responder.SubmitEvents(context.Background(), core.UserMessage("hello"), 1) {
			}
			return nil
		},
		platform:  "linux",
		osVersion: "Linux test",
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

func TestRunPropagatesCompositionErrors(t *testing.T) {
	getwdErr := errors.New("cwd unavailable")
	if err := run(dependencies{getwd: func() (string, error) { return "", getwdErr }}); !errors.Is(err, getwdErr) {
		t.Fatalf("getwd error = %v, want %v", err, getwdErr)
	}

	clientErr := errors.New("client unavailable")
	deps := dependencies{
		getenv: func(string) string { return "" },
		getwd:  func() (string, error) { return "/tmp/project", nil },
		newClient: func(core.APIConfig) (query.ModelClient, error) {
			return nil, clientErr
		},
		platform:  "linux",
		osVersion: "Linux test",
	}
	if err := run(deps); !errors.Is(err, clientErr) {
		t.Fatalf("client error = %v, want %v", err, clientErr)
	}

	tuiErr := errors.New("terminal unavailable")
	deps.newClient = func(core.APIConfig) (query.ModelClient, error) { return &recordingClient{}, nil }
	deps.runTUI = func(*session.Session, tui.Config) error { return tuiErr }
	if err := run(deps); !errors.Is(err, tuiErr) {
		t.Fatalf("TUI error = %v, want %v", err, tuiErr)
	}
}
