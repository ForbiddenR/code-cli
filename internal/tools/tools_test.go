package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"code-cli/internal/anthropicapi"
	"code-cli/internal/core"
	"code-cli/internal/tools/bash"
	"code-cli/internal/tools/brief"
	"code-cli/internal/tools/grep"
	"code-cli/internal/tools/skill"
	"code-cli/internal/tools/webfetch"
	"code-cli/internal/tools/websearch"
)

type registryDoerFunc func(*http.Request) (*http.Response, error)

func (function registryDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

type registryClient struct {
	stream anthropicapi.Stream
}

func (client *registryClient) CreateMessage(context.Context, anthropicapi.MessageRequest, ...anthropicapi.CallOption) (*anthropicapi.MessageResponse, error) {
	return nil, errors.New("unexpected CreateMessage call")
}

func (client *registryClient) StreamMessage(context.Context, anthropicapi.MessageRequest, ...anthropicapi.CallOption) (anthropicapi.Stream, error) {
	return client.stream, nil
}

func (client *registryClient) CountTokens(context.Context, anthropicapi.TokenCountRequest, ...anthropicapi.CallOption) (*anthropicapi.TokenCountResponse, error) {
	return nil, errors.New("unexpected CountTokens call")
}

type registryStream struct {
	events chan anthropicapi.StreamEvent
}

func newRegistryStream(events ...anthropicapi.StreamEvent) *registryStream {
	channel := make(chan anthropicapi.StreamEvent, len(events))
	for _, event := range events {
		channel <- event
	}
	close(channel)
	return &registryStream{events: channel}
}

func (stream *registryStream) Events() <-chan anthropicapi.StreamEvent { return stream.events }
func (stream *registryStream) Close() error                            { return nil }

func TestRegistryEnumeratesAndLooksUpConcreteTools(t *testing.T) {
	registry := newTestRegistry(t, newRegistryStream())
	wantNames := []string{"Bash", "Grep", "WebFetch", "WebSearch", "SendUserMessage", "Skill"}

	entries := registry.All()
	definitions := registry.Definitions()
	if len(entries) != len(wantNames) || len(definitions) != len(wantNames) {
		t.Fatalf("entries = %d, definitions = %d", len(entries), len(definitions))
	}
	for index, want := range wantNames {
		if entries[index].Name() != want || definitions[index].Name != want || !json.Valid(definitions[index].InputSchema) {
			t.Fatalf("entry %d = %q, definition = %#v", index, entries[index].Name(), definitions[index])
		}
	}
	if _, ok := registry.Lookup(websearch.ServerToolName); ok {
		t.Fatal("hosted web_search was registered as a local tool")
	}
	canonical, ok := registry.Lookup("SendUserMessage")
	if !ok {
		t.Fatal("canonical Brief tool not found")
	}
	alias, ok := registry.Lookup("Brief")
	if !ok || alias.Name() != canonical.Name() || !reflect.DeepEqual(alias.Aliases(), []string{"Brief"}) {
		t.Fatalf("alias = %#v, found = %t", alias, ok)
	}
	for _, name := range []string{"brief", "bash", "missing"} {
		if _, ok := registry.Lookup(name); ok {
			t.Fatalf("case-mismatched or unknown name %q resolved", name)
		}
	}

	definitions[0].InputSchema[0] = 'x'
	entries[4].aliases[0] = "changed"
	freshDefinitions := registry.Definitions()
	freshBrief, _ := registry.Lookup("Brief")
	if !json.Valid(freshDefinitions[0].InputSchema) || !reflect.DeepEqual(freshBrief.Aliases(), []string{"Brief"}) {
		t.Fatal("registry returned mutable internal state")
	}
}

func TestRegistryToolMetadataAndClassification(t *testing.T) {
	registry := newTestRegistry(t, newRegistryStream())
	tests := []struct {
		name           string
		input          json.RawMessage
		maxResultChars int
		strict         bool
		classification InputClassification
	}{
		{name: "Bash", input: json.RawMessage(`{"command":"printf ok"}`), maxResultChars: 30_000, strict: true},
		{name: "Grep", input: json.RawMessage(`{"pattern":"ok"}`), maxResultChars: 20_000, strict: true, classification: InputClassification{ConcurrencySafe: true, ReadOnly: true}},
		{name: "WebFetch", input: json.RawMessage(`{"url":"https://example.com","prompt":"read"}`), maxResultChars: 100_000, classification: InputClassification{ConcurrencySafe: true, ReadOnly: true}},
		{name: "WebSearch", input: json.RawMessage(`{"query":"current Go"}`), maxResultChars: 100_000, classification: InputClassification{ConcurrencySafe: true, ReadOnly: true}},
		{name: "SendUserMessage", input: json.RawMessage(`{"message":"done","status":"normal"}`), maxResultChars: 100_000, classification: InputClassification{ConcurrencySafe: true, ReadOnly: true}},
		{name: "Skill", input: json.RawMessage(`{"skill":"review","args":"src"}`), maxResultChars: 100_000},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool, ok := registry.Lookup(test.name)
			if !ok {
				t.Fatal("tool not found")
			}
			definition := tool.Definition()
			if !tool.IsEnabled() || tool.MaxResultSizeChars() != test.maxResultChars || definition.Strict != test.strict {
				t.Fatalf("enabled = %t, max = %d, definition = %#v", tool.IsEnabled(), tool.MaxResultSizeChars(), definition)
			}
			classification, err := tool.ClassifyInput(test.input)
			if err != nil || classification != test.classification {
				t.Fatalf("classification = %#v, error = %v", classification, err)
			}
			if _, err := tool.ClassifyInput(json.RawMessage(`{"unexpected":true}`)); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("invalid classification error = %v", err)
			}
		})
	}
}

