package uv_test

import (
	"os"
	"path/filepath"
	"testing"

	"pyexec/internal/uv"
)

// skipIfNoUV skips the test when uv is not installed.
func skipIfNoUV(t *testing.T) {
	t.Helper()
	if !uv.Available() {
		t.Skip("uv not installed — skipping")
	}
}

func TestFind(t *testing.T) {
	skipIfNoUV(t)
	p, err := uv.Find()
	if err != nil {
		t.Fatalf("Find: unexpected error: %v", err)
	}
	if p == "" {
		t.Fatal("Find: returned empty path")
	}
}

func TestAvailable(t *testing.T) {
	// Just assert it doesn't panic. The actual value depends on the environment.
	_ = uv.Available()
}

func TestVersion(t *testing.T) {
	skipIfNoUV(t)
	ver, err := uv.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if ver == "" {
		t.Fatal("Version: returned empty string")
	}
	t.Logf("uv version: %s", ver)
}

func TestLockIfStale_MissingLock(t *testing.T) {
	skipIfNoUV(t)
	dir := t.TempDir()

	// Write a minimal pyproject.toml.
	toml := []byte(`
[project]
name = "testpkg"
version = "0.1.0"
requires-python = ">=3.8"
dependencies = []
`)
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), toml, 0o644); err != nil {
		t.Fatal(err)
	}

	ran, err := uv.LockIfStale(dir)
	if err != nil {
		t.Fatalf("LockIfStale: %v", err)
	}
	if !ran {
		t.Error("expected LockIfStale to run uv lock (lock file was absent)")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "uv.lock")); statErr != nil {
		t.Error("uv.lock was not created")
	}
}

func TestLockIfStale_UpToDate(t *testing.T) {
	skipIfNoUV(t)
	dir := t.TempDir()

	toml := []byte(`
[project]
name = "testpkg"
version = "0.1.0"
requires-python = ">=3.8"
dependencies = []
`)
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), toml, 0o644); err != nil {
		t.Fatal(err)
	}
	// Run lock once to create an up-to-date uv.lock.
	if err := uv.Lock(dir); err != nil {
		t.Fatalf("initial lock: %v", err)
	}

	ran, err := uv.LockIfStale(dir)
	if err != nil {
		t.Fatalf("LockIfStale (2nd): %v", err)
	}
	if ran {
		t.Error("expected LockIfStale to skip (lock was already up-to-date)")
	}
}

func TestAddRemove(t *testing.T) {
	skipIfNoUV(t)
	dir := t.TempDir()

	toml := []byte(`
[project]
name = "testpkg"
version = "0.1.0"
requires-python = ">=3.8"
dependencies = []
`)
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), toml, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := uv.Lock(dir); err != nil {
		t.Fatalf("lock: %v", err)
	}

	// Add a package.
	if err := uv.Add(dir, []string{"six"}, false); err != nil {
		t.Fatalf("Add six: %v", err)
	}
	content, _ := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
	if !contains(string(content), "six") {
		t.Error("expected 'six' in pyproject.toml after Add")
	}

	// Remove it.
	if err := uv.Remove(dir, []string{"six"}, false); err != nil {
		t.Fatalf("Remove six: %v", err)
	}
	content, _ = os.ReadFile(filepath.Join(dir, "pyproject.toml"))
	if contains(string(content), `"six"`) {
		t.Error("expected 'six' to be gone from pyproject.toml after Remove")
	}
}

func TestAddEmptyPackages(t *testing.T) {
	err := uv.Add(".", []string{}, false)
	if err == nil {
		t.Fatal("expected error when no packages given to Add")
	}
}

func TestRemoveEmptyPackages(t *testing.T) {
	err := uv.Remove(".", []string{}, false)
	if err == nil {
		t.Fatal("expected error when no packages given to Remove")
	}
}

func TestRaw_NoArgs(t *testing.T) {
	err := uv.Raw(".", []string{})
	if err == nil {
		t.Fatal("expected error when no args given to Raw")
	}
}

func TestRaw_Version(t *testing.T) {
	skipIfNoUV(t)
	// `uv version` via the Raw passthrough.
	err := uv.Raw(".", []string{"version"})
	if err != nil {
		t.Fatalf("Raw version: %v", err)
	}
}

func TestTree(t *testing.T) {
	skipIfNoUV(t)
	dir := t.TempDir()
	toml := []byte(`
[project]
name = "testpkg"
version = "0.1.0"
requires-python = ">=3.8"
dependencies = []
`)
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), toml, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := uv.Lock(dir); err != nil {
		t.Fatalf("lock: %v", err)
	}
	// tree should not error on a valid project.
	if err := uv.Tree(dir); err != nil {
		t.Fatalf("Tree: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
