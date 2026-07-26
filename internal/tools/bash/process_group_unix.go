//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package bash

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return syscall.Kill(-process.Pid, syscall.SIGTERM)
}

func killProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return syscall.Kill(-process.Pid, syscall.SIGKILL)
}

func cleanupDescendants(process *os.Process, _ time.Duration) error {
	if process == nil {
		return nil
	}
	err := killProcess(process)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
