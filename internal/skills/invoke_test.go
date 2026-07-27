package skills

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"code-cli/internal/core"
)

type recordingExpander struct {
	body       string
	definition Definition
	err        error
}

func (expander *recordingExpander) Expand(_ context.Context, definition Definition, body string) (string, error) {
	expander.body = body
	expander.definition = definition
	return body + "\nexpanded", expander.err
}

func invocationSnapshot(metadata Metadata, body, directory string) *Snapshot {
	definition := Definition{Name: "demo", Source: SourceExplicit, Directory: directory, File: directory + "/SKILL.md", Body: body, Metadata: metadata}
	return assembleSnapshot([]loadedDefinition{{definition: definition, identity: definition.File}}, nil, nil)
}

func TestParseArguments(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"one two", []string{"one", "two"}},
		{`one "two three" 'four five' six\ seven`, []string{"one", "two three", "four five", "six seven"}},
		{`"" ''`, []string{"", ""}},
		{`unterminated "quote`, []string{"unterminated", `"quote`}},
		{`trailing\`, []string{`trailing\`}},
	}
	for _, test := range tests {
		if got := parseArguments(test.input); !reflect.DeepEqual(got, test.want) {
			t.Errorf("parseArguments(%q) = %#v, want %#v", test.input, got, test.want)
		}
	}
}

func TestSubstituteArgumentsOrderingAndBoundaries(t *testing.T) {
	args := `zero "one value" two`
	content := "$first|$second|$missing|$first_more|$ARGUMENTS[0]|$ARGUMENTS[1]|$ARGUMENTS[9]|$0|$2|$20|$ARGUMENTS"
	got := substituteArguments(content, &args, []string{"first", "second", "missing"})
	want := `zero|one value|two|$first_more|zero|one value||zero|two||zero "one value" two`
	if got != want {
		t.Fatalf("substitution = %q, want %q", got, want)
	}
	if got := substituteArguments("body", &args, nil); got != "body\n\nARGUMENTS: "+args {
		t.Fatalf("implicit arguments = %q", got)
	}
	if got := substituteArguments("body", nil, nil); got != "body" {
		t.Fatalf("nil arguments changed content: %q", got)
	}
	if got := substituteArguments("body", new(""), nil); got != "body" {
		t.Fatalf("empty arguments changed content: %q", got)
	}
}

func TestInvokeExpansionOrderSessionAndPlan(t *testing.T) {
	effort := core.EffortMedium
	metadata := defaultMetadata()
	metadata.DisplayName = "Demo Skill"
	metadata.ArgumentNames = []string{"target"}
	metadata.AllowedTools = []string{"Read"}
	metadata.AllowedToolsSpecified = true
	metadata.Model = "sonnet"
	metadata.Effort = &effort
	metadata.Shell = "enabled"
	snapshot := invocationSnapshot(metadata, "$target ${CLAUDE_SKILL_DIR} ${CLAUDE_SESSION_ID}", "/skill/dir")
	expander := &recordingExpander{}
	plan, err := snapshot.Invoke(context.Background(), "demo", InvocationOptions{
		Origin: OriginUser, Args: new("world"), SessionID: "session-123", ShellExpander: expander,
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if expander.body != "world /skill/dir session-123" {
		t.Fatalf("shell input = %q", expander.body)
	}
	if plan.Instructions != "Base directory for this skill: /skill/dir\n\nworld /skill/dir session-123\nexpanded" {
		t.Fatalf("instructions = %q", plan.Instructions)
	}
	if plan.Name != "demo" || plan.DisplayName != "Demo Skill" || plan.Source != SourceExplicit || plan.Model != "sonnet" || plan.Effort == nil || *plan.Effort != effort {
		t.Fatalf("plan = %#v", plan)
	}
	plan.AllowedTools[0] = "mutated"
	plan.Effort = nil
	definition, _ := snapshot.Lookup("demo")
	if definition.Metadata.AllowedTools[0] != "Read" || definition.Metadata.Effort == nil {
		t.Fatal("invocation plan mutated snapshot")
	}
}

func TestInvokeOriginAndUnsupportedMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata Metadata
		options  InvocationOptions
		want     error
		contains string
	}{
		{"model disabled", Metadata{UserInvocable: true, Context: "inline", DisableModelInvocation: true}, InvocationOptions{}, ErrModelInvocationOff, ""},
		{"user disabled", Metadata{Context: "inline", UserInvocable: false}, InvocationOptions{Origin: OriginUser}, ErrUserInvocationOff, ""},
		{"unknown origin", defaultMetadata(), InvocationOptions{Origin: "automation"}, nil, "unsupported skill invocation origin"},
		{"fork", Metadata{UserInvocable: true, Context: "fork"}, InvocationOptions{Origin: OriginUser}, ErrForkContextUnsupported, ""},
		{"agent", Metadata{UserInvocable: true, Context: "inline", Agent: "worker"}, InvocationOptions{Origin: OriginUser}, ErrForkContextUnsupported, ""},
		{"hooks", Metadata{UserInvocable: true, Context: "inline", Hooks: map[string]any{"pre": true}}, InvocationOptions{Origin: OriginUser}, ErrHooksUnsupported, ""},
		{"shell without expander", Metadata{UserInvocable: true, Context: "inline", Shell: "yes"}, InvocationOptions{Origin: OriginUser}, ErrShellUnsupported, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := invocationSnapshot(test.metadata, "body", "/skill")
			_, err := snapshot.Invoke(context.Background(), "demo", test.options)
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if test.contains != "" && (err == nil || !strings.Contains(err.Error(), test.contains)) {
				t.Fatalf("error = %v, want substring %q", err, test.contains)
			}
		})
	}
}

func TestInvokeErrorsAndCancellation(t *testing.T) {
	metadata := defaultMetadata()
	snapshot := invocationSnapshot(metadata, "${CLAUDE_SESSION_ID}", "/skill")
	if _, err := snapshot.Invoke(context.Background(), "demo", InvocationOptions{Origin: OriginUser}); !errors.Is(err, ErrSessionIDRequired) {
		t.Fatalf("missing session error = %v", err)
	}
	if _, err := snapshot.Invoke(context.Background(), "missing", InvocationOptions{}); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("missing skill error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := snapshot.Invoke(ctx, "demo", InvocationOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	expander := &recordingExpander{err: errors.New("expand failed")}
	metadata.Shell = "yes"
	snapshot = invocationSnapshot(metadata, "body", "/skill")
	if _, err := snapshot.Invoke(context.Background(), "demo", InvocationOptions{Origin: OriginUser, ShellExpander: expander}); err == nil || !strings.Contains(err.Error(), "expand failed") {
		t.Fatalf("expander error = %v", err)
	}
}
