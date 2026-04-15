//go:build darwin

package platform

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"pyexec/pkg/types"
)

// TraceDeps traces shared library dependencies using otool -L on macOS.
func TraceDeps(binaryPath string) ([]types.SystemDep, error) {
	out, err := exec.Command("otool", "-L", binaryPath).Output()
	if err != nil {
		return nil, fmt.Errorf("otool -L %s: %w", binaryPath, err)
	}
	return parseOtoolOutput(out), nil
}

// parseOtoolOutput parses lines like:
//   /usr/lib/libSystem.B.dylib (compatibility version 1.0.0, ...)
func parseOtoolOutput(data []byte) []types.SystemDep {
	var deps []types.SystemDep
	seen := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	first := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if first { // first line is the binary itself
			first = false
			continue
		}
		// Extract the path before the first space.
		parts := strings.SplitN(line, " ", 2)
		if len(parts) == 0 || parts[0] == "" {
			continue
		}
		path := parts[0]
		name := lastPathComponent(path)
		if seen[name] {
			continue
		}
		seen[name] = true
		sha, size := hashFile(path)
		deps = append(deps, types.SystemDep{
			Name:   name,
			SHA256: sha,
			Size:   size,
		})
	}
	return deps
}

func lastPathComponent(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// NamespaceSysProcAttr returns nil on macOS — namespaces are not supported.
func NamespaceSysProcAttr() *syscall.SysProcAttr {
	return nil
}

func hashFile(path string) (string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0
	}
	return hex.EncodeToString(h.Sum(nil)), size
}
