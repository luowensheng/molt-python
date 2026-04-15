//go:build !linux

package main

import "os/exec"

// applyIsolation is a no-op on non-Linux platforms.
// Namespace isolation is only available on Linux.
func applyIsolation(cmd *exec.Cmd) {}
