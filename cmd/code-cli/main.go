package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
	"code-cli/internal/query"
	"code-cli/internal/session"
	"code-cli/internal/systemprompt"
	"code-cli/internal/tui"
)

const version = "dev"

type dependencies struct {
	getenv    func(string) string
	getwd     func() (string, error)
	newClient func(core.APIConfig) (query.ModelClient, error)
	runTUI    func(*session.Session, tui.Config) error
	platform  string
	osVersion string
}

func main() {
	if err := run(defaultDependencies()); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "run code-cli: %v\n", err)
		os.Exit(1)
	}
}

func defaultDependencies() dependencies {
	return dependencies{
		getenv: os.Getenv,
		getwd:  os.Getwd,
		newClient: func(config core.APIConfig) (query.ModelClient, error) {
			return anthropicapi.NewSDKClient(config)
		},
		runTUI:    tui.RunWithConfig,
		platform:  runtime.GOOS,
		osVersion: runtime.GOOS + " " + runtime.GOARCH,
	}
}

func run(deps dependencies) error {
	workingDirectory, err := deps.getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	model := core.ModelID(strings.TrimSpace(deps.getenv("ANTHROPIC_MODEL")))
	if model == "" {
		model = core.DefaultModel
	}
	shell := strings.TrimSpace(deps.getenv("SHELL"))
	if shell == "" {
		shell = "unknown"
	}
	platform := strings.TrimSpace(deps.platform)
	if platform == "" {
		platform = runtime.GOOS
	}
	osVersion := strings.TrimSpace(deps.osVersion)
	if osVersion == "" {
		osVersion = runtime.GOOS + " " + runtime.GOARCH
	}

	system, err := systemprompt.Build(systemprompt.Options{
		Environment: systemprompt.Environment{
			WorkingDirectory: workingDirectory,
			Platform:         platform,
			Shell:            shell,
			OSVersion:        osVersion,
			Model:            model,
		},
		Tools:               nil,
		Skills:              nil,
		EnablePromptCaching: true,
	})
	if err != nil {
		return fmt.Errorf("build system prompt: %w", err)
	}

	client, err := deps.newClient(core.APIConfig{
		BaseURL: strings.TrimSpace(deps.getenv("ANTHROPIC_BASE_URL")),
	})
	if err != nil {
		return fmt.Errorf("construct Anthropic client: %w", err)
	}
	engine, err := query.NewEngine(query.Config{
		Client:     client,
		Runtime:    query.NoTools{},
		Authorizer: query.DenyAll{},
		Request: anthropicapi.MessageRequest{
			Model:  model,
			System: system,
			Tools:  nil,
		},
	})
	if err != nil {
		return fmt.Errorf("construct query engine: %w", err)
	}

	if err := deps.runTUI(session.New(), tui.Config{
		Responder:        engine,
		Version:          version,
		Model:            model.String(),
		WorkingDirectory: workingDirectory,
	}); err != nil {
		return fmt.Errorf("run TUI: %w", err)
	}
	return nil
}
