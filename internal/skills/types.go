// Package skills discovers, loads, and invokes standalone Claude Code skills.
package skills

import (
	"context"
	"errors"
	"maps"
	"slices"

	"code-cli/internal/core"
)

var (
	ErrSkillNotFound          = errors.New("skill not found")
	ErrModelInvocationOff     = errors.New("skill disables model invocation")
	ErrUserInvocationOff      = errors.New("skill disables user invocation")
	ErrSkillInactive          = errors.New("skill is not active")
	ErrForkContextUnsupported = errors.New("forked skill context is unsupported")
	ErrHooksUnsupported       = errors.New("skill hooks are unsupported")
	ErrShellUnsupported       = errors.New("skill shell expansion is unsupported")
	ErrSessionIDRequired      = errors.New("skill requires a session ID")
	ErrInvalidBundledFile     = errors.New("invalid bundled skill file")
)

// Source identifies where a skill definition was loaded from.
type Source string

const (
	SourceBundled        Source = "bundled"
	SourceManaged        Source = "managed"
	SourceUser           Source = "user"
	SourceProject        Source = "project"
	SourceAdditional     Source = "additional"
	SourceLegacyCommand  Source = "commands_DEPRECATED"
	SourceDynamicProject Source = "dynamic-project"
	SourceExplicit       Source = "explicit"
)

// InvocationOrigin identifies who requested a skill.
type InvocationOrigin string

const (
	OriginModel InvocationOrigin = "model"
	OriginUser  InvocationOrigin = "user"
)

// Metadata contains parsed SKILL.md frontmatter.
type Metadata struct {
	DisplayName            string
	Description            string
	WhenToUse              string
	ArgumentHint           string
	ArgumentNames          []string
	Version                string
	AllowedTools           []string
	AllowedToolsSpecified  bool
	Model                  string
	Effort                 *core.Effort
	DisableModelInvocation bool
	UserInvocable          bool
	Paths                  []string
	Context                string
	Agent                  string
	Hooks                  map[string]any
	Shell                  string
}

// Definition is one immutable skill or legacy command.
type Definition struct {
	Name      string
	Aliases   []string
	Source    Source
	Directory string
	File      string
	Body      string
	Metadata  Metadata
	Bundled   *BundledContent
}

// Summary describes one currently active model-invocable skill.
type Summary struct {
	Name        string
	Description string
}

// Diagnostic reports a candidate that automatic discovery skipped.
type Diagnostic struct {
	Source Source
	Path   string
	Name   string
	Err    error
}

// ChangeSet describes an atomic manager snapshot replacement.
type ChangeSet struct {
	Added       []string
	Removed     []string
	Diagnostics []Diagnostic
}

// ShellExpander expands trusted local shell directives. Implementations own all
// authorization and process execution policy.
type ShellExpander interface {
	Expand(context.Context, Definition, string) (string, error)
}

// InvocationOptions supplies host state that is intentionally not global.
type InvocationOptions struct {
	Origin        InvocationOrigin
	Args          *string
	SessionID     string
	ShellExpander ShellExpander
}

// InvocationPlan contains instructions and declarative host effects.
type InvocationPlan struct {
	Name                  string
	DisplayName           string
	Source                Source
	Instructions          string
	AllowedTools          []string
	AllowedToolsSpecified bool
	Model                 string
	Effort                *core.Effort
	Context               string
	Agent                 string
	Hooks                 map[string]any
}

// Snapshot is an immutable resolved skill catalog.
type Snapshot struct {
	definitions map[string]Definition
	aliases     map[string]string
	identities  map[string]string
	active      map[string]bool
	summaries   []Summary
	diagnostics []Diagnostic
}

// Definitions returns defensive copies in canonical-name order.
func (snapshot *Snapshot) Definitions() []Definition {
	if snapshot == nil {
		return nil
	}
	names := slices.Sorted(maps.Keys(snapshot.definitions))
	result := make([]Definition, 0, len(names))
	for _, name := range names {
		result = append(result, cloneDefinition(snapshot.definitions[name]))
	}
	return result
}

// Summaries returns model-invocable active skills in deterministic order.
func (snapshot *Snapshot) Summaries() []Summary {
	if snapshot == nil {
		return nil
	}
	return append([]Summary(nil), snapshot.summaries...)
}

// Diagnostics returns automatic-discovery warnings.
func (snapshot *Snapshot) Diagnostics() []Diagnostic {
	if snapshot == nil {
		return nil
	}
	return append([]Diagnostic(nil), snapshot.diagnostics...)
}

// IsActive reports whether a resolved skill is active in this snapshot.
func (snapshot *Snapshot) IsActive(name string) bool {
	if snapshot == nil {
		return false
	}
	if resolved, ok := snapshot.aliases[name]; ok {
		name = resolved
	}
	return snapshot.active[name]
}

// Lookup resolves an exact canonical name or alias.
func (snapshot *Snapshot) identity(name string) string {
	if snapshot == nil {
		return ""
	}
	if resolved, ok := snapshot.aliases[name]; ok {
		name = resolved
	}
	return snapshot.identities[name]
}

func (snapshot *Snapshot) Lookup(name string) (Definition, bool) {
	if snapshot == nil {
		return Definition{}, false
	}
	canonical := name
	if resolved, ok := snapshot.aliases[name]; ok {
		canonical = resolved
	}
	definition, ok := snapshot.definitions[canonical]
	if !ok {
		return Definition{}, false
	}
	return cloneDefinition(definition), true
}

func cloneDefinition(definition Definition) Definition {
	definition.Aliases = append([]string(nil), definition.Aliases...)
	definition.Metadata = cloneMetadata(definition.Metadata)
	if definition.Bundled != nil {
		bundled := cloneBundledContent(*definition.Bundled)
		definition.Bundled = &bundled
	}
	return definition
}

func cloneMetadata(metadata Metadata) Metadata {
	metadata.ArgumentNames = append([]string(nil), metadata.ArgumentNames...)
	metadata.AllowedTools = append([]string(nil), metadata.AllowedTools...)
	metadata.Paths = append([]string(nil), metadata.Paths...)
	metadata.Hooks = cloneStringAnyMap(metadata.Hooks)
	if metadata.Effort != nil {
		effort := *metadata.Effort
		metadata.Effort = &effort
	}
	return metadata
}

func cloneStringAnyMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = cloneAny(item)
	}
	return result
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneStringAnyMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneAny(item)
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}
