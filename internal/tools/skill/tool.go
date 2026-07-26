package skill

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"code-cli/internal/core"
)

var (
	ErrSkillNotFound          = errors.New("skill not found")
	ErrModelInvocationOff     = errors.New("skill disables model invocation")
	ErrForkContextUnsupported = errors.New("forked skill context is unsupported")
)

// Output is the typed host-facing result of an inline skill launch.
type Output struct {
	Success      bool     `json:"success"`
	CommandName  string   `json:"command_name"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Model        string   `json:"model,omitempty"`
	Inline       bool     `json:"inline"`
}

// Result contains the skill launch output and declarative conversation effects.
type Result struct {
	Output       Output
	Instructions string
	AllowedTools []string
	Model        string
	Effort       *core.Effort
}

// Tool is an immutable catalog of explicitly configured local skills.
type Tool struct {
	entries   map[string]entry
	summaries []Summary
}

// New snapshots all configured local skills.
func New(config Config) (*Tool, error) {
	entries, summaries, err := loadCatalog(config)
	if err != nil {
		return nil, err
	}
	return &Tool{entries: entries, summaries: summaries}, nil
}

// Available returns model-invocable skill summaries in deterministic order.
func (tool *Tool) Available() []Summary {
	if tool == nil {
		return nil
	}
	return append([]Summary(nil), tool.summaries...)
}

// Call loads one snapshotted skill into the main conversation.
func (tool *Tool) Call(ctx context.Context, input Input) (Result, error) {
	if tool == nil {
		return Result{}, errors.New("skill tool is nil")
	}
	if ctx == nil {
		ctx = context.TODO()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := ValidateInput(&input); err != nil {
		return Result{}, err
	}
	skillEntry, exists := tool.entries[input.Skill]
	if !exists {
		return Result{}, fmt.Errorf("%w: %s", ErrSkillNotFound, input.Skill)
	}
	if skillEntry.metadata.disableModelInvocation {
		return Result{}, fmt.Errorf("%w: %s", ErrModelInvocationOff, input.Skill)
	}
	if skillEntry.metadata.forked {
		return Result{}, fmt.Errorf("%w: %s", ErrForkContextUnsupported, input.Skill)
	}
	body := strings.ReplaceAll(
		skillEntry.body,
		"${CLAUDE_SKILL_DIR}",
		skillEntry.directory,
	)
	body = substituteArguments(body, input.Args, skillEntry.metadata.argumentNames)
	instructions := "Base directory for this skill: " + skillEntry.directory + "\n\n" + body
	allowedTools := append([]string(nil), skillEntry.metadata.allowedTools...)
	var effort *core.Effort
	if skillEntry.metadata.effort != nil {
		value := *skillEntry.metadata.effort
		effort = &value
	}
	return Result{
		Output: Output{
			Success:      true,
			CommandName:  skillEntry.name,
			AllowedTools: append([]string(nil), allowedTools...),
			Model:        skillEntry.metadata.model,
			Inline:       true,
		},
		Instructions: instructions,
		AllowedTools: allowedTools,
		Model:        skillEntry.metadata.model,
		Effort:       effort,
	}, nil
}
