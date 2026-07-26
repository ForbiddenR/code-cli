package bash

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	DefaultTimeout        = 120 * time.Second
	MaximumTimeout        = 600 * time.Second
	DefaultMaxOutputBytes = 30000
	DefaultKillGrace      = 250 * time.Millisecond
	DefaultWaitDelay      = 250 * time.Millisecond
)

var (
	ErrExecutableNotFound = errors.New("bash executable not found")
	ErrExecutableDenied   = errors.New("bash executable permission denied")
	ErrCancelled          = errors.New("bash command cancelled")
	ErrTimeout            = errors.New("bash command timed out")
	ErrExecution          = errors.New("bash command execution failed")
	ErrInternal           = errors.New("bash internal execution failure")
)

// Config supplies local process and filesystem dependencies.
type Config struct {
	Executable       string
	ShellArgs        []string
	Runner           Runner
	WorkingDirectory string
	Environment      []string
	Getwd            func() (string, error)
	Stat             func(string) (fs.FileInfo, error)
	Now              func() time.Time
	DefaultTimeout   time.Duration
	MaxTimeout       time.Duration
	MaxOutputBytes   int
	KillGracePeriod  time.Duration
	WaitDelay        time.Duration
}

// Tool executes authorized foreground shell commands.
type Tool struct {
	executable      string
	shellArgs       []string
	runner          Runner
	cwd             string
	environment     []string
	now             func() time.Time
	defaultTimeout  time.Duration
	maxTimeout      time.Duration
	maxOutputBytes  int
	killGracePeriod time.Duration
	waitDelay       time.Duration
}

