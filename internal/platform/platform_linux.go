//go:build linux

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
	"regexp"
	"syscall"

	"pyexec/pkg/types"
)

// TraceDeps traces shared library dependencies of the given binary using ldd.
func TraceDeps(binaryPath string) ([]types.SystemDep, error) {
	out, err := exec.Command("ldd", binaryPath).Output()
	if err != nil {
		return nil, fmt.Errorf("ldd %s: %w", binaryPath, err)
	}
	return parseLddOutput(out), nil
}

var lddLine = regexp.MustCompile(`^\s+(\S+)\s+=>\s+(\S+)\s+\(`)

func parseLddOutput(data []byte) []types.SystemDep {
	var deps []types.SystemDep
	seen := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		m := lddLine.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		name, path := m[1], m[2]
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

// NamespaceSysProcAttr returns syscall attributes for Linux namespace isolation.
func NamespaceSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS |
			syscall.CLONE_NEWPID |
			syscall.CLONE_NEWUTS,
	}
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
