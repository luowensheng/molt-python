//go:build windows

package platform

import (
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"pyexec/pkg/types"
)

// TraceDeps traces DLL dependencies of a Windows PE binary.
func TraceDeps(binaryPath string) ([]types.SystemDep, error) {
	f, err := pe.Open(binaryPath)
	if err != nil {
		return nil, fmt.Errorf("pe.Open %s: %w", binaryPath, err)
	}
	defer f.Close()

	imports, err := f.ImportedLibraries()
	if err != nil {
		return nil, fmt.Errorf("ImportedLibraries: %w", err)
	}

	var deps []types.SystemDep
	seen := map[string]bool{}
	for _, lib := range imports {
		name := strings.ToLower(lib)
		if seen[name] {
			continue
		}
		seen[name] = true
		// Skip Windows system DLLs — they are guaranteed present.
		if isSystemDLL(name) {
			continue
		}
		// Try to locate the DLL on PATH.
		path := findDLL(name)
		sha, size := hashFile(path)
		deps = append(deps, types.SystemDep{
			Name:   name,
			SHA256: sha,
			Size:   size,
		})
	}
	return deps, nil
}

// isSystemDLL returns true for DLLs that are always present on Windows.
func isSystemDLL(name string) bool {
	system := []string{
		"kernel32.dll", "user32.dll", "gdi32.dll", "advapi32.dll",
		"shell32.dll", "ole32.dll", "oleaut32.dll", "ntdll.dll",
		"msvcrt.dll", "ws2_32.dll", "winmm.dll", "version.dll",
		"shlwapi.dll", "comctl32.dll", "comdlg32.dll",
	}
	for _, s := range system {
		if name == s {
			return true
		}
	}
	return false
}

func findDLL(name string) string {
	searchDirs := []string{
		os.Getenv("SystemRoot") + "\\System32",
		os.Getenv("SystemRoot"),
	}
	for _, dir := range searchDirs {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return name
}

// NamespaceSysProcAttr returns nil on Windows — namespaces are not supported.
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
