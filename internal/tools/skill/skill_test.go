package skill

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"code-cli/internal/core"
)

func TestParseInput(t *testing.T) {
	empty := ""
	tests := []struct {
		name    string
		data    string
		want    Input
		wantErr bool
	}{
		{name: "minimal", data: `{"skill":"review"}`, want: Input{Skill: "review"}},
		{name: "slash and empty args", data: `{"skill":" /review ","args":""}`, want: Input{Skill: "review", Args: &empty}},
		{name: "unknown field", data: `{"skill":"review","Args":"x"}`, wantErr: true},
		{name: "null", data: `null`, wantErr: true},
		{name: "array", data: `[]`, wantErr: true},
		{name: "null args", data: `{"skill":"review","args":null}`, wantErr: true},
		{name: "trailing", data: `{"skill":"review"}{}`, wantErr: true},
		{name: "traversal", data: `{"skill":"../review"}`, wantErr: true},
		{name: "second slash", data: `{"skill":"//review"}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseInput([]byte(test.data))
			if test.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseInput() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ParseInput() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestArgumentSubstitution(t *testing.T) {
	args := `one "two words" 'three words' four\ five`
	got := substituteArguments(
		`all=$ARGUMENTS indexed=$ARGUMENTS[1] short=$2 named=$first untouched=$firstMore`,
		&args,
		[]string{"first"},
	)
	want := `all=one "two words" 'three words' four\ five indexed=two words short=three words named=one untouched=$firstMore`
	if got != want {
		t.Fatalf("substituteArguments() = %q, want %q", got, want)
	}

	if got := substituteArguments("no placeholders", &args, nil); got != "no placeholders\n\nARGUMENTS: "+args {
		t.Fatalf("append fallback = %q", got)
	}
	if got := substituteArguments("$ARGUMENTS $0", nil, nil); got != "$ARGUMENTS $0" {
		t.Fatalf("absent args changed placeholders: %q", got)
	}
	empty := ""
	if got := substituteArguments("x$ARGUMENTS-$0", &empty, nil); got != "x-" {
		t.Fatalf("empty args substitution = %q", got)
	}
	malformed := `'two words`
	if got := substituteArguments("$0|$1", &malformed, nil); got != "'two|words" {
		t.Fatalf("malformed quote fallback = %q", got)
	}
	literalPlaceholder := "$ARGUMENTS"
	if got := substituteArguments("$ARGUMENTS", &literalPlaceholder, nil); got != "$ARGUMENTS" {
		t.Fatalf("placeholder was mistaken for unused: %q", got)
	}
	singleQuoted := `'one\two'`
	if got := substituteArguments("$0", &singleQuoted, nil); got != `one\two` {
		t.Fatalf("single-quoted backslash = %q", got)
	}
}

func TestParseFrontmatter(t *testing.T) {
	content := "---\r\ndescription: Reviews code\r\nwhen_to_use: Use for review\r\nallowed-tools: Bash(git status), Read Grep\r\nmodel: inherit\r\neffort: high\r\ndisable-model-invocation: \"false\"\r\narguments: [target, \"0\", target]\r\ncontext: inline\r\n---\r\n# Body\r\n"
	got, body, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("parseFrontmatter() error = %v", err)
	}
	if got.description != "Reviews code" || got.whenToUse != "Use for review" || got.model != "" {
		t.Fatalf("metadata = %#v", got)
	}
	if !reflect.DeepEqual(got.allowedTools, []string{"Bash(git status)", "Read", "Grep"}) {
		t.Fatalf("allowed tools = %#v", got.allowedTools)
	}
	if !reflect.DeepEqual(got.argumentNames, []string{"target"}) {
		t.Fatalf("argument names = %#v", got.argumentNames)
	}
	if got.effort == nil || *got.effort != core.EffortHigh {
		t.Fatalf("effort = %#v", got.effort)
	}
	if body != "# Body\n" {
		t.Fatalf("body = %q", body)
	}
	if _, _, err := parseFrontmatter("---\neffort: extreme\n---\nbody"); err == nil {
		t.Fatal("expected invalid effort error")
	}
	if _, _, err := parseFrontmatter("---\ndescription: [bad]\n---\nbody"); err == nil {
		t.Fatal("expected invalid description error")
	}
	if _, _, err := parseFrontmatter("---\ndescription: broken"); err == nil {
		t.Fatal("expected unterminated frontmatter error")
	}
}

func TestToolCatalogAndCall(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeSkill(t, first, "review", `---
description: Review first
when_to_use: Use before merging
allowed-tools: [Read, Grep, Read]
model: claude-opus-4-8
effort: high
arguments: [target]
---
Review $target from ${CLAUDE_SKILL_DIR}.`)
	writeSkill(t, second, "review", "Review second.")
	writeSkill(t, second, "alpha", "# Alpha skill\n\nDo work.")
	writeSkill(t, second, "hidden", "---\ndisable-model-invocation: true\n---\nHidden")
	writeSkill(t, second, "forked", "---\ncontext: fork\n---\nForked")

	tool, err := New(Config{Roots: []string{first, second}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	wantSummaries := []Summary{
		{Name: "alpha", Description: "Alpha skill"},
		{Name: "review", Description: "Review first Use before merging"},
	}
	if got := tool.Available(); !reflect.DeepEqual(got, wantSummaries) {
		t.Fatalf("Available() = %#v, want %#v", got, wantSummaries)
	}
	available := tool.Available()
	available[0].Name = "changed"
	if tool.Available()[0].Name != "alpha" {
		t.Fatal("Available returned mutable catalog state")
	}

	args := `src/main.go`
	result, err := tool.Call(context.Background(), Input{Skill: "/review", Args: &args})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !result.Output.Success || !result.Output.Inline || result.Output.CommandName != "review" {
		t.Fatalf("output = %#v", result.Output)
	}
	if result.Model != "claude-opus-4-8" || result.Effort == nil || *result.Effort != core.EffortHigh {
		t.Fatalf("effects = model %q, effort %#v", result.Model, result.Effort)
	}
	if !reflect.DeepEqual(result.AllowedTools, []string{"Read", "Grep"}) {
		t.Fatalf("allowed tools = %#v", result.AllowedTools)
	}
	firstCanonical, err := filepath.EvalSymlinks(first)
	if err != nil {
		t.Fatal(err)
	}
	wantDirectory := filepath.Join(firstCanonical, "review")
	if !strings.Contains(result.Instructions, "Base directory for this skill: "+wantDirectory) ||
		!strings.Contains(result.Instructions, "Review src/main.go from "+wantDirectory+".") {
		t.Fatalf("instructions = %q", result.Instructions)
	}

	if _, err := tool.Call(context.Background(), Input{Skill: "Review"}); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("case-sensitive lookup error = %v", err)
	}
	if _, err := tool.Call(context.Background(), Input{Skill: "hidden"}); !errors.Is(err, ErrModelInvocationOff) {
		t.Fatalf("hidden error = %v", err)
	}
	if _, err := tool.Call(context.Background(), Input{Skill: "forked"}); !errors.Is(err, ErrForkContextUnsupported) {
		t.Fatalf("forked error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tool.Call(cancelled, Input{Skill: "review"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call error = %v", err)
	}

	writeSkill(t, first, "review", "Changed after construction.")
	result, err = tool.Call(context.Background(), Input{Skill: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Instructions, "Changed after construction") {
		t.Fatal("catalog was not immutable")
	}
}

func TestCatalogRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeSkill(t, outside, "external", "External")
	if err := os.Symlink(filepath.Join(outside, "external"), filepath.Join(root, "escaped")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := New(Config{Roots: []string{root}}); err == nil || !strings.Contains(err.Error(), "outside configured root") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestInvalidConfiguredRoot(t *testing.T) {
	if _, err := New(Config{Roots: []string{filepath.Join(t.TempDir(), "missing")}}); err == nil {
		t.Fatal("expected missing root error")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Roots: []string{file}}); err == nil {
		t.Fatal("expected non-directory root error")
	}
}

func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
