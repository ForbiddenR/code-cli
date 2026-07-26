package grep

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLimitedBufferBoundsAndCancels(t *testing.T) {
	cancelled := false
	buffer := newLimitedBuffer(5, func() { cancelled = true })
	if count, err := buffer.Write([]byte("abc")); err != nil || count != 3 {
		t.Fatalf("first write = %d, %v", count, err)
	}
	if count, err := buffer.Write([]byte("defg")); err != nil || count != 4 {
		t.Fatalf("second write = %d, %v", count, err)
	}
	if buffer.String() != "abcde" || !buffer.Overflow() || !cancelled {
		t.Fatalf("buffer = %q, overflow = %v, cancelled = %v", buffer.String(), buffer.Overflow(), cancelled)
	}
}

func TestCompleteOutput(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{"", ""},
		{"partial", ""},
		{"complete\npartial", "complete\n"},
		{"complete\n", "complete\n"},
	} {
		if got := completeOutput(test.input); got != test.want {
			t.Fatalf("completeOutput(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestExecRunner(t *testing.T) {
	runner := execRunner{}
	t.Run("capture success", func(t *testing.T) {
		result, err := runner.Run(context.Background(), os.Args[0], []string{"-test.run=TestExecRunnerHelper", "--", "success"}, 100)
		if err != nil || result.ExitCode != 0 || result.Stdout != "stdout\n" || result.Stderr != "stderr\n" || result.Overflow {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
	})
	t.Run("exit code", func(t *testing.T) {
		result, err := runner.Run(context.Background(), os.Args[0], []string{"-test.run=TestExecRunnerHelper", "--", "exit1"}, 100)
		if err == nil || result.ExitCode != 1 {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
	})
	t.Run("overflow", func(t *testing.T) {
		result, err := runner.Run(context.Background(), os.Args[0], []string{"-test.run=TestExecRunnerHelper", "--", "overflow"}, 5)
		if !result.Overflow || len(result.Stdout) > 5 {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		result, err := runner.Run(ctx, os.Args[0], []string{"-test.run=TestExecRunnerHelper", "--", "sleep"}, 100)
		if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) || result.ExitCode == 0 {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
	})
}

func TestExecRunnerHelper(t *testing.T) {
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	switch os.Args[separator+1] {
	case "success":
		fmt.Fprintln(os.Stdout, "stdout")
		fmt.Fprintln(os.Stderr, "stderr")
	case "exit1":
		os.Exit(1)
	case "overflow":
		for {
			fmt.Fprint(os.Stdout, strings.Repeat("x", 10_000))
		}
	case "sleep":
		time.Sleep(time.Second)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func TestClassifyProcessResult(t *testing.T) {
	background := context.Background()
	for _, test := range []struct {
		name    string
		result  ProcessResult
		err     error
		wantErr error
	}{
		{name: "success", result: ProcessResult{ExitCode: 0}},
		{name: "no matches", result: ProcessResult{ExitCode: 1}, err: context.Canceled},
		{name: "overflow", result: ProcessResult{ExitCode: -1, Overflow: true}, err: context.Canceled, wantErr: ErrOutputLimit},
		{name: "usage", result: ProcessResult{ExitCode: 2, Stderr: "bad regex"}, err: context.Canceled, wantErr: ErrUsage},
		{name: "execution", result: ProcessResult{ExitCode: 3, Stderr: "failed"}, err: context.Canceled, wantErr: ErrExecution},
	} {
		t.Run(test.name, func(t *testing.T) {
			partial, err := classifyProcessResult(background, background, test.result, test.err)
			if partial != (test.result.Overflow && strings.Contains(test.result.Stdout, "\n")) {
				t.Fatalf("partial = %v", partial)
			}
			if test.wantErr == nil && err != nil || test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}
}
