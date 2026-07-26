//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package bash

import (
	"os"
	"os/exec"
	"time"
)

func configureProcessGroup(*exec.Cmd) {}

func terminateProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return process.Kill()
}

func killProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return process.Kill()
}

func cleanupDescendants(*os.Process, time.Duration) error { return nil }
