package bash

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
	"unicode/utf8"
)

const truncationMarker = "\n[output truncated]"

// RunRequest describes one isolated foreground process execution.
type RunRequest struct {
	Executable      string
	Args            []string
	Dir             string
	Env             []string
	MaxOutputBytes  int
	KillGracePeriod time.Duration
	WaitDelay       time.Duration
}

// ProcessResult is the raw result returned by a Runner.
type ProcessResult struct {
	CombinedOutput string
	ExitCode       int
	Started        bool
	Truncated      bool
}

// Runner executes a foreground process.
type Runner interface {
	Run(context.Context, RunRequest) (ProcessResult, error)
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(context.Context, RunRequest) (ProcessResult, error)

func (function RunnerFunc) Run(ctx context.Context, request RunRequest) (ProcessResult, error) {
	return function(ctx, request)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, request RunRequest) (ProcessResult, error) {
	if ctx == nil {
		return ProcessResult{ExitCode: -1}, errors.New("bash runner context is nil")
	}
	writer := newBoundedCombinedWriter(request.MaxOutputBytes)
	executable, err := resolveExecutable(request.Executable, request.Dir, request.Env)
	if err != nil {
		return ProcessResult{ExitCode: -1}, err
	}
	command := exec.Command(executable, request.Args...)
	command.Dir = request.Dir
	command.Env = append([]string(nil), request.Env...)
	command.Stdout = writer
	command.Stderr = writer
	command.WaitDelay = request.WaitDelay
	configureProcessGroup(command)

	result := ProcessResult{ExitCode: -1}
	if err := command.Start(); err != nil {
		result.CombinedOutput, result.Truncated = writer.Result()
		return result, err
	}
	result.Started = true

	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()

	var waitErr error
	select {
	case waitErr = <-waited:
		if errors.Is(waitErr, exec.ErrWaitDelay) {
			if cleanupErr := cleanupDescendants(command.Process, request.KillGracePeriod); cleanupErr != nil {
				waitErr = fmt.Errorf("clean up descendants after pipe wait timeout: %w", cleanupErr)
			}
		}
	case <-ctx.Done():
		waitErr = waitAfterCancellation(command, waited, request)
	}

	if command.ProcessState != nil {
		result.ExitCode = command.ProcessState.ExitCode()
	}
	result.CombinedOutput, result.Truncated = writer.Result()
	return result, waitErr
}

func waitAfterCancellation(command *exec.Cmd, waited <-chan error, request RunRequest) error {
	if err := terminateProcess(command.Process); err != nil {
		_ = killProcess(command.Process)
	}
	grace := request.KillGracePeriod
	if grace <= 0 {
		grace = time.Millisecond
	}
	timer := time.NewTimer(grace)
	select {
	case err := <-waited:
		timer.Stop()
		return err
	case <-timer.C:
		_ = killProcess(command.Process)
	}
	finalWait := request.WaitDelay
	if finalWait <= 0 {
		finalWait = time.Second
	}
	timer = time.NewTimer(finalWait)
	defer timer.Stop()
	select {
	case err := <-waited:
		return err
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

type boundedCombinedWriter struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func newBoundedCombinedWriter(limit int) *boundedCombinedWriter {
	return &boundedCombinedWriter{limit: limit}
}

func (writer *boundedCombinedWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.limit <= 0 {
		writer.truncated = writer.truncated || len(data) > 0
		return len(data), nil
	}
	remaining := writer.limit - writer.buffer.Len()
	if remaining > 0 {
		count := len(data)
		if count > remaining {
			count = remaining
		}
		_, _ = writer.buffer.Write(data[:count])
	}
	if len(data) > remaining {
		writer.truncated = true
	}
	return len(data), nil
}

func (writer *boundedCombinedWriter) Result() (string, bool) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	data := append([]byte(nil), writer.buffer.Bytes()...)
	if !writer.truncated {
		return validUTF8Within(data, writer.limit), false
	}
	return appendMarkerWithin(data, truncationMarker, writer.limit), true
}

func appendMarkerWithin(data []byte, marker string, limit int) string {
	if limit <= 0 {
		return ""
	}
	markerBytes := []byte(marker)
	if len(markerBytes) >= limit {
		return validUTF8Within(markerBytes, limit)
	}
	prefixLimit := limit - len(markerBytes)
	prefix := validUTF8Within(data, prefixLimit)
	return prefix + marker
}

func validUTF8Within(data []byte, limit int) string {
	if limit <= 0 || len(data) == 0 {
		return ""
	}
	if len(data) > limit {
		data = data[:limit]
	}
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return string(data)
}
