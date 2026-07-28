package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"code-cli/internal/core"
	"code-cli/internal/tools"
)

// ToolRuntime is the narrow tool boundary consumed by Engine.
type ToolRuntime interface {
	EnabledDefinitions() []core.ToolDefinition
	Classify(name string, input json.RawMessage) (tools.InputClassification, error)
	Execute(ctx context.Context, name string, input json.RawMessage, options tools.ExecuteOptions) (tools.ExecutionResult, error)
}

// NoTools is a concrete runtime that advertises and executes no tools.
type NoTools struct{}

func (NoTools) EnabledDefinitions() []core.ToolDefinition { return nil }

func (NoTools) Classify(name string, _ json.RawMessage) (tools.InputClassification, error) {
	return tools.InputClassification{}, fmt.Errorf("%w: %q", tools.ErrToolNotFound, name)
}

func (NoTools) Execute(ctx context.Context, name string, _ json.RawMessage, _ tools.ExecuteOptions) (tools.ExecutionResult, error) {
	if ctx == nil {
		return tools.ExecutionResult{}, ErrNilContext
	}
	return tools.ExecutionResult{}, fmt.Errorf("%w: %q", tools.ErrToolNotFound, name)
}

// ToolCall is the authorization view of one model-requested tool invocation.
type ToolCall struct {
	ID             string
	Name           string
	Input          json.RawMessage
	Classification tools.InputClassification
}

// Authorizer decides whether a classified tool call may execute.
type Authorizer interface {
	Authorize(context.Context, ToolCall) error
}

// AuthorizeFunc adapts a function to Authorizer.
type AuthorizeFunc func(context.Context, ToolCall) error

func (fn AuthorizeFunc) Authorize(ctx context.Context, call ToolCall) error {
	if ctx == nil {
		return ErrNilContext
	}
	if fn == nil {
		return errors.New("tool authorization is unavailable")
	}
	return fn(ctx, call)
}

// AllowAll authorizes every classified tool call.
type AllowAll struct{}

func (AllowAll) Authorize(ctx context.Context, _ ToolCall) error {
	if ctx == nil {
		return ErrNilContext
	}
	return nil
}

// DenyAll rejects every tool call.
type DenyAll struct{}

func (DenyAll) Authorize(ctx context.Context, _ ToolCall) error {
	if ctx == nil {
		return ErrNilContext
	}
	return errors.New("tool use denied")
}
