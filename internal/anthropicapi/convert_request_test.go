package anthropicapi

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"code-cli/internal/core"
)

func TestNewMessageParamsPreservesRequestShape(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)
	req := MessageRequest{
		MaxTokens: 128,
		System: []core.SystemBlock{
			{Type: core.ContentBlockText, Text: "You are concise.", CacheControl: &core.CacheControl{Type: core.CacheControlEphemeral}},
		},
		Messages: []core.Message{
			{
				Role: core.RoleUser,
				Content: []core.ContentBlock{
					{Type: core.ContentBlockText, Text: "What is the weather?", CacheControl: &core.CacheControl{Type: core.CacheControlEphemeral}},
				},
			},
		},
		Tools: []core.ToolDefinition{
			{Name: "weather", Description: "Get weather.", InputSchema: schema},
		},
		Thinking:      new(core.DefaultThinking()),
		OutputConfig:  &core.OutputConfig{Effort: core.EffortHigh},
		StopSequences: []string{"STOP"},
		Metadata:      map[string]string{"user_id": "user-123"},
	}

	params, err := newMessageParams(req)
	if err != nil {
		t.Fatalf("newMessageParams() error = %v", err)
	}
	if params.Model != "claude-opus-4-8" {
		t.Fatalf("default model = %q", params.Model)
	}
	if params.MaxTokens != 128 {
		t.Fatalf("max tokens = %d", params.MaxTokens)
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal params JSON: %v", err)
	}

	assertJSONPath(t, got, "model", "claude-opus-4-8")
	assertJSONPath(t, got, "messages.0.role", "user")
	assertJSONPath(t, got, "messages.0.content.0.cache_control.type", "ephemeral")
	assertJSONPath(t, got, "system.0.cache_control.type", "ephemeral")
	assertJSONPath(t, got, "tools.0.name", "weather")
	assertJSONPath(t, got, "tools.0.description", "Get weather.")
	assertJSONPath(t, got, "tools.0.input_schema.properties.city.type", "string")
	assertJSONPath(t, got, "thinking.type", "adaptive")
	assertJSONPath(t, got, "output_config.effort", "high")
	assertJSONPath(t, got, "stop_sequences.0", "STOP")
	assertJSONPath(t, got, "metadata.user_id", "user-123")
}

func TestNewTokenCountParamsUsesDefaultModel(t *testing.T) {
	params, err := newTokenCountParams(TokenCountRequest{
		Messages: []core.Message{core.UserMessage("count me")},
	})
	if err != nil {
		t.Fatalf("newTokenCountParams() error = %v", err)
	}
	if params.Model != "claude-opus-4-8" {
		t.Fatalf("default model = %q", params.Model)
	}
}

func TestApplyOptions(t *testing.T) {
	timeout := 30 * time.Second
	opts := ApplyOptions(
		WithTimeout(timeout),
		WithBeta("fine-grained-tool-streaming"),
		WithHeader("x-test", "1"),
	)
	if opts.Timeout != timeout {
		t.Fatalf("timeout = %s", opts.Timeout)
	}
	if len(opts.Betas) != 1 || opts.Betas[0] != "fine-grained-tool-streaming" {
		t.Fatalf("betas = %#v", opts.Betas)
	}
	if opts.Headers["x-test"] != "1" {
		t.Fatalf("headers = %#v", opts.Headers)
	}
}

//go:fix inline
func ptr[T any](value T) *T {
	return new(value)
}

func assertJSONPath(t *testing.T, root any, path string, want any) {
	t.Helper()
	got := lookupJSONPath(t, root, path)
	if got != want {
		t.Fatalf("%s = %#v, want %#v", path, got, want)
	}
}

func lookupJSONPath(t *testing.T, root any, path string) any {
	t.Helper()
	current := root
	for _, part := range splitPath(path) {
		switch value := current.(type) {
		case map[string]any:
			current = value[part]
		case []any:
			var index int
			if _, err := fmt.Sscanf(part, "%d", &index); err != nil {
				t.Fatalf("path segment %q is not a list index", part)
			}
			if index < 0 || index >= len(value) {
				t.Fatalf("path index %d out of range for %q", index, path)
			}
			current = value[index]
		default:
			t.Fatalf("cannot descend into %T at %q", current, part)
		}
	}
	return current
}

func splitPath(path string) []string {
	var parts []string
	start := 0
	for i, r := range path {
		if r == '.' {
			parts = append(parts, path[start:i])
			start = i + 1
		}
	}
	return append(parts, path[start:])
}
