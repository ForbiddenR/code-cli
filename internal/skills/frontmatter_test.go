package skills

import (
	"reflect"
	"strings"
	"testing"

	"code-cli/internal/core"
)

func TestParseFrontmatterFullFields(t *testing.T) {
	content := "---\n" +
		"name: ' Display Name '\n" +
		"description: ' Description '\n" +
		"when_to_use: ' When useful '\n" +
		"argument-hint: '<one> <two>'\n" +
		"arguments: [first, second, first, '2', '']\n" +
		"version: ' 1.2.3 '\n" +
		"allowed-tools: 'Read, Bash(git status, git diff) Write Read'\n" +
		"model: inherit\n" +
		"effort: high\n" +
		"disable-model-invocation: 'true'\n" +
		"user-invocable: false\n" +
		"paths: ['src/**', '!src/generated/**', '/docs/*.md', 'win\\*.go']\n" +
		"context: fork\n" +
		"agent: helper\n" +
		"hooks:\n  pre:\n    - command: test\n" +
		"shell: enabled\n" +
		"unknown: ignored\n" +
		"---\nBody\n"
	metadata, body, err := parseFrontmatter(content)
	if err != nil {
		t.Fatalf("parseFrontmatter: %v", err)
	}
	if body != "Body\n" {
		t.Fatalf("body = %q", body)
	}
	if metadata.DisplayName != "Display Name" || metadata.Description != "Description" || metadata.WhenToUse != "When useful" || metadata.ArgumentHint != "<one> <two>" || metadata.Version != "1.2.3" {
		t.Fatalf("string fields = %#v", metadata)
	}
	if !reflect.DeepEqual(metadata.ArgumentNames, []string{"first", "second"}) {
		t.Fatalf("arguments = %#v", metadata.ArgumentNames)
	}
	if !reflect.DeepEqual(metadata.AllowedTools, []string{"Read", "Bash(git status, git diff)", "Write"}) || !metadata.AllowedToolsSpecified {
		t.Fatalf("allowed tools = %#v specified=%v", metadata.AllowedTools, metadata.AllowedToolsSpecified)
	}
	if metadata.Model != "" || metadata.Effort == nil || *metadata.Effort != core.EffortHigh {
		t.Fatalf("model/effort = %q/%v", metadata.Model, metadata.Effort)
	}
	if !metadata.DisableModelInvocation || metadata.UserInvocable || metadata.Context != "fork" || metadata.Agent != "helper" || metadata.Shell != "enabled" {
		t.Fatalf("invocation metadata = %#v", metadata)
	}
	if !reflect.DeepEqual(metadata.Paths, []string{"src/**", "!src/generated/**", "/docs/*.md", "win/*.go"}) {
		t.Fatalf("paths = %#v", metadata.Paths)
	}
	if !matchesPathPatterns(metadata.Paths, "docs/readme.md") || matchesPathPatterns(metadata.Paths, "nested/docs/readme.md") {
		t.Fatalf("root-anchored path lost during parsing: %#v", metadata.Paths)
	}
	if metadata.Hooks["pre"] == nil {
		t.Fatalf("hooks = %#v", metadata.Hooks)
	}
}

func TestParseFrontmatterDefaultsAndLineEndings(t *testing.T) {
	metadata, body, err := parseFrontmatter("plain\r\nbody")
	if err != nil || body != "plain\nbody" {
		t.Fatalf("no-frontmatter parse = %#v %q %v", metadata, body, err)
	}
	if !metadata.UserInvocable || metadata.Context != "inline" || metadata.AllowedToolsSpecified {
		t.Fatalf("defaults = %#v", metadata)
	}

	metadata, body, err = parseFrontmatter("---\r\nallowed-tools: []\r\nmodel: sonnet\r\n---\r\ntext")
	if err != nil || body != "text" || metadata.Model != "sonnet" || !metadata.AllowedToolsSpecified || metadata.AllowedTools == nil {
		t.Fatalf("CRLF parse = %#v %q %v", metadata, body, err)
	}
}

func TestParseFrontmatterErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"unterminated", "---\ndescription: x", "unterminated frontmatter"},
		{"bad yaml", "---\ndescription: [\n---\n", "parse frontmatter"},
		{"string type", "---\nname: 4\n---\n", "frontmatter name must be a string"},
		{"bool type", "---\nuser-invocable: yes\n---\n", "frontmatter user-invocable must be a boolean"},
		{"list type", "---\narguments: 4\n---\n", "frontmatter arguments must be a string or string list"},
		{"list member", "---\npaths: [ok, 3]\n---\n", "frontmatter paths must contain only strings"},
		{"effort type", "---\neffort: 3\n---\n", "frontmatter effort must be a string"},
		{"effort value", "---\neffort: impossible\n---\n", "unsupported frontmatter effort"},
		{"context type", "---\ncontext: false\n---\n", "frontmatter context must be a string"},
		{"context value", "---\ncontext: sidecar\n---\n", "unsupported frontmatter context"},
		{"hooks type", "---\nhooks: []\n---\n", "frontmatter hooks must be an object"},
		{"empty path", "---\npaths: ['!']\n---\n", "empty pattern"},
		{"path traversal", "---\npaths: ['src/../secret']\n---\n", "contains traversal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := parseFrontmatter(test.content)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestFrontmatterHelpers(t *testing.T) {
	if got := splitAllowedTools("Read Bash(git status, git diff),Write"); !reflect.DeepEqual(got, []string{"Read", "Bash(git status, git diff)", "Write"}) {
		t.Fatalf("splitAllowedTools = %#v", got)
	}
	if got := descriptionFromBody("\n## Heading\nrest"); got != "Heading" {
		t.Fatalf("descriptionFromBody = %q", got)
	}
	if got := deduplicate([]string{"a", "b", "a"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("deduplicate = %#v", got)
	}
}
