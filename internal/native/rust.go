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

	"molt/internal/progress"
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
const rustBuildRecipeVersion = "v4"

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
			progressLine("  ✓ rust    %s  (cached)\n", s.Module)
		}
		return Artifact{
			Source: s, Hash: hash, Path: cachedPath, SoName: soName,
			AbiTag: abiTag, Plat: plat,
		}, nil
	}

	if verbose {
		progressLine("  ↻ rust    %s\n", s.Module)
	}

	buildDir, err := rustBuildRoot(s.Module, hash)
	if err != nil {
		return Artifact{}, err
	}
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return Artifact{}, err
	}
	libPath, err := maybeInjectShim(buildDir, s.Path, content, rust, s.Module)
	if err != nil {
		return Artifact{}, fmt.Errorf("inject pyo3 shim for %s: %w", s.Path, err)
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
	prefix := fmt.Sprintf("    [%s] ", s.Module)
	if out, err := progress.Stream(cmd, prefix, streamingEnabled()); err != nil {
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
			progressLine("  ✓ rust    %s  (cached)\n", module)
		}
		return Artifact{
			Source: src, Hash: hash, Path: cachedPath, SoName: soName,
			AbiTag: abiTag, Plat: plat,
		}, nil
	}

	if verbose {
		progressLine("  ↻ rust    %s\n", module)
	}

	crateName, err := readCrateName(srcDir)
	if err != nil {
		crateName = module
	}

	cmd := exec.Command("cargo", "build", "--release")
	cmd.Dir = srcDir
	prefix := fmt.Sprintf("    [%s] ", module)
	if out, err := progress.Stream(cmd, prefix, streamingEnabled()); err != nil {
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

// maybeInjectShim optionally writes a generated lib.rs in buildDir that
// adds the pyo3 prelude and/or `#[pyfunction]` / `#[pymodule]`
// attributes around the user's source. Returns the path to use as
// [lib].path in the generated Cargo.toml — either the user's file
// (no-op) or the new lib.rs.
//
// Behaviour:
//   - rust.AutoPrelude — when on AND the file doesn't already reference
//     pyo3, prepend `use pyo3::prelude::*; use pyo3::types::PyModule;`.
//   - rust.AutoAttrs   — when on, walk the source and prepend
//     `#[pyfunction]` to every top-level `fn` that has no attribute,
//     except the fn named after the module → `#[pymodule]`.
//
// If neither rewrite applies, returns the user's path unchanged.
func maybeInjectShim(buildDir, rsPath string, content []byte, rust RustConfig, moduleName string) (string, error) {
	wantPrelude := rust.AutoPrelude && !userReferencesPyO3(content)
	wantAttrs := rust.AutoAttrs

	if !wantPrelude && !wantAttrs {
		return rsPath, nil
	}

	body := content
	if wantAttrs {
		body = injectPyO3Attrs(body, moduleName)
	}

	var buf strings.Builder
	buf.WriteString("// Auto-generated by molt: pyo3 boilerplate wrapper.\n")
	buf.WriteString("// Original source: ")
	buf.WriteString(rsPath)
	buf.WriteString("\n")
	buf.WriteString("#![allow(unused_imports)]\n")
	if wantPrelude {
		buf.WriteString("use pyo3::prelude::*;\n")
		buf.WriteString("use pyo3::types::PyModule;\n")
	}
	buf.WriteString("\n")
	buf.Write(body)
	if !strings.HasSuffix(buf.String(), "\n") {
		buf.WriteByte('\n')
	}

	libPath := filepath.Join(buildDir, "lib.rs")
	if err := os.WriteFile(libPath, []byte(buf.String()), 0o644); err != nil {
		return "", err
	}
	return libPath, nil
}

// injectPyO3Attrs scans src line-by-line and prepends `#[pyfunction]` or
// `#[pymodule]` to top-level `fn` declarations that don't already carry
// an attribute. The `#[pymodule]` form is used when the function name
// matches `moduleName`; everything else gets `#[pyfunction]`.
//
// Top-level is detected by anchoring on column 0: nested fns (indented)
// are skipped. Lines that are blank, line-comments (`//`), or doc
// comments (`///`) pass through, and an attribute line on the
// immediately-preceding non-blank line suppresses injection (so users
// can opt a fn out by writing any `#[…]` of their own — `#[allow(…)]`,
// `#[doc(hidden)]`, etc.).
func injectPyO3Attrs(src []byte, moduleName string) []byte {
	lines := strings.Split(string(src), "\n")
	var out strings.Builder
	out.Grow(len(src) + 64)

	// True iff the most recent non-blank, non-comment line was an
	// attribute (`#[...]`). Reset to false after we emit a real item.
	prevWasAttr := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "":
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		case strings.HasPrefix(trimmed, "//"):
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		case strings.HasPrefix(trimmed, "#["):
			prevWasAttr = true
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}

		if !prevWasAttr {
			if name, ok := parseTopLevelFnName(line); ok {
				attr := "#[pyfunction]"
				if name == moduleName {
					attr = "#[pymodule]"
				}
				out.WriteString(attr)
				out.WriteByte('\n')
			}
		}
		out.WriteString(line)
		out.WriteByte('\n')
		prevWasAttr = false
	}

	// Drop the trailing newline we added if the input didn't have one.
	res := out.String()
	if !strings.HasSuffix(string(src), "\n") && strings.HasSuffix(res, "\n") {
		res = strings.TrimSuffix(res, "\n")
	}
	return []byte(res)
}

// parseTopLevelFnName returns the function name of a top-level `fn`
// declaration on `line`, or ("", false) if the line isn't one.
//
// "Top-level" means the line starts at column 0 (no leading whitespace).
// Recognises the pub / async / unsafe modifier soup before `fn`.
func parseTopLevelFnName(line string) (string, bool) {
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return "", false
	}
	rest := line
	// Strip a soup of prefix modifiers in any order. `extern` is the
	// special one: it may carry a string ABI like `extern "C"`.
	for {
		matched := false
		for _, prefix := range []string{"pub(crate) ", "pub(super) ", "pub ", "async ", "unsafe ", "const "} {
			if strings.HasPrefix(rest, prefix) {
				rest = strings.TrimPrefix(rest, prefix)
				matched = true
				break
			}
		}
		if strings.HasPrefix(rest, "extern ") {
			rest = strings.TrimPrefix(rest, "extern ")
			if strings.HasPrefix(rest, `"`) {
				if i := strings.IndexByte(rest[1:], '"'); i >= 0 {
					rest = strings.TrimLeft(rest[i+2:], " ")
				}
			}
			matched = true
		}
		if !matched {
			break
		}
	}
	if !strings.HasPrefix(rest, "fn ") {
		return "", false
	}
	rest = rest[3:]
	end := 0
	for end < len(rest) {
		c := rest[end]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return "", false
	}
	return rest[:end], true
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
