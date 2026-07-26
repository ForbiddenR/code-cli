//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package bash

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExecRunnerCancellationTerminatesDescendants(t *testing.T) {
	directory := t.TempDir()
	sentinel := filepath.Join(directory, "descendant-survived")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result, err := (execRunner{}).Run(ctx, RunRequest{
		Executable:      os.Args[0],
		Args:            []string{"-test.run=TestBashRunnerHelper", "--", "spawn-child", sentinel},
		Dir:             directory,
		Env:             os.Environ(),
		MaxOutputBytes:  100,
		KillGracePeriod: 20 * time.Millisecond,
		WaitDelay:       20 * time.Millisecond,
	})
	if err == nil || !result.Started || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("result = %#v, error = %v", result, err)
	}
	assertFileAbsentFor(t, sentinel, time.Second)
}
