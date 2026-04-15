package platform_test

import (
	"os"
	"runtime"
	"testing"

	"pyexec/internal/platform"
)

func TestCurrent(t *testing.T) {
	if platform.Current() == "" {
		t.Fatal("Current() returned empty string")
	}
	if platform.Current() != runtime.GOOS {
		t.Errorf("expected %s got %s", runtime.GOOS, platform.Current())
	}
}

func TestPythonBinaryName(t *testing.T) {
	cases := map[string]string{
		"linux":   "python3",
		"darwin":  "python3",
		"windows": "python.exe",
	}
	for goos, want := range cases {
		got := platform.PythonBinaryName(goos)
		if got != want {
			t.Errorf("PythonBinaryName(%s) = %q, want %q", goos, got, want)
		}
	}
}

func TestLibExtension(t *testing.T) {
	cases := map[string]string{
		"linux":   ".so",
		"darwin":  ".dylib",
		"windows": ".dll",
	}
	for goos, want := range cases {
		got := platform.LibExtension(goos)
		if got != want {
			t.Errorf("LibExtension(%s) = %q, want %q", goos, got, want)
		}
	}
}

func TestExeSuffix(t *testing.T) {
	if platform.ExeSuffix("windows") != ".exe" {
		t.Error("ExeSuffix(windows) should be .exe")
	}
	if platform.ExeSuffix("linux") != "" {
		t.Error("ExeSuffix(linux) should be empty")
	}
	if platform.ExeSuffix("darwin") != "" {
		t.Error("ExeSuffix(darwin) should be empty")
	}
}

func TestDefaultInstallBase(t *testing.T) {
	base, err := platform.DefaultInstallBase()
	if err != nil {
		t.Fatalf("DefaultInstallBase: %v", err)
	}
	if base == "" {
		t.Fatal("DefaultInstallBase returned empty string")
	}
	t.Logf("install base: %s", base)
}

func TestDefaultCacheDir(t *testing.T) {
	dir, err := platform.DefaultCacheDir()
	if err != nil {
		t.Fatalf("DefaultCacheDir: %v", err)
	}
	if dir == "" {
		t.Fatal("DefaultCacheDir returned empty string")
	}
	t.Logf("cache dir: %s", dir)
}

func TestPythonStandaloneURL(t *testing.T) {
	cases := []struct {
		goos   string
		goarch string
	}{
		{"linux", "amd64"},
		{"linux", "arm64"},
		{"darwin", "amd64"},
		{"darwin", "arm64"},
		{"windows", "amd64"},
	}
	for _, c := range cases {
		url, err := platform.PythonStandaloneURL("3.12.1", c.goos, c.goarch)
		if err != nil {
			t.Errorf("PythonStandaloneURL(%s/%s): %v", c.goos, c.goarch, err)
			continue
		}
		if url == "" {
			t.Errorf("empty URL for %s/%s", c.goos, c.goarch)
		}
		t.Logf("%s/%s → %s", c.goos, c.goarch, url)
	}
}

func TestPythonStandaloneURL_UnsupportedArch(t *testing.T) {
	_, err := platform.PythonStandaloneURL("3.12.1", "linux", "386")
	if err == nil {
		t.Fatal("expected error for unsupported arch")
	}
}

func TestPythonStandaloneURL_UnsupportedOS(t *testing.T) {
	_, err := platform.PythonStandaloneURL("3.12.1", "plan9", "amd64")
	if err == nil {
		t.Fatal("expected error for unsupported OS")
	}
}

func TestEnvVars(t *testing.T) {
	dir := t.TempDir()
	for _, goos := range []string{"linux", "darwin", "windows"} {
		env := platform.EnvVars(dir, goos)
		if len(env) == 0 {
			t.Errorf("EnvVars(%s) returned empty slice", goos)
		}
		// Every env slice must contain PYTHONNOUSERSITE.
		found := false
		for _, e := range env {
			if e == "PYTHONNOUSERSITE=1" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("EnvVars(%s) missing PYTHONNOUSERSITE=1", goos)
		}
	}
}

func TestIsolationSupported(t *testing.T) {
	supported := platform.IsolationSupported()
	if runtime.GOOS != "linux" && supported {
		t.Error("IsolationSupported should be false on non-Linux")
	}
	t.Logf("isolation supported: %v", supported)
}

func TestTraceDeps(t *testing.T) {
	// Find a binary that exists on this platform to trace.
	var binary string
	candidates := []string{"/usr/bin/python3", "/usr/bin/ls", "/bin/ls", "/usr/bin/env"}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			binary = c
			break
		}
	}
	if binary == "" {
		t.Skip("no suitable binary found to trace")
	}

	deps, err := platform.TraceDeps(binary)
	if err != nil {
		t.Logf("TraceDeps(%s): %v (may be expected on this platform)", binary, err)
		return
	}
	t.Logf("TraceDeps(%s): found %d deps", binary, len(deps))
	for _, d := range deps {
		if d.Name == "" {
			t.Error("dep with empty name")
		}
	}
}
