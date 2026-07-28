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
	"code-cli/internal/tools/skill"
	"code-cli/internal/tools/webfetch"
	"code-cli/internal/tools/websearch"
)

var (
	ErrToolNotFound  = errors.New("tool not found")
	ErrToolDisabled  = errors.New("tool disabled")
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
	Skill     skill.Config
	Provider  string
	Model     core.ModelID
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
	SessionID string
	Progress  ProgressFunc
}

// InjectedMessage is conversation content emitted separately from a tool result.
type InjectedMessage struct {
	Message         core.Message
	IsMeta          bool
	SourceToolUseID string
}

// ContextEffects declares optional context changes for a host to apply.
type ContextEffects struct {
	AllowedTools          []string
	AllowedToolsSpecified bool
	Model                 string
	Effort                *core.Effort
}

// ExecutionResult retains typed host output and its normalized model result.
type ExecutionResult struct {
	CanonicalName  string
	Output         any
	ToolResult     core.ContentBlock
	NewMessages    []InjectedMessage
	ContextEffects *ContextEffects
}

// Registry is an immutable collection of the retained concrete tools.
type Registry struct {
	entries   []Tool
	byName    map[string]int
	skillTool *skill.Tool
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
	skillTool, err := skill.New(config.Skill)
	if err != nil {
		return nil, fmt.Errorf("construct %s tool: %w", skill.ToolName, err)
	}

	now := config.Now
	if now == nil {
		now = config.WebSearch.Now
	}
	if now == nil {
		now = time.Now
	}
	config.WebSearch.Now = now
	webSearchTool := websearch.New(config.WebSearch)
	provider := config.Provider
	if provider == "" {
		provider = "firstParty"
	}
	model := config.Model
	if model == "" {
		model = core.DefaultModel
	}
	webSearchEnabled := websearch.IsEnabled(provider, model)
	entries := []Tool{
		newBashEntry(bashTool),
		newGrepEntry(grepTool),
		newWebFetchEntry(webFetchTool),
		newWebSearchEntry(webSearchTool, websearch.Definition(now()), webSearchEnabled),
		newBriefEntry(briefTool),
		newSkillEntry(skillTool),
	}
	registry, err := buildRegistry(entries)
	if err != nil {
		return nil, err
	}
	registry.skillTool = skillTool
	return registry, nil
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

// Enabled returns enabled concrete entries in stable model-facing order.
func (registry *Registry) Enabled() []Tool {
	if registry == nil {
		return nil
	}
	result := make([]Tool, 0, len(registry.entries))
	for _, entry := range registry.entries {
		if entry.IsEnabled() {
			result = append(result, cloneTool(entry))
		}
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

// EnabledDefinitions returns enabled canonical definitions in stable order.
func (registry *Registry) EnabledDefinitions() []core.ToolDefinition {
	if registry == nil {
		return nil
	}
	result := make([]core.ToolDefinition, 0, len(registry.entries))
	for _, entry := range registry.entries {
		if entry.IsEnabled() {
			result = append(result, cloneDefinition(entry.definition))
		}
	}
	return result
}

// Skills returns model-invocable configured skill summaries.
func (registry *Registry) Skills() []skill.Summary {
	if registry == nil || registry.skillTool == nil {
		return nil
	}
	return registry.skillTool.Available()
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

// Classify resolves an enabled tool and strictly classifies its input.
func (registry *Registry) Classify(name string, input json.RawMessage) (InputClassification, error) {
	tool, ok := registry.Lookup(name)
	if !ok {
		return InputClassification{}, fmt.Errorf("%w: %q", ErrToolNotFound, name)
	}
	if !tool.IsEnabled() {
		return InputClassification{}, fmt.Errorf("%w: %q", ErrToolDisabled, name)
	}
	return tool.ClassifyInput(input)
}

// Execute resolves and invokes one tool by canonical name or alias.
func (registry *Registry) Execute(ctx context.Context, name string, input json.RawMessage, options ExecuteOptions) (ExecutionResult, error) {
	tool, ok := registry.Lookup(name)
	if !ok {
		return ExecutionResult{}, fmt.Errorf("%w: %q", ErrToolNotFound, name)
	}
	if !tool.IsEnabled() {
		return ExecutionResult{}, fmt.Errorf("%w: %q", ErrToolDisabled, name)
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
		if entry.classify == nil {
			return nil, fmt.Errorf("tool %q has no classifier", name)
		}
		if entry.maxResultSizeChars <= 0 {
			return nil, fmt.Errorf("tool %q has invalid maximum result size", name)
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
	definition.Strict = true
	return buildTool(toolSpec{
		definition:         definition,
		maxResultSizeChars: 30_000,
		classify: classifyWith(definition.Name, bash.ParseInput, func(bash.Input) InputClassification {
			return InputClassification{}
		}),
		execute: func(ctx context.Context, raw json.RawMessage, options ExecuteOptions) (ExecutionResult, error) {
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
		},
	})
}

func newGrepEntry(tool *grep.Tool) Tool {
	definition := grep.Definition()
	definition.Strict = true
	return buildTool(toolSpec{
		definition:         definition,
		maxResultSizeChars: 20_000,
		classify: classifyWith(definition.Name, grep.ParseInput, func(grep.Input) InputClassification {
			return InputClassification{ConcurrencySafe: true, ReadOnly: true}
		}),
		execute: func(ctx context.Context, raw json.RawMessage, options ExecuteOptions) (ExecutionResult, error) {
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
		},
	})
}

func newWebFetchEntry(tool *webfetch.WebFetchTool) Tool {
	definition := webfetch.Definition()
	return buildTool(toolSpec{
		definition:         definition,
		maxResultSizeChars: 100_000,
		classify: classifyWith(definition.Name, webfetch.ParseInput, func(webfetch.Input) InputClassification {
			return InputClassification{ConcurrencySafe: true, ReadOnly: true}
		}),
		execute: func(ctx context.Context, raw json.RawMessage, options ExecuteOptions) (ExecutionResult, error) {
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
		},
	})
}

func newWebSearchEntry(tool *websearch.WebSearchTool, definition core.ToolDefinition, enabled bool) Tool {
	return buildTool(toolSpec{
		definition:         definition,
		maxResultSizeChars: 100_000,
		enabled:            func() bool { return enabled },
		classify: classifyWith(definition.Name, websearch.ParseInput, func(websearch.Input) InputClassification {
			return InputClassification{ConcurrencySafe: true, ReadOnly: true}
		}),
		execute: func(ctx context.Context, raw json.RawMessage, options ExecuteOptions) (ExecutionResult, error) {
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
		},
	})
}

func newSkillEntry(tool *skill.Tool) Tool {
	definition := skill.Definition()
	return buildTool(toolSpec{
		definition:         definition,
		maxResultSizeChars: skill.MaxResultSizeChars,
		classify: classifyWith(definition.Name, skill.ParseInput, func(skill.Input) InputClassification {
			return InputClassification{}
		}),
		execute: func(ctx context.Context, raw json.RawMessage, options ExecuteOptions) (ExecutionResult, error) {
			input, err := skill.ParseInput(raw)
			if err != nil {
				return ExecutionResult{}, invalidInputError(definition.Name, err)
			}
			launched, callErr := tool.CallModel(ctx, input, options.SessionID)
			if callErr != nil {
				if errors.Is(callErr, skill.ErrSkillNotFound) ||
					errors.Is(callErr, skill.ErrModelInvocationOff) ||
					errors.Is(callErr, skill.ErrSkillInactive) ||
					errors.Is(callErr, skill.ErrForkContextUnsupported) ||
					errors.Is(callErr, skill.ErrHooksUnsupported) ||
					errors.Is(callErr, skill.ErrShellUnsupported) ||
					errors.Is(callErr, skill.ErrSessionIDRequired) {
					return ExecutionResult{}, invalidInputError(definition.Name, callErr)
				}
				return failedResult(definition.Name, launched.Output, options.ToolUseID, callErr), executionError(definition.Name, callErr)
			}
			result := normalizedResult(
				definition.Name,
				launched.Output,
				"Launching skill: "+launched.Output.CommandName,
				false,
				options.ToolUseID,
			)
			result.NewMessages = []InjectedMessage{{
				Message:         core.UserMessage(launched.Instructions),
				IsMeta:          true,
				SourceToolUseID: options.ToolUseID,
			}}
			if launched.AllowedToolsSpecified || launched.Model != "" || launched.Effort != nil {
				effects := &ContextEffects{
					AllowedTools:          append([]string(nil), launched.AllowedTools...),
					AllowedToolsSpecified: launched.AllowedToolsSpecified,
					Model:                 launched.Model,
				}
				if launched.Effort != nil {
					effort := *launched.Effort
					effects.Effort = &effort
				}
				result.ContextEffects = effects
			}
			return result, nil
		},
	})
}

func newBriefEntry(tool *brief.Tool) Tool {
	definition := brief.Definition()
	return buildTool(toolSpec{
		definition:         definition,
		aliases:            brief.Aliases(),
		maxResultSizeChars: 100_000,
		classify: classifyWith(definition.Name, brief.ParseInput, func(brief.Input) InputClassification {
			return InputClassification{ConcurrencySafe: true, ReadOnly: true}
		}),
		execute: func(ctx context.Context, raw json.RawMessage, options ExecuteOptions) (ExecutionResult, error) {
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
		},
	})
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
