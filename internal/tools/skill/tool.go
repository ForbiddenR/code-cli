package skill

import (
	"context"
	"errors"

	"code-cli/internal/core"
	skillsdomain "code-cli/internal/skills"
)

var (
	ErrSkillNotFound          = skillsdomain.ErrSkillNotFound
	ErrModelInvocationOff     = skillsdomain.ErrModelInvocationOff
	ErrUserInvocationOff      = skillsdomain.ErrUserInvocationOff
	ErrSkillInactive          = skillsdomain.ErrSkillInactive
	ErrForkContextUnsupported = skillsdomain.ErrForkContextUnsupported
	ErrHooksUnsupported       = skillsdomain.ErrHooksUnsupported
	ErrShellUnsupported       = skillsdomain.ErrShellUnsupported
	ErrSessionIDRequired      = skillsdomain.ErrSessionIDRequired
)

// Output is the typed host-facing result of an inline skill launch.
type Output struct {
	Success               bool     `json:"success"`
	CommandName           string   `json:"command_name"`
	AllowedTools          []string `json:"allowed_tools,omitempty"`
	AllowedToolsSpecified bool     `json:"allowed_tools_specified,omitempty"`
	Model                 string   `json:"model,omitempty"`
	Inline                bool     `json:"inline"`
}

// Result contains the skill launch output and declarative conversation effects.
type Result struct {
	Output                Output
	Instructions          string
	AllowedTools          []string
	AllowedToolsSpecified bool
	Model                 string
	Effort                *core.Effort
	Source                skillsdomain.Source
}

// Tool adapts an immutable snapshot or refreshable manager to the model tool.
type Tool struct {
	snapshot *skillsdomain.Snapshot
	manager  *skillsdomain.Manager
}

// New snapshots configured roots or uses the supplied shared manager.
func New(config Config) (*Tool, error) {
	if config.Manager != nil {
		if len(config.Roots) != 0 {
			return nil, errors.New("skill roots and manager are mutually exclusive")
		}
		return &Tool{manager: config.Manager}, nil
	}
	snapshot, err := skillsdomain.LoadStrict(config.Roots)
	if err != nil {
		return nil, err
	}
	return &Tool{snapshot: snapshot}, nil
}

// Available returns model-invocable skill summaries in deterministic order.
func (tool *Tool) Available() []Summary {
	if tool == nil {
		return nil
	}
	if tool.manager != nil {
		return tool.manager.Summaries()
	}
	return tool.snapshot.Summaries()
}

// Call loads one skill into the main conversation as a model invocation.
func (tool *Tool) Call(ctx context.Context, input Input) (Result, error) {
	return tool.CallModel(ctx, input, "")
}

// CallModel loads one model-invoked skill with explicit host session state.
func (tool *Tool) CallModel(ctx context.Context, input Input, sessionID string) (Result, error) {
	return tool.call(ctx, input, skillsdomain.OriginModel, sessionID)
}

// CallUser loads one explicitly user-invoked skill.
func (tool *Tool) CallUser(ctx context.Context, input Input, sessionID string) (Result, error) {
	return tool.call(ctx, input, skillsdomain.OriginUser, sessionID)
}

func (tool *Tool) call(ctx context.Context, input Input, origin skillsdomain.InvocationOrigin, sessionID string) (Result, error) {
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
	options := skillsdomain.InvocationOptions{Origin: origin, Args: input.Args, SessionID: sessionID}
	var (
		plan skillsdomain.InvocationPlan
		err  error
	)
	if tool.manager != nil {
		plan, err = tool.manager.Invoke(ctx, input.Skill, options)
	} else {
		plan, err = tool.snapshot.Invoke(ctx, input.Skill, options)
	}
	if err != nil {
		return Result{}, err
	}
	allowedTools := append([]string(nil), plan.AllowedTools...)
	var effort *core.Effort
	if plan.Effort != nil {
		value := *plan.Effort
		effort = &value
	}
	return Result{
		Output: Output{
			Success:               true,
			CommandName:           plan.Name,
			AllowedTools:          append([]string(nil), allowedTools...),
			AllowedToolsSpecified: plan.AllowedToolsSpecified,
			Model:                 plan.Model,
			Inline:                plan.Context == "inline",
		},
		Instructions:          plan.Instructions,
		AllowedTools:          allowedTools,
		AllowedToolsSpecified: plan.AllowedToolsSpecified,
		Model:                 plan.Model,
		Effort:                effort,
		Source:                plan.Source,
	}, nil
}
