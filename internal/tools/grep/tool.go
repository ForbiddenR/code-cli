package grep

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const DefaultTimeout = 20 * time.Second

var (
	ErrExecutableNotFound = errors.New("ripgrep executable not found")
	ErrExecutableDenied   = errors.New("ripgrep executable permission denied")
	ErrCancelled          = errors.New("grep cancelled")
	ErrTimeout            = errors.New("grep timed out")
	ErrOutputLimit        = errors.New("grep output limit exceeded")
	ErrUsage              = errors.New("invalid ripgrep invocation")
	ErrExecution          = errors.New("ripgrep execution failed")
)

// Config supplies local process and filesystem dependencies.
type Config struct {
	Executable     string
	Runner         Runner
	Timeout        time.Duration
	MaxOutputBytes int
	Getwd          func() (string, error)
	UserHomeDir    func() (string, error)
	Stat           func(string) (fs.FileInfo, error)
}

// Tool executes bounded local ripgrep searches.
type Tool struct {
	config Config
}

// New constructs a Grep tool with standard-library defaults.
func New(config Config) *Tool {
	if config.Executable == "" {
		config.Executable = "rg"
	}
	if config.Runner == nil {
		config.Runner = execRunner{}
	}
	if config.Timeout <= 0 {
		config.Timeout = DefaultTimeout
	}
	if config.MaxOutputBytes <= 0 {
		config.MaxOutputBytes = MaxOutputBytes
	}
	if config.Getwd == nil {
		config.Getwd = os.Getwd
	}
	if config.UserHomeDir == nil {
		config.UserHomeDir = os.UserHomeDir
	}
	if config.Stat == nil {
		config.Stat = os.Stat
	}
	return &Tool{config: config}
}

// Call validates the target, executes ripgrep, and structures its output.
func (tool *Tool) Call(ctx context.Context, input Input) (Output, error) {
	if tool == nil {
		return Output{}, errors.New("grep tool is nil")
	}
	if ctx == nil {
		return Output{}, errors.New("grep context is nil")
	}
	if err := ValidateInput(input); err != nil {
		return Output{}, err
	}
	cwd, err := tool.config.Getwd()
	if err != nil {
		return Output{}, fmt.Errorf("get current working directory: %w", err)
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return Output{}, fmt.Errorf("resolve current working directory: %w", err)
	}
	target, err := tool.resolveTarget(input.Path, cwd)
	if err != nil {
		return Output{}, err
	}
	args := buildArguments(input, target)

	runContext, cancel := context.WithTimeout(ctx, tool.config.Timeout)
	defer cancel()
	result, runErr := tool.config.Runner.Run(runContext, tool.config.Executable, args, tool.config.MaxOutputBytes)
	if shouldRetryEAGAIN(result, runContext) {
		retryArgs := make([]string, 0, len(args)+2)
		retryArgs = append(retryArgs, "-j", "1")
		retryArgs = append(retryArgs, args...)
		result, runErr = tool.config.Runner.Run(runContext, tool.config.Executable, retryArgs, tool.config.MaxOutputBytes)
	}

	partial, terminalErr := classifyProcessResult(ctx, runContext, result, runErr)
	if terminalErr != nil && !partial {
		return Output{}, terminalErr
	}
	stdout := result.Stdout
	if partial {
		stdout = completeOutput(stdout)
		if normalizeOutputLines(stdout) == nil {
			return Output{}, terminalErr
		}
	}
	return buildOutput(input, stdout, cwd, partial, tool.config.Stat), nil
}

func (tool *Tool) resolveTarget(value, cwd string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if strings.IndexByte(trimmed, 0) >= 0 {
		return "", errors.New("grep path contains a NUL byte")
	}
	original := trimmed
	if trimmed == "" {
		trimmed = cwd
	} else if trimmed == "~" || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, "~"+string(filepath.Separator)) {
		home, err := tool.config.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if trimmed == "~" {
			trimmed = home
		} else {
			trimmed = filepath.Join(home, trimmed[2:])
		}
	}
	if !filepath.IsAbs(trimmed) {
		trimmed = filepath.Join(cwd, trimmed)
	}
	target := filepath.Clean(trimmed)
	info, err := tool.config.Stat(target)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			display := original
			if display == "" {
				display = target
			}
			return "", fmt.Errorf("Path does not exist: %s. Note: your current working directory is %s.", display, cwd)
		case errors.Is(err, fs.ErrPermission):
			return "", fmt.Errorf("path is not accessible (permission denied): %s", target)
		default:
			return "", fmt.Errorf("stat grep path %s: %w", target, err)
		}
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", fmt.Errorf("grep path is not a regular file or directory: %s", target)
	}
	return target, nil
}

func shouldRetryEAGAIN(result ProcessResult, ctx context.Context) bool {
	if ctx.Err() != nil || result.Overflow {
		return false
	}
	return strings.Contains(result.Stderr, "os error 11") || strings.Contains(result.Stderr, "Resource temporarily unavailable")
}

func classifyProcessResult(parent, runContext context.Context, result ProcessResult, runErr error) (bool, error) {
	if parent.Err() != nil {
		return false, fmt.Errorf("%w: %v", ErrCancelled, parent.Err())
	}
	if runContext.Err() != nil || errors.Is(runErr, context.DeadlineExceeded) {
		return hasCompleteOutput(result.Stdout), ErrTimeout
	}
	if result.Overflow {
		return hasCompleteOutput(result.Stdout), ErrOutputLimit
	}
	if result.ExitCode == 0 || result.ExitCode == 1 {
		if runErr == nil || result.ExitCode == 1 {
			return false, nil
		}
	}
	if errors.Is(runErr, exec.ErrNotFound) || errors.Is(runErr, fs.ErrNotExist) {
		return false, fmt.Errorf("%w: %v", ErrExecutableNotFound, runErr)
	}
	if errors.Is(runErr, fs.ErrPermission) {
		return false, fmt.Errorf("%w: %v", ErrExecutableDenied, runErr)
	}
	message := strings.TrimSpace(result.Stderr)
	if result.ExitCode == 2 {
		if message == "" {
			return false, ErrUsage
		}
		return false, fmt.Errorf("%w: %s", ErrUsage, message)
	}
	if message != "" {
		return false, fmt.Errorf("%w (exit %d): %s", ErrExecution, result.ExitCode, message)
	}
	if runErr != nil {
		return false, fmt.Errorf("%w: %v", ErrExecution, runErr)
	}
	return false, fmt.Errorf("%w with exit code %d", ErrExecution, result.ExitCode)
}

func hasCompleteOutput(stdout string) bool {
	return completeOutput(stdout) != ""
}

func completeOutput(stdout string) string {
	if stdout == "" {
		return ""
	}
	if strings.HasSuffix(stdout, "\n") {
		return stdout
	}
	separator := strings.LastIndexByte(stdout, '\n')
	if separator < 0 {
		return ""
	}
	return stdout[:separator+1]
}
