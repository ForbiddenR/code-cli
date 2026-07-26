package bash

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBoundedCombinedWriter(t *testing.T) {
	writer := newBoundedCombinedWriter(24)
	if count, err := writer.Write([]byte("abcdefghij")); err != nil || count != 10 {
		t.Fatalf("first write = %d, %v", count, err)
	}
	if count, err := writer.Write([]byte("klmnopqrstuvwxyz")); err != nil || count != 16 {
		t.Fatalf("second write = %d, %v", count, err)
	}
	result, truncated := writer.Result()
	if !truncated || len(result) > 24 || !strings.HasSuffix(result, truncationMarker) || !utf8.ValidString(result) {
		t.Fatalf("result = %q, truncated = %v", result, truncated)
	}

	unicodeWriter := newBoundedCombinedWriter(4)
	_, _ = unicodeWriter.Write([]byte("ééé"))
	result, truncated = unicodeWriter.Result()
	if !truncated || len(result) > 4 || !utf8.ValidString(result) {
		t.Fatalf("unicode result = %q, truncated = %v", result, truncated)
	}
}

func TestExecRunner(t *testing.T) {
	runner := execRunner{}
	request := func(mode string) RunRequest {
		return RunRequest{
			Executable:      os.Args[0],
			Args:            []string{"-test.run=TestBashRunnerHelper", "--", mode},
			Dir:             t.TempDir(),
			Env:             os.Environ(),
			MaxOutputBytes:  100,
			KillGracePeriod: 20 * time.Millisecond,
			WaitDelay:       20 * time.Millisecond,
		}
	}
	t.Run("combined output", func(t *testing.T) {
		result, err := runner.Run(context.Background(), request("success"))
		if err != nil || !result.Started || result.ExitCode != 0 || result.Truncated || !strings.Contains(result.CombinedOutput, "stdout\n") || !strings.Contains(result.CombinedOutput, "stderr\n") {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
	})
	t.Run("exit code", func(t *testing.T) {
		result, err := runner.Run(context.Background(), request("exit7"))
		if err == nil || !result.Started || result.ExitCode != 7 {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
	})
	t.Run("truncation drains", func(t *testing.T) {
		runRequest := request("large")
		runRequest.MaxOutputBytes = 30
		result, err := runner.Run(context.Background(), runRequest)
		if err != nil || !result.Truncated || len(result.CombinedOutput) > 30 || !strings.HasSuffix(result.CombinedOutput, truncationMarker) {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		result, err := runner.Run(ctx, request("sleep"))
		if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) || !result.Started || result.ExitCode == 0 {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
	})
	t.Run("wait delay", func(t *testing.T) {
		runRequest := request("parent-exit-child")
		sentinel := t.TempDir() + string(os.PathSeparator) + "sentinel"
		runRequest.Args = append(runRequest.Args, sentinel)
		started := time.Now()
		result, err := runner.Run(context.Background(), runRequest)
		if !errors.Is(err, exec.ErrWaitDelay) || result.ExitCode != 0 || time.Since(started) > time.Second {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
		assertFileAbsentFor(t, sentinel, time.Second)
	})
	t.Run("startup failure", func(t *testing.T) {
		runRequest := request("success")
		runRequest.Executable = filepathMissingExecutable(t)
		result, err := runner.Run(context.Background(), runRequest)
		if err == nil || result.Started || result.ExitCode != -1 {
			t.Fatalf("result = %#v, error = %v", result, err)
		}
	})
}

func TestResolveExecutableUsesSnapshottedPath(t *testing.T) {
	directory := t.TempDir()
	name := "bash-helper"
	path := directory + string(os.PathSeparator) + name
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, path); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveExecutable(name, t.TempDir(), []string{"PATH=" + directory})
	if err != nil || resolved != path {
		t.Fatalf("resolved = %q, error = %v", resolved, err)
	}
	if _, err := resolveExecutable(name, t.TempDir(), []string{"PATH=" + t.TempDir()}); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing executable error = %v", err)
	}
	denied := filepathMissingExecutable(t)
	if err := os.WriteFile(denied, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveExecutable(denied, t.TempDir(), nil); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("permission error = %v", err)
	}
}

func TestBashRunnerHelper(t *testing.T) {
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
	case "exit7":
		os.Exit(7)
	case "large":
		fmt.Fprint(os.Stdout, strings.Repeat("x", 10000))
	case "sleep":
		fmt.Fprintln(os.Stdout, "started")
		time.Sleep(time.Second)
	case "child-sleep":
		if separator+2 >= len(os.Args) {
			os.Exit(4)
		}
		time.Sleep(100 * time.Millisecond)
		if err := os.WriteFile(os.Args[separator+2], []byte("survived"), 0o600); err != nil {
			os.Exit(5)
		}
	case "spawn-child", "parent-exit-child":
		if separator+2 >= len(os.Args) {
			os.Exit(4)
		}
		command := exec.Command(os.Args[0], "-test.run=TestBashRunnerHelper", "--", "child-sleep", os.Args[separator+2])
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			os.Exit(3)
		}
		fmt.Fprintln(os.Stdout, command.Process.Pid)
		if os.Args[separator+1] == "spawn-child" {
			time.Sleep(10 * time.Second)
		}
	default:
		if _, err := strconv.Atoi(os.Args[separator+1]); err == nil {
			os.Exit(2)
		}
		os.Exit(2)
	}
	os.Exit(0)
}

func filepathMissingExecutable(t *testing.T) string {
	t.Helper()
	return t.TempDir() + string(os.PathSeparator) + "missing"
}

func assertFileAbsentFor(t *testing.T, path string, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		_, err := os.Stat(path)
		if err == nil {
			t.Fatalf("descendant remained alive and wrote %s", path)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat descendant sentinel: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
