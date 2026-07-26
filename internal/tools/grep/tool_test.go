package grep

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const wantToolPrompt = "A powerful search tool built on ripgrep\n\n" +
	"  Usage:\n" +
	"  - ALWAYS use Grep for search tasks. NEVER invoke `grep` or `rg` as a Bash command. The Grep tool has been optimized for correct permissions and access.\n" +
	"  - Supports full regex syntax (e.g., \"log.*Error\", \"function\\s+\\w+\")\n" +
	"  - Filter files with glob parameter (e.g., \"*.js\", \"**/*.tsx\") or type parameter (e.g., \"js\", \"py\", \"rust\")\n" +
	"  - Output modes: \"content\" shows matching lines, \"files_with_matches\" shows only file paths (default), \"count\" shows match counts\n" +
	"  - Use Agent tool for open-ended searches requiring multiple rounds\n" +
	"  - Pattern syntax: Uses ripgrep (not grep) - literal braces need escaping (use `interface\\{\\}` to find `interface{}` in Go code)\n" +
	"  - Multiline matching: By default patterns match within single lines only. For cross-line patterns like `struct \\{[\\s\\S]*?field`, use `multiline: true`\n"

func TestDefinition(t *testing.T) {
	if ToolName != "Grep" || ToolPrompt != wantToolPrompt {
		t.Fatal("Grep identity or prompt differs from TypeScript")
	}
	definition := Definition()
	if definition.Name != ToolName || definition.Description != ToolPrompt {
		t.Fatalf("definition = %#v", definition)
	}
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type        string   `json:"type"`
			Description string   `json:"description"`
			Enum        []string `json:"enum"`
		} `json:"properties"`
		Required             []string `json:"required"`
		AdditionalProperties bool     `json:"additionalProperties"`
	}
	if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || schema.AdditionalProperties || !reflect.DeepEqual(schema.Required, []string{"pattern"}) {
		t.Fatalf("schema shape = %#v", schema)
	}
	if len(schema.Properties) != 14 || !reflect.DeepEqual(schema.Properties["output_mode"].Enum, []string{"content", "files_with_matches", "count"}) {
		t.Fatalf("schema properties = %#v", schema.Properties)
	}
	if schema.Properties["head_limit"].Description == "" || schema.Properties["pattern"].Description != "The regular expression pattern to search for in file contents" {
		t.Fatal("schema descriptions are missing")
	}
}

