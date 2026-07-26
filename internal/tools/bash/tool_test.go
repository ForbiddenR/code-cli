package bash

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestDefinition(t *testing.T) {
	if ToolName != "Bash" || !strings.Contains(ToolPrompt, "bash -c") || strings.Contains(ToolPrompt, "background") || strings.Contains(ToolPrompt, "sandbox") {
		t.Fatalf("unexpected Bash identity or prompt: %q", ToolPrompt)
	}
	definition := Definition()
	if definition.Name != ToolName || definition.Description != ToolPrompt {
		t.Fatalf("definition = %#v", definition)
	}
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"properties"`
		Required             []string `json:"required"`
		AdditionalProperties bool     `json:"additionalProperties"`
	}
	if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || schema.AdditionalProperties || !reflect.DeepEqual(schema.Required, []string{"command"}) {
		t.Fatalf("schema shape = %#v", schema)
	}
	if len(schema.Properties) != 3 || schema.Properties["command"].Description != "The command to execute" || !strings.Contains(schema.Properties["description"].Description, "active voice") {
		t.Fatalf("schema properties = %#v", schema.Properties)
	}
	for _, unsupported := range []string{"run_in_background", "dangerouslyDisableSandbox", "_simulatedSedEdit"} {
		if _, ok := schema.Properties[unsupported]; ok {
			t.Fatalf("schema exposes %q", unsupported)
		}
	}
}

func TestParseInput(t *testing.T) {
	input, err := ParseInput([]byte(`{"command":"printf '%s' x","timeout":"12.5","description":"Prints x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if input.Command != "printf '%s' x" || input.TimeoutMS == nil || *input.TimeoutMS != 12.5 || input.Description != "Prints x" {
		t.Fatalf("input = %#v", input)
	}
	for _, value := range []string{`{"command":""}`, `{"command":"   "}`, `{"command":"x","timeout":1}`, `{"command":"x","timeout":0.25}`, `{"command":"x","timeout":1e2}`} {
		if _, err := ParseInput([]byte(value)); err != nil {
			t.Fatalf("ParseInput(%s): %v", value, err)
		}
	}

	invalid := []string{
		``, `null`, `[]`, `{}`, `{"Command":"x"}`, `{"command":null}`,
		`{"command":"x","extra":true}`, `{"command":"x","run_in_background":true}`,
		`{"command":"x","dangerouslyDisableSandbox":true}`, `{"command":"x","_simulatedSedEdit":{}}`,
		`{"command":"x","timeout":null}`, `{"command":"x","timeout":0}`,
		`{"command":"x","timeout":-1}`, `{"command":"x","timeout":600000.1}`,
		`{"command":"x","timeout":"1e2"}`,
		`{"command":"x","timeout":"NaN"}`, `{"command":"x","description":null}`,
		`{"command":"x\u0000y"}`, `{"command":"x"} {}`,
	}
	for _, value := range invalid {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseInput([]byte(value)); err == nil {
				t.Fatalf("ParseInput(%q) succeeded", value)
			}
		})
	}
}

func TestValidateInput(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, -1, MaxTimeoutMS + 1} {
		if err := ValidateInput(Input{Command: "x", TimeoutMS: &value}); err == nil {
			t.Fatalf("timeout %v succeeded", value)
		}
	}
	if err := ValidateInput(Input{Command: "x\x00y"}); err == nil {
		t.Fatal("NUL command succeeded")
	}
}

