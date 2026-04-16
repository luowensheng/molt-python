//go:build !linux

package main

import "os/exec"

func applyIsolation(cmd *exec.Cmd) {}
