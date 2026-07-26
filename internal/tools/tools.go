// Package tools assembles and dispatches the concrete tools retained by code-cli.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"code-cli/internal/core"
	"code-cli/internal/tools/bash"
	"code-cli/internal/tools/brief"
	"code-cli/internal/tools/grep"
	"code-cli/internal/tools/webfetch"
	"code-cli/internal/tools/websearch"
)

var (
	ErrToolNotFound  = errors.New("tool not found")
	ErrInvalidInput  = errors.New("invalid tool input")
	ErrToolExecution = errors.New("tool execution failed")
)

// Config supplies the package-specific dependencies for every concrete tool.
type Config struct {
	Bash      bash.Config
	Grep      grep.Config
	Brief     brief.Config
	WebFetch  webfetch.Config
	WebSearch websearch.Config
	Now       func() time.Time // Overrides the shared WebSearch definition and execution clock.
}

// ProgressEvent is the registry-level representation of tool progress.
type ProgressEvent struct {
	ToolName    string `json:"toolName"`
	ToolUseID   string `json:"toolUseID"`
	OperationID string `json:"operationID,omitempty"`
	Type        string `json:"type"`
	Query       string `json:"query,omitempty"`
	ResultCount int    `json:"resultCount,omitempty"`
}

// ProgressFunc receives synchronous best-effort progress updates.
type ProgressFunc func(ProgressEvent)

// ExecuteOptions supplies call metadata that is not part of model input.
type ExecuteOptions struct {
	ToolUseID string
	Progress  ProgressFunc
}

// ExecutionResult retains typed host output and its normalized model result.
type ExecutionResult struct {
	CanonicalName string
	Output        any
	ToolResult    core.ContentBlock
}

type executeFunc func(context.Context, json.RawMessage, ExecuteOptions) (ExecutionResult, error)

// Tool is one immutable concrete registry entry.
type Tool struct {
	definition core.ToolDefinition
	aliases    []string
	execute    executeFunc
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

// Registry is an immutable collection of the retained concrete tools.
type Registry struct {
	entries []Tool
	byName  map[string]int
}

// NewRegistry constructs every retained concrete tool atomically.
func NewRegistry(config Config) (*Registry, error) {
	bashTool, err := bash.New(config.Bash)
	if err != nil {
		return nil, fmt.Errorf("construct %s tool: %w", bash.ToolName, err)
	}
	grepTool := grep.New(config.Grep)
	webFetchTool := webfetch.New(config.WebFetch)
	briefTool := brief.New(config.Brief)

	now := config.Now
	if now == nil {
		now = config.WebSearch.Now
	}
	if now == nil {
		now = time.Now
	}
	config.WebSearch.Now = now
	webSearchTool := websearch.New(config.WebSearch)
	entries := []Tool{
		newBashEntry(bashTool),
		newGrepEntry(grepTool),
		newWebFetchEntry(webFetchTool),
		newWebSearchEntry(webSearchTool, websearch.Definition(now())),
		newBriefEntry(briefTool),
	}
	return buildRegistry(entries)
}

// All returns all concrete entries in stable model-facing order.
func (registry *Registry) All() []Tool {
	if registry == nil {
		return nil
	}
	result := make([]Tool, len(registry.entries))
	for index, entry := range registry.entries {
		result[index] = cloneTool(entry)
	}
	return result
}

// Definitions returns every canonical custom-tool definition in stable order.
func (registry *Registry) Definitions() []core.ToolDefinition {
	if registry == nil {
		return nil
	}
	result := make([]core.ToolDefinition, len(registry.entries))
	for index, entry := range registry.entries {
		result[index] = cloneDefinition(entry.definition)
	}
	return result
}

// Lookup resolves an exact canonical name or alias.
func (registry *Registry) Lookup(name string) (Tool, bool) {
	if registry == nil {
		return Tool{}, false
	}
	index, ok := registry.byName[name]
	if !ok {
		return Tool{}, false
	}
	return cloneTool(registry.entries[index]), true
}

// Execute resolves and invokes one tool by canonical name or alias.
func (registry *Registry) Execute(ctx context.Context, name string, input json.RawMessage, options ExecuteOptions) (ExecutionResult, error) {
	tool, ok := registry.Lookup(name)
	if !ok {
		return ExecutionResult{}, fmt.Errorf("%w: %q", ErrToolNotFound, name)
	}
	return tool.Execute(ctx, input, options)
}

func buildRegistry(entries []Tool) (*Registry, error) {
	registry := &Registry{
		entries: make([]Tool, len(entries)),
		byName:  make(map[string]int),
	}
	owners := make(map[string]string)
	for index, entry := range entries {
		entry = cloneTool(entry)
		name := entry.definition.Name
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("tool canonical name is empty")
		}
		if !json.Valid(entry.definition.InputSchema) {
			return nil, fmt.Errorf("tool %q has invalid input schema", name)
		}
		if entry.execute == nil {
			return nil, fmt.Errorf("tool %q has no executor", name)
		}
		for _, candidate := range append([]string{name}, entry.aliases...) {
			if strings.TrimSpace(candidate) == "" {
				return nil, fmt.Errorf("tool %q has an empty alias", name)
			}
			if owner, exists := owners[candidate]; exists {
				return nil, fmt.Errorf("tool name or alias %q collides with %q", candidate, owner)
			}
			registry.byName[candidate] = index
			owners[candidate] = name
		}
		registry.entries[index] = entry
	}
	return registry, nil
}