func TestNewSnapshotsConfiguration(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{WorkingDirectory: file}); err == nil {
		t.Fatal("file working directory succeeded")
	}
	if _, err := New(Config{WorkingDirectory: filepath.Join(directory, "missing")}); err == nil {
		t.Fatal("missing working directory succeeded")
	}
	if _, err := New(Config{WorkingDirectory: directory, DefaultTimeout: 2 * time.Second, MaxTimeout: time.Second}); err == nil {
		t.Fatal("invalid timeout configuration succeeded")
	}
	if _, err := New(Config{WorkingDirectory: directory, MaxTimeout: MaximumTimeout + time.Second}); err == nil {
		t.Fatal("timeout above hard maximum succeeded")
	}
	if _, err := New(Config{WorkingDirectory: directory, MaxOutputBytes: -1}); err == nil {
		t.Fatal("negative configuration succeeded")
	}
	relativeBase := t.TempDir()
	if err := os.Mkdir(filepath.Join(relativeBase, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	relativeTool, err := New(Config{WorkingDirectory: "child", Getwd: func() (string, error) { return relativeBase, nil }})
	if err != nil || relativeTool.cwd != filepath.Join(relativeBase, "child") {
		t.Fatalf("relative cwd = %q, error = %v", relativeTool.cwd, err)
	}

	runner := &recordingRunner{}
	args := []string{"--noprofile", "-c"}
	environment := []string{"A=one"}
	tool, err := New(Config{Executable: "custom-shell", ShellArgs: args, Runner: runner, WorkingDirectory: directory, Environment: environment})
	if err != nil {
		t.Fatal(err)
	}
	args[0] = "changed"
	environment[0] = "A=changed"
	if _, err := tool.Call(context.Background(), Input{Command: "echo '$A' | cat\nprintf x > out"}); err != nil {
		t.Fatal(err)
	}
	call := runner.callsSnapshot()[0]
	if call.request.Executable != "custom-shell" || !reflect.DeepEqual(call.request.Args, []string{"--noprofile", "-c", "echo '$A' | cat\nprintf x > out"}) {
		t.Fatalf("request = %#v", call.request)
	}
	if call.request.Dir != directory || !reflect.DeepEqual(call.request.Env, []string{"A=one"}) || call.request.MaxOutputBytes != DefaultMaxOutputBytes {
		t.Fatalf("request = %#v", call.request)
	}
	call.request.Env[0] = "A=mutated"
	if _, err := tool.Call(context.Background(), Input{Command: "-leading"}); err != nil {
		t.Fatal(err)
	}
	if got := runner.callsSnapshot()[1].request.Env; !reflect.DeepEqual(got, []string{"A=one"}) {
		t.Fatalf("environment was not cloned: %#v", got)
	}
}

func TestTimeoutSelection(t *testing.T) {
	tool, err := New(Config{WorkingDirectory: t.TempDir(), DefaultTimeout: 3 * time.Second, MaxTimeout: 4 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := tool.timeoutFor(Input{}); err != nil || got != 3*time.Second {
		t.Fatalf("default timeout = %s, %v", got, err)
	}
	fraction := 1.5
	if got, err := tool.timeoutFor(Input{TimeoutMS: &fraction}); err != nil || got != 1500*time.Microsecond {
		t.Fatalf("fraction timeout = %s, %v", got, err)
	}
	tiny := 0.0000001
	if got, err := tool.timeoutFor(Input{TimeoutMS: &tiny}); err != nil || got != time.Nanosecond {
		t.Fatalf("tiny timeout = %s, %v", got, err)
	}
	tooLarge := 4001.0
	if _, err := tool.timeoutFor(Input{TimeoutMS: &tooLarge}); err == nil {
		t.Fatal("configured maximum was not enforced")
	}
}

func TestCallClassifiesResults(t *testing.T) {
	tests := []struct {
		name           string
		command        string
		response       runnerResponse
		wantErr        error
		wantExit       int
		wantSemantic   string
		wantModel      string
		wantModelError bool
	}{
		{name: "success", command: "echo ok", response: runnerResponse{result: ProcessResult{CombinedOutput: "\n\nok  \n", ExitCode: 0, Started: true}}, wantExit: 0, wantModel: "ok"},
		{name: "silent", command: ":", response: runnerResponse{result: ProcessResult{ExitCode: 0, Started: true}}, wantExit: 0, wantModel: "(Bash completed with no output)"},
		{name: "ordinary failure", command: "false", response: runnerResponse{result: ProcessResult{CombinedOutput: "bad\n", ExitCode: 7, Started: true}, err: &exec.ExitError{}}, wantErr: ErrExecution, wantExit: 7, wantModel: "bad\n\n[command exited with code 7]", wantModelError: true},
		{name: "grep no matches", command: "printf x; /usr/bin/grep z file", response: runnerResponse{result: ProcessResult{ExitCode: 1, Started: true}, err: &exec.ExitError{}}, wantExit: 1, wantSemantic: "No matches found", wantModel: "No matches found"},
		{name: "rg no matches", command: "rg z", response: runnerResponse{result: ProcessResult{ExitCode: 1, Started: true}}, wantExit: 1, wantSemantic: "No matches found", wantModel: "No matches found"},
		{name: "find inaccessible", command: "find .", response: runnerResponse{result: ProcessResult{ExitCode: 1, Started: true}}, wantExit: 1, wantSemantic: "Some directories were inaccessible", wantModel: "Some directories were inaccessible"},
		{name: "diff differs", command: "diff a b", response: runnerResponse{result: ProcessResult{ExitCode: 1, Started: true}}, wantExit: 1, wantSemantic: "Files differ", wantModel: "Files differ"},
		{name: "test false", command: "test -f x", response: runnerResponse{result: ProcessResult{ExitCode: 1, Started: true}}, wantExit: 1, wantSemantic: "Condition is false", wantModel: "Condition is false"},
		{name: "bracket false", command: "[ -f x ]", response: runnerResponse{result: ProcessResult{ExitCode: 1, Started: true}}, wantExit: 1, wantSemantic: "Condition is false", wantModel: "Condition is false"},
		{name: "grep usage", command: "grep '['", response: runnerResponse{result: ProcessResult{ExitCode: 2, Started: true}, err: &exec.ExitError{}}, wantErr: ErrExecution, wantExit: 2, wantModel: "[command exited with code 2]", wantModelError: true},
		{name: "not found", command: "x", response: runnerResponse{result: ProcessResult{ExitCode: -1}, err: exec.ErrNotFound}, wantErr: ErrExecutableNotFound, wantExit: -1, wantModelError: true},
		{name: "permission", command: "x", response: runnerResponse{result: ProcessResult{ExitCode: -1}, err: fs.ErrPermission}, wantErr: ErrExecutableDenied, wantExit: -1, wantModelError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &recordingRunner{responses: []runnerResponse{test.response}}
			tool, err := New(Config{Runner: runner, WorkingDirectory: t.TempDir(), MaxOutputBytes: 1000})
			if err != nil {
				t.Fatal(err)
			}
			output, callErr := tool.Call(context.Background(), Input{Command: test.command})
			if test.wantErr == nil && callErr != nil || test.wantErr != nil && !errors.Is(callErr, test.wantErr) {
				t.Fatalf("output = %#v, error = %v, want %v", output, callErr, test.wantErr)
			}
			if output.ExitCode != test.wantExit || output.ReturnCodeInterpretation != test.wantSemantic || output.IsError != test.wantModelError {
				t.Fatalf("output = %#v", output)
			}
			block := MapToolResultToToolResultBlockParam(output, "toolu_1")
			if block.ToolUseID != "toolu_1" || block.Type != "tool_result" || block.IsError != test.wantModelError {
				t.Fatalf("block = %#v", block)
			}
			if test.wantModel != "" && block.Content != test.wantModel {
				t.Fatalf("content = %q, want %q", block.Content, test.wantModel)
			}
		})
	}
}

func TestCallTimeoutCancellationAndTruncation(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		runner := RunnerFunc(func(ctx context.Context, _ RunRequest) (ProcessResult, error) {
			<-ctx.Done()
			return ProcessResult{CombinedOutput: "partial\n", ExitCode: -1, Started: true}, ctx.Err()
		})
		tool, err := New(Config{Runner: runner, WorkingDirectory: t.TempDir(), DefaultTimeout: time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		output, err := tool.Call(context.Background(), Input{Command: "sleep"})
		if !errors.Is(err, ErrTimeout) || !output.Interrupted || !output.TimedOut || !output.IsError || output.Stdout != "partial\n" {
			t.Fatalf("output = %#v, error = %v", output, err)
		}
		if got := MapToolResultToToolResultBlockParam(output, "id"); !strings.Contains(got.Content, "timed out") || !got.IsError {
			t.Fatalf("block = %#v", got)
		}
	})
	t.Run("caller cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		runner := RunnerFunc(func(ctx context.Context, _ RunRequest) (ProcessResult, error) {
			return ProcessResult{CombinedOutput: "partial", ExitCode: -1, Started: true}, ctx.Err()
		})
		tool, err := New(Config{Runner: runner, WorkingDirectory: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		output, err := tool.Call(ctx, Input{Command: "sleep"})
		if !errors.Is(err, ErrCancelled) || !output.Interrupted || output.TimedOut || !strings.Contains(MapToolResultToToolResultBlockParam(output, "id").Content, "cancelled") {
			t.Fatalf("output = %#v, error = %v", output, err)
		}
	})
	t.Run("truncated", func(t *testing.T) {
		runner := &recordingRunner{responses: []runnerResponse{{result: ProcessResult{CombinedOutput: "prefix\n[output truncated]", ExitCode: 0, Started: true, Truncated: true}}}}
		tool, err := New(Config{Runner: runner, WorkingDirectory: t.TempDir(), MaxOutputBytes: 30})
		if err != nil {
			t.Fatal(err)
		}
		output, err := tool.Call(context.Background(), Input{Command: "lots"})
		if err != nil || !output.Truncated {
			t.Fatalf("output = %#v, error = %v", output, err)
		}
		block := MapToolResultToToolResultBlockParam(output, "id")
		if len(block.Content) > 30 || strings.Count(block.Content, "output was truncated") != 1 {
			t.Fatalf("block = %#v", block)
		}
	})
	t.Run("tiny model bound", func(t *testing.T) {
		for _, output := range []Output{
			{ExitCode: 0, OutputLimit: 1},
			{ExitCode: 1, ReturnCodeInterpretation: "No matches found", OutputLimit: 1},
			{ExitCode: 7, IsError: true, FailureMessage: "command exited with code 7", OutputLimit: 1},
		} {
			if content := MapToolResultToToolResultBlockParam(output, "id").Content; len(content) > 1 || !utf8.ValidString(content) {
				t.Fatalf("content = %q", content)
			}
		}
	})
	t.Run("mapping survives JSON round trip", func(t *testing.T) {
		output := Output{Stdout: "bad\n", ExitCode: 7, IsError: true, FailureMessage: "command exited with code 7", OutputLimit: 40}
		data, err := json.Marshal(output)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Output
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		before := MapToolResultToToolResultBlockParam(output, "id")
		after := MapToolResultToToolResultBlockParam(decoded, "id")
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("before = %#v, after = %#v", before, after)
		}
	})
	t.Run("literal truncation marker", func(t *testing.T) {
		output := Output{Stdout: "literal\n[output truncated]", ExitCode: 0, OutputLimit: 100}
		if content := MapToolResultToToolResultBlockParam(output, "id").Content; content != output.Stdout {
			t.Fatalf("content = %q", content)
		}
	})
}