func TestRegistryEnabledViewsAndDisabledDispatch(t *testing.T) {
	registry := newTestRegistryWithConfig(t, newRegistryStream(), func(config *Config) {
		config.Provider = "bedrock"
	})
	if len(registry.All()) != 6 || len(registry.Definitions()) != 6 {
		t.Fatalf("exhaustive entries = %d, definitions = %d", len(registry.All()), len(registry.Definitions()))
	}
	wantEnabled := []string{"Bash", "Grep", "WebFetch", "SendUserMessage", "Skill"}
	enabled := registry.Enabled()
	definitions := registry.EnabledDefinitions()
	if len(enabled) != len(wantEnabled) || len(definitions) != len(wantEnabled) {
		t.Fatalf("enabled entries = %d, definitions = %d", len(enabled), len(definitions))
	}
	for index, name := range wantEnabled {
		if enabled[index].Name() != name || definitions[index].Name != name {
			t.Fatalf("enabled %d = %q, definition = %q", index, enabled[index].Name(), definitions[index].Name)
		}
	}
	search, ok := registry.Lookup("WebSearch")
	if !ok || search.IsEnabled() {
		t.Fatalf("disabled WebSearch = %#v, found = %t", search, ok)
	}
	if _, err := registry.Execute(context.Background(), "WebSearch", json.RawMessage(`not-json`), ExecuteOptions{}); !errors.Is(err, ErrToolDisabled) {
		t.Fatalf("disabled WebSearch error = %v", err)
	}

	executed := false
	disabled := buildTool(toolSpec{
		definition:         core.ToolDefinition{Name: "SendUserMessage", InputSchema: json.RawMessage(`{"type":"object"}`)},
		aliases:            []string{"Brief"},
		enabled:            func() bool { return false },
		maxResultSizeChars: 1,
		classify: func(json.RawMessage) (InputClassification, error) {
			return InputClassification{}, errors.New("classifier must not run")
		},
		execute: func(context.Context, json.RawMessage, ExecuteOptions) (ExecutionResult, error) {
			executed = true
			return ExecutionResult{}, nil
		},
	})
	disabledRegistry, err := buildRegistry([]Tool{disabled})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SendUserMessage", "Brief"} {
		if _, err := disabledRegistry.Execute(context.Background(), name, json.RawMessage(`not-json`), ExecuteOptions{}); !errors.Is(err, ErrToolDisabled) {
			t.Fatalf("disabled %s error = %v", name, err)
		}
	}
	if executed {
		t.Fatal("disabled tool executed")
	}

	var nilRegistry *Registry
	if nilRegistry.Enabled() != nil || nilRegistry.EnabledDefinitions() != nil || nilRegistry.Skills() != nil {
		t.Fatal("nil registry enabled views were not safe")
	}
}

