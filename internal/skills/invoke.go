package skills

import (
	"context"
	"fmt"
	"strings"
)

// Invoke resolves and expands one skill from an immutable snapshot.
func (snapshot *Snapshot) Invoke(ctx context.Context, name string, options InvocationOptions) (InvocationPlan, error) {
	if snapshot == nil {
		return InvocationPlan{}, fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}
	if err := ctx.Err(); err != nil {
		return InvocationPlan{}, err
	}
	definition, exists := snapshot.Lookup(name)
	if !exists {
		return InvocationPlan{}, fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}
	if !snapshot.IsActive(name) {
		return InvocationPlan{}, fmt.Errorf("%w: %s", ErrSkillInactive, definition.Name)
	}
	origin := options.Origin
	if origin == "" {
		origin = OriginModel
	}
	switch origin {
	case OriginModel:
		if definition.Metadata.DisableModelInvocation {
			return InvocationPlan{}, fmt.Errorf("%w: %s", ErrModelInvocationOff, definition.Name)
		}
	case OriginUser:
		if !definition.Metadata.UserInvocable {
			return InvocationPlan{}, fmt.Errorf("%w: %s", ErrUserInvocationOff, definition.Name)
		}
	default:
		return InvocationPlan{}, fmt.Errorf("unsupported skill invocation origin %q", origin)
	}
	if definition.Metadata.Context == "fork" || definition.Metadata.Agent != "" {
		return InvocationPlan{}, fmt.Errorf("%w: %s", ErrForkContextUnsupported, definition.Name)
	}
	if len(definition.Metadata.Hooks) > 0 {
		return InvocationPlan{}, fmt.Errorf("%w: %s", ErrHooksUnsupported, definition.Name)
	}
	if definition.Metadata.Shell != "" && options.ShellExpander == nil {
		return InvocationPlan{}, fmt.Errorf("%w: %s", ErrShellUnsupported, definition.Name)
	}

	body := definition.Body
	directory := definition.Directory
	if definition.Bundled != nil {
		var err error
		body, directory, err = definition.Bundled.prompt(ctx, options.Args)
		if err != nil {
			return InvocationPlan{}, fmt.Errorf("build bundled skill %q: %w", definition.Name, err)
		}
	}
	body = substituteArguments(body, options.Args, definition.Metadata.ArgumentNames)
	body = strings.ReplaceAll(body, "${CLAUDE_SKILL_DIR}", directory)
	if strings.Contains(body, "${CLAUDE_SESSION_ID}") {
		if options.SessionID == "" {
			return InvocationPlan{}, fmt.Errorf("%w: %s", ErrSessionIDRequired, definition.Name)
		}
		body = strings.ReplaceAll(body, "${CLAUDE_SESSION_ID}", options.SessionID)
	}
	if options.ShellExpander != nil && definition.Source != SourceBundled {
		expanded, err := options.ShellExpander.Expand(ctx, definition, body)
		if err != nil {
			return InvocationPlan{}, fmt.Errorf("expand skill %q shell directives: %w", definition.Name, err)
		}
		body = expanded
	}
	instructions := body
	if directory != "" {
		instructions = "Base directory for this skill: " + directory + "\n\n" + body
	}
	var effort = definition.Metadata.Effort
	if effort != nil {
		value := *effort
		effort = &value
	}
	displayName := definition.Metadata.DisplayName
	if displayName == "" {
		displayName = definition.Name
	}
	return InvocationPlan{
		Name:                  definition.Name,
		DisplayName:           displayName,
		Source:                definition.Source,
		Instructions:          instructions,
		AllowedTools:          append([]string(nil), definition.Metadata.AllowedTools...),
		AllowedToolsSpecified: definition.Metadata.AllowedToolsSpecified,
		Model:                 definition.Metadata.Model,
		Effort:                effort,
		Context:               definition.Metadata.Context,
		Agent:                 definition.Metadata.Agent,
		Hooks:                 cloneStringAnyMap(definition.Metadata.Hooks),
	}, nil
}
