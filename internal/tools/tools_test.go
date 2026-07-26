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
	wantNames := []string{"Bash", "Grep", "WebFetch", "WebSearch", "SendUserMessage"}

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
		})
	}
	if len(progress) != 2 ||
		progress[0].ToolName != "WebSearch" || progress[0].Type != string(websearch.ProgressQueryUpdate) || progress[0].ToolUseID != "toolu_WebSearch" || progress[0].OperationID != "search-progress-1" || progress[0].Query != "current Go" ||
		progress[1].ToolName != "WebSearch" || progress[1].Type != string(websearch.ProgressResultsReceived) || progress[1].ToolUseID != "toolu_WebSearch" || progress[1].OperationID != "srv_1" || progress[1].Query != "current Go" || progress[1].ResultCount != 1 {
		t.Fatalf("progress = %#v", progress)
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

	execute := func(context.Context, json.RawMessage, ExecuteOptions) (ExecutionResult, error) {
		return ExecutionResult{}, nil
	}
	_, err = buildRegistry([]Tool{
		{definition: core.ToolDefinition{Name: "One", InputSchema: json.RawMessage(`{"type":"object"}`)}, aliases: []string{"shared"}, execute: execute},
		{definition: core.ToolDefinition{Name: "Two", InputSchema: json.RawMessage(`{"type":"object"}`)}, aliases: []string{"shared"}, execute: execute},
	})
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("collision error = %v", err)
	}
}

func newTestRegistry(t *testing.T, stream anthropicapi.Stream) *Registry {
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
	registry, err := NewRegistry(Config{
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
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
