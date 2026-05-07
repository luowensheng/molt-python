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

// rustBuildRecipeVersion is mixed into the cache hash so changes to how
// we drive cargo (env vars, generated build.rs, etc.) automatically
// invalidate cached binaries from older molt versions even when source,
// pyo3 version, and ABI tag are unchanged. Bump on any build-recipe edit.
const rustBuildRecipeVersion = "v3"

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
	h.Write([]byte{0})
	h.Write([]byte(rustBuildRecipeVersion))
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

// generateCargo writes a minimal Cargo.toml + build.rs for a single .rs
// PyO3 file. The [lib] path points at libPath, which is either the user's
// source directly OR an auto-generated wrapper that injects the pyo3
// prelude (see maybeInjectPrelude).
//
// The build.rs calls pyo3_build_config::add_extension_module_link_args(),
// which is required on macOS (-undefined dynamic_lookup) and silently does
// the right thing on Linux / Windows. Without it, linking a cdylib that
// uses the `extension-module` feature fails on macOS with hundreds of
// undefined `_Py*` symbols.
func generateCargo(buildDir, rsPath, module string, rust RustConfig) error {
	feats := make([]string, len(rust.Pyo3Features))
	for i, f := range rust.Pyo3Features {
		feats[i] = fmt.Sprintf("%q", f)
	}
	cargo := fmt.Sprintf(`[package]
name = %q
version = "0.1.0"
edition = "2021"

[lib]
name = %q
crate-type = ["cdylib"]
path = %q

[dependencies]
pyo3 = { version = %q, features = [%s] }

[build-dependencies]
pyo3-build-config = %q
`, module, module, rsPath, rust.Pyo3Version, strings.Join(feats, ", "), rust.Pyo3Version)
	if err := os.WriteFile(filepath.Join(buildDir, "Cargo.toml"), []byte(cargo), 0o644); err != nil {
		return err
	}
	const buildRs = `fn main() {
    pyo3_build_config::add_extension_module_link_args();
}
`
	return os.WriteFile(filepath.Join(buildDir, "build.rs"), []byte(buildRs), 0o644)
}

// BuildRustFile compiles a single .rs file (PyO3 mode) via cargo.
// Generates a Cargo.toml in ~/.molt/native/rust-build/ and runs cargo there
// so the user's project directory is never modified.
func BuildRustFile(s Source, pyExe, abiTag, plat, extSuffix string, verbose bool, rust RustConfig) (Artifact, error) {
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
	libPath, err := maybeInjectPrelude(buildDir, s.Path, content, rust)
	if err != nil {
		return Artifact{}, fmt.Errorf("inject pyo3 prelude for %s: %w", s.Path, err)
	}
	if err := generateCargo(buildDir, libPath, s.Module, rust); err != nil {
		return Artifact{}, fmt.Errorf("generate Cargo.toml for %s: %w", s.Path, err)
	}

	cmd := exec.Command("cargo", "build", "--release")
	cmd.Dir = buildDir
	// Pin pyo3's build script to the venv's interpreter so it generates
	// bindings for the *runtime* Python version, not whatever python3
	// happens to be first on PATH. Without this, building on a host with
	// system Python 3.13 while the venv is 3.11 emits 3.12+ symbols
	// (e.g. _PyErr_GetRaisedException) that fail at import time.
	//
	// PYO3_USE_ABI3_FORWARD_COMPATIBILITY also kicks in when the chosen
	// Python is newer than pyo3 supports, allowing the abi3 fallback.
	cmd.Env = append(os.Environ(),
		"PYO3_PYTHON="+pyExe,
		"PYO3_USE_ABI3_FORWARD_COMPATIBILITY=1",
	)
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

// maybeInjectPrelude writes a tiny lib.rs wrapper in buildDir that imports
// `pyo3::prelude::*` and `pyo3::types::PyModule`, then `include!`s the
// user's source. Returns the path that should be used as the crate root
// in [lib].path of the generated Cargo.toml.
//
// Behaviour:
//   - rust.AutoPrelude == false → returns rsPath, no wrapper written.
//   - The user's file already references `pyo3` (e.g. `use pyo3::…`,
//     `pyo3::Python`, etc., outside string literals/comments) → no
//     wrapper, returns rsPath directly. This avoids duplicate-import
//     conflicts and respects users who want to be explicit.
//   - Otherwise → writes <buildDir>/lib.rs and returns its path.
//
// `include!(…)` pastes the file's content at the location of the macro
// call, so the user's code is compiled exactly as if it had been the
// crate root, only with the prelude already in scope.
func maybeInjectPrelude(buildDir, rsPath string, content []byte, rust RustConfig) (string, error) {
	if !rust.AutoPrelude {
		return rsPath, nil
	}
	if userReferencesPyO3(content) {
		return rsPath, nil
	}
	wrapperPath := filepath.Join(buildDir, "lib.rs")
	wrapper := fmt.Sprintf(`// Auto-generated by molt: pyo3 prelude wrapper.
// The user's source is included verbatim below.
#![allow(unused_imports)]

use pyo3::prelude::*;
use pyo3::types::PyModule;

include!(%s);
`, fmt.Sprintf("%q", rsPath))
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o644); err != nil {
		return "", err
	}
	return wrapperPath, nil
}

// userReferencesPyO3 reports whether the source already mentions pyo3
// outside of comments and string literals. Conservative: a single-line
// `// use pyo3::…` won't trigger injection skip, since it's a comment.
//
// We do a tiny tokenizer pass: skip /* … */ and // … and "…" / '…'.
func userReferencesPyO3(src []byte) bool {
	const needle = "pyo3"
	s := string(src)
	i := 0
	for i < len(s) {
		c := s[i]
		// Block comment
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return false
			}
			i += j + 4
			continue
		}
		// Line comment
		if c == '/' && i+1 < len(s) && s[i+1] == '/' {
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return false
			}
			i += j + 1
			continue
		}
		// String literal (handles raw `r"…"` and `r#"…"#` minimally)
		if c == '"' {
			j := strings.IndexByte(s[i+1:], '"')
			if j < 0 {
				return false
			}
			i += j + 2
			continue
		}
		if c == '\'' {
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return false
			}
			i += j + 2
			continue
		}
		if c == 'p' && i+len(needle) <= len(s) && s[i:i+len(needle)] == needle {
			return true
		}
		i++
	}
	return false
}
