package native

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// RustMode describes how a .rs file exposes itself to Python.
type RustMode int

const (
	RustModeUnknown RustMode = iota
	RustModePyO3             // uses pyo3 → produces a real PyInit_<name> extension
)

// DetectRustMode reads a .rs file and returns RustModePyO3 when it contains
// pyo3 usage, RustModeUnknown otherwise.
func DetectRustMode(path string) (RustMode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RustModeUnknown, err
	}
	s := string(data)
	if strings.Contains(s, "use pyo3") ||
		strings.Contains(s, "#[pymodule]") ||
		strings.Contains(s, "pyo3::") {
		return RustModePyO3, nil
	}
	return RustModeUnknown, nil
}

// CargoAvailable reports whether cargo is on PATH.
func CargoAvailable() bool {
	_, err := exec.LookPath("cargo")
	return err == nil
}

// hashRustFile produces a cache key for a single .rs source file.
// pyo3 version + features participate so bumping pyproject.toml's
// [tool.molt.rust] invalidates stale cached binaries.
func hashRustFile(content []byte, abiTag, plat string, rust RustConfig) string {
	h := sha256.New()
	h.Write(content)
	h.Write([]byte{0})
	h.Write([]byte(abiTag))
	h.Write([]byte{0})
	h.Write([]byte(plat))
	h.Write([]byte{0})
	h.Write([]byte(rust.Pyo3Version))
	for _, f := range rust.Pyo3Features {
		h.Write([]byte{0})
		h.Write([]byte(f))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// hashRustProject hashes all .rs + Cargo.toml + Cargo.lock files in a project.
func hashRustProject(srcDir, abiTag, plat string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(srcDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "target" || strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		ext := filepath.Ext(name)
		if ext != ".rs" && name != "Cargo.toml" && name != "Cargo.lock" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcDir, p)
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	h.Write([]byte(abiTag))
	h.Write([]byte{0})
	h.Write([]byte(plat))
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// rustBuildRoot returns the directory where molt stores generated Cargo.toml
// files for single-.rs builds: ~/.molt/native/rust-build/<module>-<hash>/.
func rustBuildRoot(module, hash string) (string, error) {
	root, err := CacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "rust-build", module+"-"+hash), nil
}

// cargoLibName returns the library file cargo produces (before renaming).
func cargoLibName(crateName string) string {
	switch runtime.GOOS {
	case "windows":
		return crateName + ".dll"
	case "darwin":
		return "lib" + crateName + ".dylib"
	default:
		return "lib" + crateName + ".so"
	}
}

// findCargoOutput searches target/release/ for the compiled shared library.
func findCargoOutput(buildDir, crateName string) (string, error) {
	releaseDir := filepath.Join(buildDir, "target", "release")
	// Try the canonical name first.
	if p := filepath.Join(releaseDir, cargoLibName(crateName)); fileExists(p) {
		return p, nil
	}
	// Fallback: glob for any lib<name>.{dylib,so,dll}.
	for _, ext := range []string{".dylib", ".so", ".dll"} {
		if p := filepath.Join(releaseDir, "lib"+crateName+ext); fileExists(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("cargo output not found in %s for crate %q (tried lib%s.{dylib,so,dll})",
		releaseDir, crateName, crateName)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// generateCargo writes a minimal Cargo.toml for a single .rs PyO3 file.
// The [lib] path is absolute so cargo can compile the file from the build dir.
// pyo3 version and features come from [tool.molt.rust] in pyproject.toml.
func generateCargo(buildDir, rsPath, module string, rust RustConfig) error {
	feats := make([]string, len(rust.Pyo3Features))
	for i, f := range rust.Pyo3Features {
		feats[i] = fmt.Sprintf("%q", f)
	}
	content := fmt.Sprintf(`[package]
name = %q
version = "0.1.0"
edition = "2021"

[lib]
name = %q
crate-type = ["cdylib"]
path = %q

[dependencies]
pyo3 = { version = %q, features = [%s] }
`, module, module, rsPath, rust.Pyo3Version, strings.Join(feats, ", "))
	return os.WriteFile(filepath.Join(buildDir, "Cargo.toml"), []byte(content), 0o644)
}

// BuildRustFile compiles a single .rs file (PyO3 mode) via cargo.
// Generates a Cargo.toml in ~/.molt/native/rust-build/ and runs cargo there
// so the user's project directory is never modified.
func BuildRustFile(s Source, abiTag, plat, extSuffix string, verbose bool, rust RustConfig) (Artifact, error) {
	content, err := os.ReadFile(s.Path)
	if err != nil {
		return Artifact{}, fmt.Errorf("read %s: %w", s.Path, err)
	}
	hash := hashRustFile(content, abiTag, plat, rust)
	soName := s.Basename + extSuffix

	if hit, cachedPath, err := hasCacheEntry(hash, soName); err != nil {
		return Artifact{}, err
	} else if hit {
		if verbose {
			fmt.Printf("  ✓ rust    %s  (cached)\n", s.Module)
		}
		return Artifact{
			Source: s, Hash: hash, Path: cachedPath, SoName: soName,
			AbiTag: abiTag, Plat: plat,
		}, nil
	}

	if verbose {
		fmt.Printf("  ↻ rust    %s\n", s.Module)
	}

	buildDir, err := rustBuildRoot(s.Module, hash)
	if err != nil {
		return Artifact{}, err
	}
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return Artifact{}, err
	}
	if err := generateCargo(buildDir, s.Path, s.Module, rust); err != nil {
		return Artifact{}, fmt.Errorf("generate Cargo.toml for %s: %w", s.Path, err)
	}

	cmd := exec.Command("cargo", "build", "--release")
	cmd.Dir = buildDir
	// Allow building against Python versions newer than pyo3 officially
	// supports — pyo3's build script bails otherwise. Using the stable
	// ABI is the upstream-recommended workaround and only kicks in when
	// the runtime Python is actually newer than pyo3's max.
	cmd.Env = append(os.Environ(), "PYO3_USE_ABI3_FORWARD_COMPATIBILITY=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return Artifact{}, fmt.Errorf("cargo build %s:\n%s", s.Path, string(out))
	}

	cargoOut, err := findCargoOutput(buildDir, s.Module)
	if err != nil {
		return Artifact{}, err
	}

	cdir, err := cacheDir(hash)
	if err != nil {
		return Artifact{}, err
	}
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		return Artifact{}, err
	}
	dest := filepath.Join(cdir, soName)
	if err := copyFile(cargoOut, dest); err != nil {
		return Artifact{}, fmt.Errorf("cache rust artifact %s: %w", s.Module, err)
	}
	if err := writeCacheMeta(hash, CacheEntry{
		Lang: "rust", SoName: soName, Source: s.Path,
		AbiTag: abiTag, Platform: plat, CompiledAt: time.Now().UTC(),
	}); err != nil {
		return Artifact{}, err
	}
	return Artifact{
		Source: s, Hash: hash, Path: dest, SoName: soName,
		AbiTag: abiTag, Plat: plat,
	}, nil
}

// BuildRustProject compiles a Rust cargo project (with its own Cargo.toml).
// Runs `cargo build --release` inside srcDir and copies the output .so
// into the global native cache renamed to <module><ext_suffix>.
func BuildRustProject(module, srcDir, abiTag, plat, extSuffix string, verbose bool) (Artifact, error) {
	hash, err := hashRustProject(srcDir, abiTag, plat)
	if err != nil {
		return Artifact{}, fmt.Errorf("hash rust project %s: %w", srcDir, err)
	}
	soName := module + extSuffix
	src := Source{Lang: "rust", Path: srcDir, Module: module, Basename: module}

	if hit, cachedPath, err := hasCacheEntry(hash, soName); err != nil {
		return Artifact{}, err
	} else if hit {
		if verbose {
			fmt.Printf("  ✓ rust    %s  (cached)\n", module)
		}
		return Artifact{
			Source: src, Hash: hash, Path: cachedPath, SoName: soName,
			AbiTag: abiTag, Plat: plat,
		}, nil
	}

	if verbose {
		fmt.Printf("  ↻ rust    %s\n", module)
	}

	crateName, err := readCrateName(srcDir)
	if err != nil {
		crateName = module
	}

	cmd := exec.Command("cargo", "build", "--release")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return Artifact{}, fmt.Errorf("cargo build %s:\n%s", srcDir, string(out))
	}

	cargoOut, err := findCargoOutput(srcDir, crateName)
	if err != nil {
		return Artifact{}, err
	}

	cdir, err := cacheDir(hash)
	if err != nil {
		return Artifact{}, err
	}
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		return Artifact{}, err
	}
	dest := filepath.Join(cdir, soName)
	if err := copyFile(cargoOut, dest); err != nil {
		return Artifact{}, fmt.Errorf("cache rust project %s: %w", module, err)
	}
	if err := writeCacheMeta(hash, CacheEntry{
		Lang: "rust", SoName: soName, Source: srcDir,
		AbiTag: abiTag, Platform: plat, CompiledAt: time.Now().UTC(),
	}); err != nil {
		return Artifact{}, err
	}
	return Artifact{
		Source: src, Hash: hash, Path: dest, SoName: soName,
		AbiTag: abiTag, Plat: plat,
	}, nil
}

// readCrateName reads [package] name from Cargo.toml.
func readCrateName(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
	if err != nil {
		return "", err
	}
	inPkg := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "[package]" {
			inPkg = true
			continue
		}
		if strings.HasPrefix(line, "[") {
			inPkg = false
		}
		if inPkg && strings.HasPrefix(line, "name") {
			idx := strings.Index(line, "=")
			if idx < 0 {
				continue
			}
			return strings.Trim(strings.TrimSpace(line[idx+1:]), `"'`), nil
		}
	}
	return "", fmt.Errorf("name not found in Cargo.toml")
}