func TestRegistryDispatchesEveryConcreteTool(t *testing.T) {
	searchStream := newRegistryStream(
		anthropicapi.StreamEvent{
			Type:  anthropicapi.StreamEventContentBlockStart,
			Index: 0,
			Block: &core.ContentBlock{Type: core.ContentBlockServerToolUse, ID: "srv_1", Name: websearch.ServerToolName},
		},
		anthropicapi.StreamEvent{
			Type:  anthropicapi.StreamEventContentBlockDelta,
			Index: 0,
			Delta: &anthropicapi.ContentDelta{Type: "input_json_delta", PartialJSON: `{"query":"current Go"}`},
		},
		anthropicapi.StreamEvent{
			Type:  anthropicapi.StreamEventContentBlockStart,
			Index: 1,
			Block: &core.ContentBlock{
				Type:      core.ContentBlockWebSearchToolResult,
				ToolUseID: "srv_1",
				Content:   []core.ContentBlock{{Type: core.ContentBlockWebSearchResult, Title: "Go", URL: "https://go.dev"}},
			},
		},
	)
	registry := newTestRegistry(t, searchStream)

	grepPath := filepath.Join(t.TempDir(), "match.txt")
	if err := os.WriteFile(grepPath, []byte("match\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	grepInput, _ := json.Marshal(map[string]any{"pattern": "match", "path": grepPath, "output_mode": "content"})

	tests := []struct {
		name       string
		input      json.RawMessage
		wantText   string
		outputType any
	}{
		{name: "Bash", input: json.RawMessage(`{"command":"ok"}`), wantText: "bash-ok", outputType: bash.Output{}},
		{name: "Grep", input: grepInput, wantText: "match", outputType: grep.Output{}},
		{name: "WebFetch", input: json.RawMessage(`{"url":"https://go.dev/doc","prompt":"read"}`), wantText: "# Go", outputType: webfetch.Output{}},
		{name: "WebSearch", input: json.RawMessage(`{"query":"current Go"}`), wantText: "https://go.dev", outputType: websearch.Output{}},
		{name: "Brief", input: json.RawMessage(`{"message":"done","status":"normal"}`), wantText: "Message delivered", outputType: brief.Output{}},
	}

	var progress []ProgressEvent
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), test.name, test.input, ExecuteOptions{
				ToolUseID: "toolu_" + test.name,
				Progress:  func(event ProgressEvent) { progress = append(progress, event) },
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			wantCanonical := test.name
			if test.name == "Brief" {
				wantCanonical = "SendUserMessage"
			}
			if result.CanonicalName != wantCanonical || result.ToolResult.Type != core.ContentBlockToolResult || result.ToolResult.ToolUseID != "toolu_"+test.name || result.ToolResult.IsError || len(result.ToolResult.Content) != 1 || !strings.Contains(result.ToolResult.Content[0].Text, test.wantText) {
				t.Fatalf("result = %#v", result)
			}
			if test.outputType != nil && reflect.TypeOf(result.Output) != reflect.TypeOf(test.outputType) {
				t.Fatalf("output type = %T, want %T", result.Output, test.outputType)
			}
			if result.NewMessages != nil || result.ContextEffects != nil {
				t.Fatalf("ordinary tool returned Skill effects: %#v", result)
			}
		})
	}
	if len(progress) != 2 ||
		progress[0].ToolName != "WebSearch" || progress[0].Type != string(websearch.ProgressQueryUpdate) || progress[0].ToolUseID != "toolu_WebSearch" || progress[0].OperationID != "search-progress-1" || progress[0].Query != "current Go" ||
		progress[1].ToolName != "WebSearch" || progress[1].Type != string(websearch.ProgressResultsReceived) || progress[1].ToolUseID != "toolu_WebSearch" || progress[1].OperationID != "srv_1" || progress[1].Query != "current Go" || progress[1].ResultCount != 1 {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestRegistryDispatchesSkill(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "review")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `---
description: Review code
when_to_use: Use before merging
allowed-tools: [Read, Grep, Read]
model: claude-opus-4-8
effort: high
arguments: [target]
---
Review $target from ${CLAUDE_SKILL_DIR}.`
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"hidden": "---\ndisable-model-invocation: true\n---\nHidden",
		"forked": "---\ncontext: fork\n---\nForked",
	} {
		skillDirectory := filepath.Join(root, name)
		if err := os.Mkdir(skillDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillDirectory, "SKILL.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	registry := newTestRegistryWithConfig(t, newRegistryStream(), func(config *Config) {
		config.Skill = skill.Config{Roots: []string{root}}
	})

	summaries := registry.Skills()
	wantSummaries := []skill.Summary{{Name: "review", Description: "Review code Use before merging"}}
	if !reflect.DeepEqual(summaries, wantSummaries) {
		t.Fatalf("Skills() = %#v, want %#v", summaries, wantSummaries)
	}
	summaries[0].Name = "changed"
	if registry.Skills()[0].Name != "review" {
		t.Fatal("Skills returned mutable registry state")
	}

	result, err := registry.Execute(
		context.Background(),
		"Skill",
		json.RawMessage(`{"skill":"/review","args":"src/main.go"}`),
		ExecuteOptions{ToolUseID: "toolu_skill"},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	output, ok := result.Output.(skill.Output)
	if !ok || !output.Success || !output.Inline || output.CommandName != "review" {
		t.Fatalf("output = %#v", result.Output)
	}
	if result.CanonicalName != "Skill" || result.ToolResult.ToolUseID != "toolu_skill" || result.ToolResult.IsError || len(result.ToolResult.Content) != 1 || result.ToolResult.Content[0].Text != "Launching skill: review" {
		t.Fatalf("tool result = %#v", result)
	}
	if len(result.NewMessages) != 1 || !result.NewMessages[0].IsMeta || result.NewMessages[0].SourceToolUseID != "toolu_skill" || result.NewMessages[0].Message.Role != core.RoleUser || len(result.NewMessages[0].Message.Content) != 1 {
		t.Fatalf("new messages = %#v", result.NewMessages)
	}
	canonicalDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	instructions := result.NewMessages[0].Message.Content[0].Text
	if !strings.Contains(instructions, "Base directory for this skill: "+canonicalDirectory) || !strings.Contains(instructions, "Review src/main.go from "+canonicalDirectory+".") {
		t.Fatalf("instructions = %q", instructions)
	}
	if result.ContextEffects == nil || !reflect.DeepEqual(result.ContextEffects.AllowedTools, []string{"Read", "Grep"}) || result.ContextEffects.Model != "claude-opus-4-8" || result.ContextEffects.Effort == nil || *result.ContextEffects.Effort != core.EffortHigh {
		t.Fatalf("context effects = %#v", result.ContextEffects)
	}

	for _, name := range []string{"missing", "../review", "hidden", "forked"} {
		input, marshalErr := json.Marshal(map[string]string{"skill": name})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err := registry.Execute(context.Background(), "Skill", input, ExecuteOptions{ToolUseID: "toolu_bad"}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid skill %q error = %v", name, err)
		}
	}
}

