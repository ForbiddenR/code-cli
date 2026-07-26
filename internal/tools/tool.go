package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"code-cli/internal/core"
)

// InputClassification describes scheduling and safety-relevant tool metadata.
// It is descriptive only and does not authorize execution.
type InputClassification struct {
	ConcurrencySafe bool
	ReadOnly        bool
	Destructive     bool
	OpenWorld       bool
}

type executeFunc func(context.Context, json.RawMessage, ExecuteOptions) (ExecutionResult, error)
type classifyFunc func(json.RawMessage) (InputClassification, error)

// Tool is one immutable concrete registry entry.
type Tool struct {
	definition         core.ToolDefinition
	aliases            []string
	execute            executeFunc
	classify           classifyFunc
	enabled            func() bool
	maxResultSizeChars int
}

type toolSpec struct {
	definition         core.ToolDefinition
	aliases            []string
	execute            executeFunc
	classify           classifyFunc
	enabled            func() bool
	maxResultSizeChars int
}

// Name returns the canonical model-facing tool name.
func (tool Tool) Name() string {
	return tool.definition.Name
}

// Definition returns a defensive copy of the model-facing declaration.
func (tool Tool) Definition() core.ToolDefinition {
	return cloneDefinition(tool.definition)
}

// Aliases returns accepted compatibility names that are not separately advertised.
func (tool Tool) Aliases() []string {
	return append([]string(nil), tool.aliases...)
}

// IsEnabled reports whether the tool is available in the current registry policy.
func (tool Tool) IsEnabled() bool {
	if tool.enabled == nil {
		return true
	}
	return tool.enabled()
}

// MaxResultSizeChars returns the maximum model-facing result size for the tool.
func (tool Tool) MaxResultSizeChars() int {
	return tool.maxResultSizeChars
}

// ClassifyInput strictly parses input before returning tool metadata.
func (tool Tool) ClassifyInput(input json.RawMessage) (InputClassification, error) {
	if tool.classify == nil {
		return InputClassification{}, fmt.Errorf("%w for %s: classifier is unavailable", ErrInvalidInput, tool.Name())
	}
	return tool.classify(input)
}

// Execute strictly parses and invokes this concrete tool.
func (tool Tool) Execute(ctx context.Context, input json.RawMessage, options ExecuteOptions) (ExecutionResult, error) {
	if tool.execute == nil {
		return ExecutionResult{}, fmt.Errorf("%w: tool %q has no executor", ErrToolExecution, tool.Name())
	}
	if ctx == nil {
		return ExecutionResult{}, fmt.Errorf("%w for %s: context is nil", ErrToolExecution, tool.Name())
	}
	if strings.TrimSpace(options.ToolUseID) == "" {
		return ExecutionResult{}, fmt.Errorf("%w for %s: tool-use ID is empty", ErrInvalidInput, tool.Name())
	}
	return tool.execute(ctx, input, options)
}

func buildTool(spec toolSpec) Tool {
	return Tool{
		definition:         cloneDefinition(spec.definition),
		aliases:            append([]string(nil), spec.aliases...),
		execute:            spec.execute,
		classify:           spec.classify,
		enabled:            spec.enabled,
		maxResultSizeChars: spec.maxResultSizeChars,
	}
}

func classifyWith[T any](name string, parse func([]byte) (T, error), classify func(T) InputClassification) classifyFunc {
	return func(raw json.RawMessage) (InputClassification, error) {
		input, err := parse(raw)
		if err != nil {
			return InputClassification{}, invalidInputError(name, err)
		}
		if classify == nil {
			return InputClassification{}, nil
		}
		return classify(input), nil
	}
}

func cloneTool(tool Tool) Tool {
	return Tool{
		definition:         cloneDefinition(tool.definition),
		aliases:            append([]string(nil), tool.aliases...),
		execute:            tool.execute,
		classify:           tool.classify,
		enabled:            tool.enabled,
		maxResultSizeChars: tool.maxResultSizeChars,
	}
}

func cloneDefinition(definition core.ToolDefinition) core.ToolDefinition {
	definition.InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
	return definition
}