func TestParseInput(t *testing.T) {
	input, err := ParseInput([]byte(`{
		"pattern":"-needle","path":" src ","glob":"*.go,*.md *.{ts,tsx}",
		"output_mode":"content","-B":"2.0","-A":3,"-C":0,"context":"4",
		"-n":"false","-i":"true","type":"go","head_limit":"0","offset":5.0,"multiline":true
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if input.Pattern != "-needle" || input.Path != " src " || input.OutputMode != OutputModeContent || input.Before == nil || *input.Before != 2 || input.Context == nil || *input.Context != 4 || input.ShowLineNumbers == nil || *input.ShowLineNumbers || input.CaseInsensitive == nil || !*input.CaseInsensitive || input.HeadLimit == nil || *input.HeadLimit != 0 || input.Offset == nil || *input.Offset != 5 || input.Multiline == nil || !*input.Multiline {
		t.Fatalf("input = %#v", input)
	}
	minimal, err := ParseInput([]byte(`{"pattern":""}`))
	if err != nil {
		t.Fatal(err)
	}
	if minimal.normalizedMode() != OutputModeFilesWithMatches || minimal.headLimit() != 250 || !minimal.lineNumbers() || minimal.Offset != nil || minimal.CaseInsensitive != nil || minimal.Multiline != nil {
		t.Fatalf("defaults = %#v", minimal)
	}

	invalid := []string{
		``, `null`, `[]`, `{}`, `{"Pattern":"x"}`, `{"pattern":null}`,
		`{"pattern":"x","extra":true}`, `{"pattern":"x","output_mode":"files"}`,
		`{"pattern":"x","-B":-1}`, `{"pattern":"x","-A":1.5}`,
		`{"pattern":"x","head_limit":"1e2"}`, `{"pattern":"x","offset":-1}`,
		`{"pattern":"x","-n":"TRUE"}`, `{"pattern":"x","-i":1}`,
		`{"pattern":"x","path":null}`, `{"pattern":"x"} {}`,
	}
	for _, value := range invalid {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseInput([]byte(value)); err == nil {
				t.Fatalf("ParseInput(%q) succeeded", value)
			}
		})
	}
}

func TestBuildArguments(t *testing.T) {
	showLines, enabled := false, true
	before, after, alias, contextLines := 1, 2, 3, 4
	input := Input{
		Pattern: "-needle", OutputMode: OutputModeContent, Before: &before, After: &after,
		ContextAlias: &alias, Context: &contextLines, ShowLineNumbers: &showLines,
		CaseInsensitive: &enabled, Type: "go", Glob: "*.go,*.md *.{ts,tsx}", Multiline: &enabled,
	}
	got := buildArguments(input, "/tmp/target")
	want := []string{
		"--hidden", "--glob", "!.git", "--glob", "!.svn", "--glob", "!.hg",
		"--glob", "!.bzr", "--glob", "!.jj", "--glob", "!.sl", "--max-columns", "500",
		"-U", "--multiline-dotall", "-i", "--with-filename", "-n", "-C", "4", "-e", "-needle",
		"--type", "go", "--glob", "*.go", "--glob", "*.md", "--glob", "*.{ts,tsx}", "/tmp/target",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments:\n got %#v\nwant %#v", got, want)
	}
	if got := buildArguments(Input{Pattern: "x"}, "/cwd"); got[len(got)-3] != "-l" || got[len(got)-2] != "x" || got[len(got)-1] != "/cwd" {
		t.Fatalf("default arguments = %#v", got)
	}
	if got := buildArguments(Input{Pattern: "x", OutputMode: OutputModeCount}, "/file"); !containsSequence(got, "-c", "--with-filename") || got[len(got)-1] != "/file" {
		t.Fatalf("count arguments = %#v", got)
	}
	if got := buildArguments(Input{Pattern: "x", OutputMode: OutputModeContent, Before: &before, After: &after}, "/cwd"); !containsSequence(got, "-n", "-B", "1", "-A", "2") {
		t.Fatalf("content arguments = %#v", got)
	}
}

func TestCallResolvesTargetExecutesAndStructuresContent(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "file.go")
	if err := os.WriteFile(file, []byte("needle"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{responses: []runnerResponse{{result: ProcessResult{Stdout: file + ":7:needle\r\n"}}}}
	tool := New(Config{Executable: "custom-rg", Runner: runner, Getwd: func() (string, error) { return directory, nil }})
	output, err := tool.Call(context.Background(), Input{Pattern: "needle", Path: "file.go", OutputMode: OutputModeContent})
	if err != nil {
		t.Fatal(err)
	}
	if output.Content != "file.go:7:needle" || output.NumLines == nil || *output.NumLines != 1 || output.NumFiles != 0 || output.Partial {
		t.Fatalf("output = %#v", output)
	}
	call := runner.callsSnapshot()[0]
	if call.executable != "custom-rg" || call.args[len(call.args)-1] != file || !containsSequence(call.args, "--with-filename", "-n", "needle") {
		t.Fatalf("call = %#v", call)
	}
}

func TestContentRelativizesContextWithoutLineNumbers(t *testing.T) {
	base := t.TempDir()
	prefix := filepath.Join(base, "prefix")
	if err := os.WriteFile(prefix, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	directory := prefix + "-directory"
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "file-with-hyphen.go")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	showLines := false
	output := buildOutput(
		Input{Pattern: "x", OutputMode: OutputModeContent, ShowLineNumbers: &showLines, Context: new(1)},
		file+"-6-context-with-12-number\n"+file+":7:match",
		directory,
		false,
		os.Stat,
	)
	if output.Content != "file-with-hyphen.go-context-with-12-number\nfile-with-hyphen.go:match" {
		t.Fatalf("content = %q", output.Content)
	}
}

func TestFilesSortingPaginationAndCountOutput(t *testing.T) {
	directory := t.TempDir()
	oldFile := filepath.Join(directory, "old.go")
	newB := filepath.Join(directory, "b.go")
	newA := filepath.Join(directory, "a.go")
	for _, path := range []string{oldFile, newB, newA} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Unix(1, 0)
	newTime := time.Unix(2, 0)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{newB, newA} {
		if err := os.Chtimes(path, newTime, newTime); err != nil {
			t.Fatal(err)
		}
	}
	limit := 2
	files := buildOutput(Input{Pattern: "x", HeadLimit: &limit}, strings.Join([]string{oldFile, newB, newA}, "\n"), directory, false, os.Stat)
	if !reflect.DeepEqual(files.Filenames, []string{"a.go", "b.go"}) || files.AppliedLimit == nil || *files.AppliedLimit != 2 {
		t.Fatalf("files = %#v", files)
	}
	count := buildOutput(Input{Pattern: "x", OutputMode: OutputModeCount, Offset: new(1)}, oldFile+":2\n"+newA+":3\n", directory, false, os.Stat)
	if count.Content != "a.go:3" || count.NumMatches == nil || *count.NumMatches != 3 || count.NumFiles != 1 || count.AppliedOffset == nil {
		t.Fatalf("count = %#v", count)
	}
}

func TestPathExpansionAndValidation(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	homeFile := filepath.Join(home, "home.txt")
	if err := os.WriteFile(homeFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{responses: []runnerResponse{{result: ProcessResult{ExitCode: 1}}}}
	tool := New(Config{
		Runner:      runner,
		Getwd:       func() (string, error) { return cwd, nil },
		UserHomeDir: func() (string, error) { return home, nil },
	})
	if _, err := tool.Call(context.Background(), Input{Pattern: "x", Path: "~/home.txt"}); err != nil {
		t.Fatal(err)
	}
	if got := runner.callsSnapshot()[0].args; got[len(got)-1] != homeFile {
		t.Fatalf("home target = %#v", got)
	}
	if _, err := tool.Call(context.Background(), Input{Pattern: "x", Path: "missing"}); err == nil || err.Error() != "Path does not exist: missing. Note: your current working directory is "+cwd+"." {
		t.Fatalf("missing path error = %v", err)
	}
	if _, err := tool.Call(context.Background(), Input{Pattern: "x", Path: "bad\x00path"}); err == nil {
		t.Fatal("NUL path succeeded")
	}
	permissionTool := New(Config{Runner: runner, Getwd: func() (string, error) { return cwd, nil }, Stat: func(string) (fs.FileInfo, error) { return nil, fs.ErrPermission }})
	if _, err := permissionTool.Call(context.Background(), Input{Pattern: "x", Path: "denied"}); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("permission error = %v", err)
	}
}

func TestRetryErrorsAndPartialOutput(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "x.go")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("eagain", func(t *testing.T) {
		runner := &recordingRunner{responses: []runnerResponse{
			{result: ProcessResult{Stderr: "Resource temporarily unavailable", ExitCode: 2}, err: errors.New("failed")},
			{result: ProcessResult{Stdout: file + "\n"}},
		}}
		output, err := New(Config{Runner: runner, Getwd: func() (string, error) { return directory, nil }}).Call(context.Background(), Input{Pattern: "x"})
		if err != nil || output.NumFiles != 1 {
			t.Fatalf("output = %#v, error = %v", output, err)
		}
		calls := runner.callsSnapshot()
		if len(calls) != 2 || !reflect.DeepEqual(calls[1].args[:2], []string{"-j", "1"}) {
			t.Fatalf("calls = %#v", calls)
		}
	})
	t.Run("usage", func(t *testing.T) {
		runner := &recordingRunner{responses: []runnerResponse{{result: ProcessResult{Stderr: "regex parse error", ExitCode: 2}, err: errors.New("exit")}}}
		_, err := New(Config{Runner: runner, Getwd: func() (string, error) { return directory, nil }}).Call(context.Background(), Input{Pattern: "["})
		if !errors.Is(err, ErrUsage) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		runner := &recordingRunner{responses: []runnerResponse{{result: ProcessResult{ExitCode: -1}, err: exec.ErrNotFound}}}
		_, err := New(Config{Runner: runner, Getwd: func() (string, error) { return directory, nil }}).Call(context.Background(), Input{Pattern: "x"})
		if !errors.Is(err, ErrExecutableNotFound) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("overflow partial", func(t *testing.T) {
		runner := &recordingRunner{responses: []runnerResponse{{result: ProcessResult{Stdout: file + ":1:x\nincomplete", ExitCode: -1, Overflow: true}, err: context.Canceled}}}
		output, err := New(Config{Runner: runner, Getwd: func() (string, error) { return directory, nil }}).Call(context.Background(), Input{Pattern: "x", OutputMode: OutputModeContent})
		if err != nil || !output.Partial || output.Content != "x.go:1:x" {
			t.Fatalf("output = %#v, error = %v", output, err)
		}
		block := MapToolResultToToolResultBlockParam(output, "toolu_1")
		if !strings.Contains(block.Content, partialWarning) {
			t.Fatalf("block = %#v", block)
		}
	})
	t.Run("timeout partial", func(t *testing.T) {
		runner := RunnerFunc(func(ctx context.Context, _ string, _ []string, _ int) (ProcessResult, error) {
			<-ctx.Done()
			return ProcessResult{Stdout: file + ":1:x\npartial", ExitCode: -1}, ctx.Err()
		})
		output, err := New(Config{Runner: runner, Timeout: time.Millisecond, Getwd: func() (string, error) { return directory, nil }}).Call(context.Background(), Input{Pattern: "x", OutputMode: OutputModeContent})
		if err != nil || !output.Partial || output.Content != "x.go:1:x" {
			t.Fatalf("output = %#v, error = %v", output, err)
		}
	})
}

func TestModelFormatting(t *testing.T) {
	limit, offset, one := 2, 4, 1
	tests := []struct {
		output Output
		want   string
	}{
		{Output{Mode: OutputModeContent}, "No matches found"},
		{Output{Mode: OutputModeContent, Content: "a:1:x", AppliedLimit: &limit, AppliedOffset: &offset}, "a:1:x\n\n[Showing results with pagination = limit: 2, offset: 4]"},
		{Output{Mode: OutputModeCount, NumFiles: 1, NumMatches: &one, Content: "a:1"}, "a:1\n\nFound 1 total occurrence across 1 file."},
		{Output{Mode: OutputModeFilesWithMatches}, "No files found"},
		{Output{Mode: OutputModeFilesWithMatches, NumFiles: 1, Filenames: []string{"a"}, AppliedOffset: &offset}, "Found 1 file offset: 4\na"},
	}
	for _, test := range tests {
		got := MapToolResultToToolResultBlockParam(test.output, "toolu_1")
		if got.ToolUseID != "toolu_1" || got.Type != "tool_result" || got.Content != test.want {
			t.Fatalf("got %q, want %q", got.Content, test.want)
		}
	}
}

func TestCancellationAndNilTool(t *testing.T) {
	var tool *Tool
	if _, err := tool.Call(context.Background(), Input{Pattern: "x"}); err == nil {
		t.Fatal("nil tool succeeded")
	}
	directory := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := RunnerFunc(func(ctx context.Context, _ string, _ []string, _ int) (ProcessResult, error) {
		return ProcessResult{Stdout: "ignored\n", ExitCode: -1}, ctx.Err()
	})
	_, err := New(Config{Runner: runner, Getwd: func() (string, error) { return directory, nil }}).Call(ctx, Input{Pattern: "x"})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("error = %v", err)
	}
}

func containsSequence(values []string, sequence ...string) bool {
	for index := 0; index+len(sequence) <= len(values); index++ {
		if reflect.DeepEqual(values[index:index+len(sequence)], sequence) {
			return true
		}
	}
	return false
}

type runnerCall struct {
	executable string
	args       []string
	maxBytes   int
}

type runnerResponse struct {
	result ProcessResult
	err    error
}

type recordingRunner struct {
	mu        sync.Mutex
	calls     []runnerCall
	responses []runnerResponse
}

func (runner *recordingRunner) Run(_ context.Context, executable string, args []string, maxBytes int) (ProcessResult, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.calls = append(runner.calls, runnerCall{executable: executable, args: append([]string(nil), args...), maxBytes: maxBytes})
	if len(runner.responses) == 0 {
		return ProcessResult{ExitCode: 1}, nil
	}
	response := runner.responses[0]
	runner.responses = runner.responses[1:]
	return response.result, response.err
}

func (runner *recordingRunner) callsSnapshot() []runnerCall {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]runnerCall(nil), runner.calls...)
}