func newBashEntry(tool *bash.Tool) Tool {
	definition := bash.Definition()
	return Tool{definition: definition, execute: func(ctx context.Context, raw json.RawMessage, options ExecuteOptions) (ExecutionResult, error) {
		input, err := bash.ParseInput(raw)
		if err != nil {
			return ExecutionResult{}, invalidInputError(definition.Name, err)
		}
		output, callErr := tool.Call(ctx, input)
		mapped := bash.MapToolResultToToolResultBlockParam(output, options.ToolUseID)
		result := normalizedResult(definition.Name, output, mapped.Content, mapped.IsError, options.ToolUseID)
		if callErr != nil {
			return result, executionError(definition.Name, callErr)
		}
		return result, nil
	}}
}

func newGrepEntry(tool *grep.Tool) Tool {
	definition := grep.Definition()
	return Tool{definition: definition, execute: func(ctx context.Context, raw json.RawMessage, options ExecuteOptions) (ExecutionResult, error) {
		input, err := grep.ParseInput(raw)
		if err != nil {
			return ExecutionResult{}, invalidInputError(definition.Name, err)
		}
		output, callErr := tool.Call(ctx, input)
		if callErr != nil {
			return failedResult(definition.Name, output, options.ToolUseID, callErr), executionError(definition.Name, callErr)
		}
		mapped := grep.MapToolResultToToolResultBlockParam(output, options.ToolUseID)
		return normalizedResult(definition.Name, output, mapped.Content, false, options.ToolUseID), nil
	}}
}

func newWebFetchEntry(tool *webfetch.WebFetchTool) Tool {
	definition := webfetch.Definition()
	return Tool{definition: definition, execute: func(ctx context.Context, raw json.RawMessage, options ExecuteOptions) (ExecutionResult, error) {
		input, err := webfetch.ParseInput(raw)
		if err != nil {
			return ExecutionResult{}, invalidInputError(definition.Name, err)
		}
		output, callErr := tool.Call(ctx, input)
		if callErr != nil {
			return failedResult(definition.Name, output, options.ToolUseID, callErr), executionError(definition.Name, callErr)
		}
		mapped := webfetch.MapToolResultToToolResultBlockParam(output, options.ToolUseID)
		return normalizedResult(definition.Name, output, mapped.Content, false, options.ToolUseID), nil
	}}
}

func newWebSearchEntry(tool *websearch.WebSearchTool, definition core.ToolDefinition) Tool {
	return Tool{definition: definition, execute: func(ctx context.Context, raw json.RawMessage, options ExecuteOptions) (ExecutionResult, error) {
		input, err := websearch.ParseInput(raw)
		if err != nil {
			return ExecutionResult{}, invalidInputError(definition.Name, err)
		}
		var progress websearch.ProgressFunc
		if options.Progress != nil {
			progress = func(event websearch.ProgressEvent) {
				options.Progress(ProgressEvent{
					ToolName:    definition.Name,
					ToolUseID:   options.ToolUseID,
					OperationID: event.ToolUseID,
					Type:        string(event.Type),
					Query:       event.Query,
					ResultCount: event.ResultCount,
				})
			}
		}
		output, callErr := tool.Call(ctx, input, progress)
		if callErr != nil {
			return failedResult(definition.Name, output, options.ToolUseID, callErr), executionError(definition.Name, callErr)
		}
		mapped := websearch.MapToolResultToToolResultBlockParam(output, options.ToolUseID)
		return normalizedResult(definition.Name, output, mapped.Content, false, options.ToolUseID), nil
	}}
}

func newBriefEntry(tool *brief.Tool) Tool {
	definition := brief.Definition()
	return Tool{definition: definition, aliases: brief.Aliases(), execute: func(ctx context.Context, raw json.RawMessage, options ExecuteOptions) (ExecutionResult, error) {
		input, err := brief.ParseInput(raw)
		if err != nil {
			return ExecutionResult{}, invalidInputError(definition.Name, err)
		}
		output, callErr := tool.Call(ctx, input)
		if callErr != nil {
			return failedResult(definition.Name, output, options.ToolUseID, callErr), executionError(definition.Name, callErr)
		}
		mapped := brief.MapToolResultToToolResultBlockParam(output, options.ToolUseID)
		return normalizedResult(definition.Name, output, mapped.Content, false, options.ToolUseID), nil
	}}
}

func normalizedResult(name string, output any, content string, isError bool, toolUseID string) ExecutionResult {
	return ExecutionResult{
		CanonicalName: name,
		Output:        output,
		ToolResult:    core.ToolResultBlock(toolUseID, []core.ContentBlock{core.TextBlock(content)}, isError),
	}
}

func failedResult(name string, output any, toolUseID string, err error) ExecutionResult {
	return normalizedResult(name, output, "Error: "+err.Error(), true, toolUseID)
}

func invalidInputError(name string, err error) error {
	return fmt.Errorf("%w for %s: %w", ErrInvalidInput, name, err)
}

func executionError(name string, err error) error {
	return fmt.Errorf("%w for %s: %w", ErrToolExecution, name, err)
}

func cloneTool(tool Tool) Tool {
	return Tool{
		definition: cloneDefinition(tool.definition),
		aliases:    append([]string(nil), tool.aliases...),
		execute:    tool.execute,
	}
}

func cloneDefinition(definition core.ToolDefinition) core.ToolDefinition {
	definition.InputSchema = append(json.RawMessage(nil), definition.InputSchema...)
	return definition
}