func TestRegistryErrorsAndFailureOutput(t *testing.T) {
	registry := newTestRegistry(t, newRegistryStream())

	if _, err := registry.Execute(context.Background(), "missing", json.RawMessage(`{}`), ExecuteOptions{ToolUseID: "id"}); !errors.Is(err, ErrToolNotFound) {
		t.Fatalf("unknown tool error = %v", err)
	}
	if _, err := registry.Execute(context.Background(), "Bash", json.RawMessage(`{"command":"ok","extra":true}`), ExecuteOptions{ToolUseID: "id"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid input error = %v", err)
	}
	if _, err := registry.Execute(context.Background(), "Bash", json.RawMessage(`{"command":"ok"}`), ExecuteOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty tool-use ID error = %v", err)
	}
	result, err := registry.Execute(context.Background(), "Bash", json.RawMessage(`{"command":"fail"}`), ExecuteOptions{ToolUseID: "id"})
	if !errors.Is(err, ErrToolExecution) || !errors.Is(err, bash.ErrExecution) {
		t.Fatalf("failure error = %v", err)
	}
	output, ok := result.Output.(bash.Output)
	if !ok || output.ExitCode != 7 || !result.ToolResult.IsError || !strings.Contains(result.ToolResult.Content[0].Text, "bash-failed") {
		t.Fatalf("failure result = %#v", result)
	}

	var nilRegistry *Registry
	if _, ok := nilRegistry.Lookup("Bash"); ok || nilRegistry.All() != nil || nilRegistry.Definitions() != nil {
		t.Fatal("nil registry access was not safe")
	}
}

func TestRegistryPreservesPartialGrepOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.txt")
	if err := os.WriteFile(path, []byte("match\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := newGrepEntry(grep.New(grep.Config{Runner: grep.RunnerFunc(func(_ context.Context, _ string, _ []string, _ int) (grep.ProcessResult, error) {
		return grep.ProcessResult{Stdout: path + ":1:match\n", ExitCode: -1, Overflow: true}, nil
	})}))
	input, _ := json.Marshal(map[string]any{"pattern": "match", "path": path, "output_mode": "content"})

	result, err := entry.Execute(context.Background(), input, ExecuteOptions{ToolUseID: "toolu_partial"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	output, ok := result.Output.(grep.Output)
	if !ok || !output.Partial || result.ToolResult.IsError || !strings.Contains(result.ToolResult.Content[0].Text, "results are partial") {
		t.Fatalf("partial result = %#v", result)
	}
}

func TestRegistryConstructionValidation(t *testing.T) {
	webSearchTime := time.Date(2040, time.September, 1, 0, 0, 0, 0, time.UTC)
	registry, err := NewRegistry(Config{
		Bash:      bash.Config{WorkingDirectory: t.TempDir()},
		WebSearch: websearch.Config{Now: func() time.Time { return webSearchTime }},
	})
	if err != nil || !strings.Contains(registry.Definitions()[3].Description, "September 2040") {
		t.Fatalf("WebSearch definition clock was not inherited: registry=%#v error=%v", registry, err)
	}

	if _, err := NewRegistry(Config{Bash: bash.Config{WorkingDirectory: filepath.Join(t.TempDir(), "missing")}}); err == nil || !strings.Contains(err.Error(), "construct Bash tool") {
		t.Fatalf("constructor error = %v", err)
	}
	if _, err := NewRegistry(Config{
		Bash:  bash.Config{WorkingDirectory: t.TempDir()},
		Skill: skill.Config{Roots: []string{filepath.Join(t.TempDir(), "missing")}},
	}); err == nil || !strings.Contains(err.Error(), "construct Skill tool") {
		t.Fatalf("Skill constructor error = %v", err)
	}

	execute := func(context.Context, json.RawMessage, ExecuteOptions) (ExecutionResult, error) {
		return ExecutionResult{}, nil
	}
	classify := func(json.RawMessage) (InputClassification, error) {
		return InputClassification{}, nil
	}
	_, err = buildRegistry([]Tool{
		{definition: core.ToolDefinition{Name: "One", InputSchema: json.RawMessage(`{"type":"object"}`)}, aliases: []string{"shared"}, execute: execute, classify: classify, maxResultSizeChars: 1},
		{definition: core.ToolDefinition{Name: "Two", InputSchema: json.RawMessage(`{"type":"object"}`)}, aliases: []string{"shared"}, execute: execute, classify: classify, maxResultSizeChars: 1},
	})
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("collision error = %v", err)
	}
}

