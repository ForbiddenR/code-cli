package grep

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync"
)

const MaxOutputBytes = 20_000_000

// ProcessResult is the bounded result of one non-shell ripgrep invocation.
type ProcessResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Overflow bool
}

// Runner executes ripgrep directly without constructing a shell command.
type Runner interface {
	Run(context.Context, string, []string, int) (ProcessResult, error)
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(context.Context, string, []string, int) (ProcessResult, error)

func (run RunnerFunc) Run(ctx context.Context, executable string, args []string, maxOutputBytes int) (ProcessResult, error) {
	return run(ctx, executable, args, maxOutputBytes)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, executable string, args []string, maxOutputBytes int) (ProcessResult, error) {
	processContext, stop := context.WithCancel(ctx)
	defer stop()

	stdout := newLimitedBuffer(maxOutputBytes, stop)
	stderr := newLimitedBuffer(maxOutputBytes, stop)
	command := exec.CommandContext(processContext, executable, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()

	result := ProcessResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: 0,
		Overflow: stdout.Overflow() || stderr.Overflow(),
	}
	if err == nil {
		return result, nil
	}
	result.ExitCode = -1
	if exitError, ok := errors.AsType[*exec.ExitError](err); ok {
		result.ExitCode = exitError.ExitCode()
	}
	return result, err
}

type limitedBuffer struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func newLimitedBuffer(limit int, cancel context.CancelFunc) *limitedBuffer {
	return &limitedBuffer{limit: limit, cancel: cancel}
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if remaining > len(data) {
			remaining = len(data)
		}
		_, _ = buffer.buffer.Write(data[:remaining])
	}
	if remaining < len(data) {
		buffer.overflow = true
		buffer.cancel()
	}
	return len(data), nil
}

func (buffer *limitedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func (buffer *limitedBuffer) Overflow() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.overflow
}
