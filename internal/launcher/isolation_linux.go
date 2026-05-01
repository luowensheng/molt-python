//go:build linux

package main

import "os/exec"

// applyIsolation is a no-op on Linux for now. The original codebase had
// namespace-based isolation (CLONE_NEWNS|NEWPID|NEWUTS) guarded by an
// --isolated flag; that flag-plumbing isn't present in this launcher, and
// applying isolation unconditionally would break for non-root users.
// Re-enable when the flag plumbing returns.
func applyIsolation(cmd *exec.Cmd) {}