func newTestRegistry(t *testing.T, stream anthropicapi.Stream) *Registry {
	t.Helper()
	return newTestRegistryWithConfig(t, stream, nil)
}

func newTestRegistryWithConfig(t *testing.T, stream anthropicapi.Stream, configure func(*Config)) *Registry {
	t.Helper()
	directory := t.TempDir()
	grepRunner := grep.RunnerFunc(func(_ context.Context, _ string, args []string, _ int) (grep.ProcessResult, error) {
		target := args[len(args)-1]
		return grep.ProcessResult{Stdout: fmt.Sprintf("%s:1:match\n", target), ExitCode: 0}, nil
	})
	bashRunner := bash.RunnerFunc(func(_ context.Context, request bash.RunRequest) (bash.ProcessResult, error) {
		command := request.Args[len(request.Args)-1]
		if command == "fail" {
			return bash.ProcessResult{CombinedOutput: "bash-failed\n", ExitCode: 7, Started: true}, nil
		}
		return bash.ProcessResult{CombinedOutput: "bash-ok\n", ExitCode: 0, Started: true}, nil
	})
	httpClient := registryDoerFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        http.Header{"Content-Type": []string{"text/markdown"}},
			Body:          io.NopCloser(strings.NewReader("# Go")),
			ContentLength: 4,
		}, nil
	})
	fixed := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	config := Config{
		Bash: bash.Config{WorkingDirectory: directory, Runner: bashRunner, Now: func() time.Time { return fixed }},
		Grep: grep.Config{Runner: grepRunner},
		WebFetch: webfetch.Config{
			HTTPClient:    httpClient,
			SkipPreflight: true,
			Now:           func() time.Time { return fixed },
		},
		WebSearch: websearch.Config{
			Client: &registryClient{stream: stream},
		},
		Now: func() time.Time { return fixed },
	}
	if configure != nil {
		configure(&config)
	}
	registry, err := NewRegistry(config)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