// New constructs a Bash tool and snapshots its cwd and environment.
func New(config Config) (*Tool, error) {
	if config.Executable == "" {
		config.Executable = "bash"
	}
	if config.ShellArgs == nil {
		config.ShellArgs = []string{"-c"}
	}
	if config.Runner == nil {
		config.Runner = execRunner{}
	}
	if config.Getwd == nil {
		config.Getwd = os.Getwd
	}
	if config.Stat == nil {
		config.Stat = os.Stat
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.DefaultTimeout < 0 || config.MaxTimeout < 0 || config.MaxOutputBytes < 0 || config.KillGracePeriod < 0 || config.WaitDelay < 0 {
		return nil, errors.New("bash configuration values must be nonnegative")
	}
	if config.DefaultTimeout == 0 {
		config.DefaultTimeout = DefaultTimeout
	}
	if config.MaxTimeout == 0 {
		config.MaxTimeout = MaximumTimeout
	}
	if config.MaxTimeout > MaximumTimeout {
		return nil, fmt.Errorf("maximum timeout cannot exceed %s", MaximumTimeout)
	}
	if config.DefaultTimeout > config.MaxTimeout {
		return nil, errors.New("default timeout exceeds maximum timeout")
	}
	if config.MaxOutputBytes == 0 {
		config.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if config.KillGracePeriod == 0 {
		config.KillGracePeriod = DefaultKillGrace
	}
	if config.WaitDelay == 0 {
		config.WaitDelay = DefaultWaitDelay
	}

	cwd := config.WorkingDirectory
	var err error
	if cwd == "" || !filepath.IsAbs(cwd) {
		base, getwdErr := config.Getwd()
		if getwdErr != nil {
			return nil, fmt.Errorf("get current working directory: %w", getwdErr)
		}
		if cwd == "" {
			cwd = base
		} else {
			cwd = filepath.Join(base, cwd)
		}
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	info, err := config.Stat(cwd)
	if err != nil {
		return nil, fmt.Errorf("stat working directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("working directory is not a directory: %s", cwd)
	}

	environment := config.Environment
	if environment == nil {
		environment = os.Environ()
	}
	return &Tool{
		executable:      config.Executable,
		shellArgs:       append([]string(nil), config.ShellArgs...),
		runner:          config.Runner,
		cwd:             cwd,
		environment:     append([]string(nil), environment...),
		now:             config.Now,
		defaultTimeout:  config.DefaultTimeout,
		maxTimeout:      config.MaxTimeout,
		maxOutputBytes:  config.MaxOutputBytes,
		killGracePeriod: config.KillGracePeriod,
		waitDelay:       config.WaitDelay,
	}, nil
}

// Call validates and executes one foreground shell command. Calling this method
// executes arbitrary code with the privileges of the hosting process.
func (tool *Tool) Call(ctx context.Context, input Input) (Output, error) {
	if tool == nil {
		return Output{}, errors.New("bash tool is nil")
	}
	if ctx == nil {
		return Output{}, errors.New("bash context is nil")
	}
	if err := ValidateInput(input); err != nil {
		return Output{}, err
	}
	timeout, err := tool.timeoutFor(input)
	if err != nil {
		return Output{}, err
	}

	startedAt := tool.now()
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := make([]string, 0, len(tool.shellArgs)+1)
	args = append(args, tool.shellArgs...)
	args = append(args, input.Command)
	result, runErr := tool.runner.Run(runContext, RunRequest{
		Executable:      tool.executable,
		Args:            args,
		Dir:             tool.cwd,
		Env:             append([]string(nil), tool.environment...),
		MaxOutputBytes:  tool.maxOutputBytes,
		KillGracePeriod: tool.killGracePeriod,
		WaitDelay:       tool.waitDelay,
	})
	elapsed := max(tool.now().Sub(startedAt), 0)
	exitCode := result.ExitCode
	if !result.Started {
		exitCode = -1
	}
	output := Output{
		Stdout:      result.CombinedOutput,
		Stderr:      "",
		ExitCode:    exitCode,
		Truncated:   result.Truncated,
		DurationMS:  elapsed.Milliseconds(),
		OutputLimit: tool.maxOutputBytes,
	}

	if ctx.Err() != nil {
		output.Interrupted = true
		output.IsError = true
		output.FailureMessage = "command cancelled"
		return output, &ExecutionError{ExitCode: output.ExitCode, Err: errors.Join(ErrCancelled, ctx.Err())}
	}
	if runContext.Err() != nil {
		output.Interrupted = true
		output.TimedOut = true
		output.IsError = true
		output.FailureMessage = fmt.Sprintf("command timed out after %s", timeout)
		return output, &ExecutionError{ExitCode: output.ExitCode, Err: errors.Join(ErrTimeout, runContext.Err())}
	}
	if !result.Started {
		output.IsError = true
		classified := classifyStartError(runErr)
		output.FailureMessage = classified.Error()
		return output, &ExecutionError{ExitCode: -1, Err: classified}
	}
	if result.ExitCode == 0 && (runErr == nil || errors.Is(runErr, exec.ErrWaitDelay)) {
		return output, nil
	}
	if result.ExitCode == 0 {
		output.IsError = true
		output.FailureMessage = "internal shell execution failure"
		return output, &ExecutionError{ExitCode: 0, Err: errors.Join(ErrInternal, runErr)}
	}
	if interpretation, ok := interpretExitCode(input.Command, result.ExitCode); ok {
		output.ReturnCodeInterpretation = interpretation
		return output, nil
	}

	output.IsError = true
	if result.ExitCode >= 0 {
		output.FailureMessage = fmt.Sprintf("command exited with code %d", result.ExitCode)
		err := &ExecutionError{ExitCode: output.ExitCode, Err: ErrExecution}
		if runErr != nil {
			err.Err = errors.Join(ErrExecution, runErr)
		}
		return output, err
	}
	output.FailureMessage = "internal shell execution failure"
	if runErr == nil {
		runErr = ErrInternal
	}
	return output, &ExecutionError{ExitCode: output.ExitCode, Err: errors.Join(ErrInternal, runErr)}
}

func (tool *Tool) timeoutFor(input Input) (time.Duration, error) {
	if input.TimeoutMS == nil {
		return tool.defaultTimeout, nil
	}
	milliseconds := *input.TimeoutMS
	if math.IsNaN(milliseconds) || math.IsInf(milliseconds, 0) || milliseconds <= 0 {
		return 0, errors.New("timeout must be greater than 0")
	}
	timeout := time.Duration(milliseconds * float64(time.Millisecond))
	if timeout <= 0 {
		timeout = time.Nanosecond
	}
	if timeout > tool.maxTimeout {
		return 0, fmt.Errorf("timeout must be at most %d milliseconds", tool.maxTimeout.Milliseconds())
	}
	return timeout, nil
}

func classifyStartError(err error) error {
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return errors.Join(ErrExecutableNotFound, err)
	case errors.Is(err, fs.ErrPermission):
		return errors.Join(ErrExecutableDenied, err)
	case err != nil:
		return errors.Join(ErrInternal, err)
	default:
		return ErrInternal
	}
}