func TestSemanticsHeuristic(t *testing.T) {
	for _, test := range []struct {
		command string
		want    string
		ok      bool
	}{
		{"echo x && grep y file", "No matches found", true},
		{"echo 'a;b' ; PATH=x rg y", "No matches found", true},
		{"grep x | diff a b", "Files differ", true},
		{"echo x\nrg y", "No matches found", true},
		{"false $(echo x; grep missing file)", "", false},
		{"false # ; grep missing file", "", false},
		{"echo grep", "", false},
		{"grep 'unterminated", "", false},
		{"grep x", "", false},
	} {
		code := 1
		if test.command == "grep x" {
			code = 2
		}
		got, ok := interpretExitCode(test.command, code)
		if got != test.want || ok != test.ok {
			t.Fatalf("interpretExitCode(%q, %d) = %q, %v", test.command, code, got, ok)
		}
	}
}

func TestNilTool(t *testing.T) {
	var tool *Tool
	if _, err := tool.Call(context.Background(), Input{}); err == nil {
		t.Fatal("nil tool succeeded")
	}
}

type recordedCall struct {
	request RunRequest
}

type runnerResponse struct {
	result ProcessResult
	err    error
}

type recordingRunner struct {
	mu        sync.Mutex
	calls     []recordedCall
	responses []runnerResponse
}

func (runner *recordingRunner) Run(_ context.Context, request RunRequest) (ProcessResult, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	cloned := request
	cloned.Args = append([]string(nil), request.Args...)
	cloned.Env = append([]string(nil), request.Env...)
	runner.calls = append(runner.calls, recordedCall{request: cloned})
	if len(runner.responses) == 0 {
		return ProcessResult{ExitCode: 0, Started: true}, nil
	}
	response := runner.responses[0]
	runner.responses = runner.responses[1:]
	return response.result, response.err
}

func (runner *recordingRunner) callsSnapshot() []recordedCall {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]recordedCall(nil), runner.calls...)
}
