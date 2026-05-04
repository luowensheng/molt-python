//go:build !linux

package main

import "os/exec"

// applyIsolation is a no-op on non-Linux platforms. Namespace isolation is
// a Linux-only feature; other platforms would need their own sandboxing.
func applyIsolation(cmd *exec.Cmd) {}
