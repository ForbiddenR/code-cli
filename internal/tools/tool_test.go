package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"code-cli/internal/core"
)

func TestBuildToolDefaultsAndClassification(t *testing.T) {
	definition := core.ToolDefinition{
		Name:        "Example",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	parse := func(data []byte) (struct{}, error) {
		if string(data) != `{}` {
			return struct{}{}, errors.New("expected empty object")
		}
		return struct{}{}, nil
	}
	tool := buildTool(toolSpec{
		definition:         definition,
		maxResultSizeChars: 123,
		classify:           classifyWith(definition.Name, parse, nil),
		execute: func(context.Context, json.RawMessage, ExecuteOptions) (ExecutionResult, error) {
			return ExecutionResult{}, nil
		},
	})

	if !tool.IsEnabled() || tool.MaxResultSizeChars() != 123 {
		t.Fatalf("enabled = %t, max = %d", tool.IsEnabled(), tool.MaxResultSizeChars())
	}
	classification, err := tool.ClassifyInput(json.RawMessage(`{}`))
	if err != nil || classification != (InputClassification{}) {
		t.Fatalf("classification = %#v, error = %v", classification, err)
	}
	if _, err := tool.ClassifyInput(json.RawMessage(`{"value":true}`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid classification error = %v", err)
	}

	definition.InputSchema[0] = 'x'
	if !json.Valid(tool.Definition().InputSchema) {
		t.Fatal("buildTool retained mutable definition input")
	}
}

func TestBuildRegistryRejectsIncompleteToolSpecs(t *testing.T) {
	execute := func(context.Context, json.RawMessage, ExecuteOptions) (ExecutionResult, error) {
		return ExecutionResult{}, nil
	}
	classify := func(json.RawMessage) (InputClassification, error) {
		return InputClassification{}, nil
	}
	valid := Tool{
		definition:         core.ToolDefinition{Name: "Example", InputSchema: json.RawMessage(`{"type":"object"}`)},
		execute:            execute,
		classify:           classify,
		maxResultSizeChars: 1,
	}

	tests := []struct {
		name string
		tool Tool
		want string
	}{
		{name: "missing executor", tool: func() Tool { tool := valid; tool.execute = nil; return tool }(), want: "no executor"},
		{name: "missing classifier", tool: func() Tool { tool := valid; tool.classify = nil; return tool }(), want: "no classifier"},
		{name: "invalid maximum", tool: func() Tool { tool := valid; tool.maxResultSizeChars = 0; return tool }(), want: "maximum result size"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := buildRegistry([]Tool{test.tool}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("buildRegistry() error = %v", err)
			}
		})
	}
}
