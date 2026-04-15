//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

// applyIsolation sets Linux namespace flags on the command.
func applyIsolation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS |
			syscall.CLONE_NEWPID |
			syscall.CLONE_NEWUTS,
	}
}
