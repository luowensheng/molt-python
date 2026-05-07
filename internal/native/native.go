// Package native is molt's native-code build pipeline. Phase 1 supports
// Cython (.pyx files in src/). Future backends (multipy's Go/Rust/C/C++/Zig
// embedded blocks) will plug in alongside without changing the surface.
//
// The flow:
//
//   1. Discover  — walk the project for native sources.
//   2. Compile   — for each not in cache, invoke the language's compiler
//                  → emit a shared library at ~/.molt/native/<hash>/<so>.
//   3. PlaceProjectView — link cached artefacts into projstate.Dir(p)/cython/
//                  so Python's import machinery finds them at runtime.
//
// `~/.molt/native/<hash>/` is content-keyed and shared across projects.
// projstate.Dir(p)/cython/ is the per-project view (symlinks → cache).
package native

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"molt/internal/projstate"
	"molt/internal/pyabi"
)

// Source describes one discoverable native input file.
type Source struct {
	Lang        string // "cython" — phase 1 only
	Path        string // absolute on-disk path
	Module      string // dotted name: "fastmath._inner"
	PackagePath string // slash form: "fastmath" (empty if top-level)
	Basename    string // "_inner"
}

// Artifact is one built shared library, sitting in the global cache.
type Artifact struct {
	Source Source
	Hash   string // 16 hex chars of sha256(content||abi||plat)
	Path   string // absolute path of the cached .so
	SoName string // "_inner.cpython-311-darwin.so" — full PEP 3149 name
	AbiTag string
	Plat   string
}

// Discover walks the project for native sources. Scans every root listed in
// cfg.Paths (project-relative). Roots that don't exist are skipped silently
// so the default [".", "src"] works even when there's no src/ directory.
//
// Module names are derived relative to the search root:
//   - root=".":   ./mathx.pyx            → Module="mathx",          PackagePath=""
//   - root="src": src/foo/bar/_inner.pyx  → Module="foo.bar._inner", PackagePath="foo/bar"
//
// Duplicates (same absolute path discovered via two roots) are deduplicated.
// Returns nil when no .pyx files are found — caller treats as a no-op.
func Discover(projectDir string, cfg CythonConfig) ([]Source, error) {
	seen := map[string]bool{} // absolute path → already added
	var out []Source

	for _, rel := range cfg.Paths {
		root := filepath.Join(projectDir, filepath.FromSlash(rel))
		if _, err := os.Stat(root); err != nil {
			continue // missing root is not an error
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}

		if err := filepath.WalkDir(absRoot, func(p string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				name := d.Name()
				if name == "__pycache__" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".pyx") {
				return nil
			}
			absP, err := filepath.Abs(p)
			if err != nil {
				return err
			}
			if seen[absP] {
				return nil
			}
			seen[absP] = true

			relPath, err := filepath.Rel(absRoot, absP)
			if err != nil {
				return err
			}
			dir, file := filepath.Split(relPath)
			basename := strings.TrimSuffix(file, ".pyx")
			pkgPath := strings.TrimSuffix(filepath.ToSlash(dir), "/")
			mod := basename
			if pkgPath != "" {
				mod = strings.ReplaceAll(pkgPath, "/", ".") + "." + basename
			}
			out = append(out, Source{
				Lang:        "cython",
				Path:        absP,
				Module:      mod,
				PackagePath: pkgPath,
				Basename:    basename,
			})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CythonAvailable reports whether `python -m cython --version` works in
// pyExe with the project's syspath dirs on PYTHONPATH. Cython is in the
// global store, so this lookup MUST honour PYTHONPATH — invoking pyExe
// alone would only find stdlib + the venv's empty site-packages.
//
// Used to decide whether to skip the cython pipeline cleanly when the
// user hasn't added Cython to their deps yet.
func CythonAvailable(pyExe string, syspathDirs []string) bool {
	cmd := exec.Command(pyExe, "-m", "cython", "--version")
	cmd.Env = append(envWithPYTHONPATH(syspathDirs), filterPythonEnv(os.Environ())...)
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

// envWithPYTHONPATH returns env entries that put syspathDirs on PYTHONPATH
// — used by every native subprocess (cython compile, cc invoke) so the
// project's deps resolve from the global store.
func envWithPYTHONPATH(syspathDirs []string) []string {
	if len(syspathDirs) == 0 {
		return nil
	}
	return []string{"PYTHONPATH=" + strings.Join(syspathDirs, string(os.PathListSeparator))}
}

// filterPythonEnv strips inherited PYTHONPATH/VIRTUAL_ENV/PYTHONHOME so
// the parent shell's env can't shadow our PYTHONPATH override.
func filterPythonEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		switch k {
		case "PYTHONPATH", "VIRTUAL_ENV", "PYTHONHOME":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// CCompiler returns the first usable C compiler on PATH. Honours $CC if
// set. Tries cc → clang → gcc otherwise. Returns ("", error) when nothing's
// available; the sync caller logs a warning and skips compilation.
func CCompiler() (string, error) {
	if envCC := os.Getenv("CC"); envCC != "" {
		if p, err := exec.LookPath(envCC); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("$CC=%q not found on PATH", envCC)
	}
	for _, c := range []string{"cc", "clang", "gcc"} {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no C compiler on PATH (install clang or gcc)")
}

// PythonIncludeDir returns sysconfig.get_paths()['include'] for pyExe —
// the directory containing Python.h. Cached per pyExe path is overkill for
// now (one call per sync), so just invoke each time.
func PythonIncludeDir(pyExe string) (string, error) {
	out, err := exec.Command(pyExe, "-c",
		"import sysconfig; print(sysconfig.get_paths()['include'])").Output()
	if err != nil {
		return "", fmt.Errorf("python include dir: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// PythonExtSuffix returns sysconfig.get_config_var('EXT_SUFFIX') for pyExe —
// the full PEP 3149 suffix (e.g. ".cpython-311-darwin.so"). The basename
// of a compiled extension module is `<name><ext_suffix>`.
func PythonExtSuffix(pyExe string) (string, error) {
	out, err := exec.Command(pyExe, "-c",
		"import sysconfig; print(sysconfig.get_config_var('EXT_SUFFIX'))").Output()
	if err != nil {
		return "", fmt.Errorf("python ext suffix: %w", err)
	}
	suffix := strings.TrimSpace(string(out))
	if suffix == "" || suffix == "None" {
		// Defensive fallback — should not happen on supported CPython builds.
		if runtime.GOOS == "windows" {
			suffix = ".pyd"
		} else {
			suffix = ".so"
		}
	}
	return suffix, nil
}

// hashSource produces the cache key for a source under a specific ABI/platform
// and compiler config. Config changes (extra args, directives) bust the cache.
func hashSource(content []byte, abiTag, plat string, cfg CythonConfig) string {
	h := sha256.New()
	h.Write(content)
	h.Write([]byte{0})
	h.Write([]byte(abiTag))
	h.Write([]byte{0})
	h.Write([]byte(plat))
	h.Write([]byte{0})
	for _, a := range cfg.ExtraCompileArgs {
		h.Write([]byte(a))
		h.Write([]byte{0})
	}
	for _, k := range sortedKeys(cfg.Directives) {
		h.Write([]byte(k))
		h.Write([]byte("="))
		h.Write([]byte(cfg.Directives[k]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// sortedKeys returns the keys of m in ascending order for deterministic hashing.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort — maps are small.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// platTag picks the most-specific platform tag from pyabi.Info.Platforms
// (e.g. "macosx_11_0_arm64", "linux_x86_64"). Falls back to "any" only if
// the slice is empty (defensive).
func platTag(info pyabi.Info) string {
	for _, p := range info.Platforms {
		if p != "any" && p != "" {
			return p
		}
	}
	if len(info.Platforms) > 0 {
		return info.Platforms[0]
	}
	return "any"
}

// PlaceProjectView creates the per-project symlink (or copy on Windows)
// tree at projstate.Dir(p)/cython/<package_path>/<so_name>. Wipes the
// directory first so removed .pyx files don't leave dead links.
//
// Each package level gets a generated __init__.py that *extends*
// __path__ to union with the same package found elsewhere on sys.path
// (i.e. the user's src/<pkg>/). Without this trick, Python's regular-
// package semantics would shadow .so files in this dir behind .py
// modules in src/, since whichever location is found first owns the
// package's __path__.
//
// The cython dir must appear BEFORE src/ in sys.path for the trick to
// work — caller responsibility (syncplan prepends).
func PlaceProjectView(projectDir string, artifacts []Artifact) error {
	root := filepath.Join(projstate.Dir(projectDir), "cython")
	_ = os.RemoveAll(root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	seenInit := map[string]bool{}
	for _, a := range artifacts {
		dir := root
		if a.Source.PackagePath != "" {
			dir = filepath.Join(root, filepath.FromSlash(a.Source.PackagePath))
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if a.Source.PackagePath != "" {
			parts := strings.Split(a.Source.PackagePath, "/")
			cur := root
			for _, p := range parts {
				cur = filepath.Join(cur, p)
				if !seenInit[cur] {
					if err := writePathExtendingInit(cur); err != nil {
						return err
					}
					seenInit[cur] = true
				}
			}
		}
		dest := filepath.Join(dir, a.SoName)
		_ = os.Remove(dest)
		if runtime.GOOS == "windows" {
			if err := copyFile(a.Path, dest); err != nil {
				return err
			}
		} else {
			if err := os.Symlink(a.Path, dest); err != nil {
				return fmt.Errorf("symlink %s → %s: %w", dest, a.Path, err)
			}
		}
	}
	return nil
}

// pathExtendingInitPy is the body written to every cython/<pkg>/__init__.py.
// It walks sys.path, finds OTHER directories holding the same package
// (e.g. the user's src/<pkg>/), and unions them into __path__. This is
// what lets compiled .so files from cython/ coexist with .py modules
// from src/ inside a single Python package namespace.
const pathExtendingInitPy = `# Auto-generated by molt sync. Edits will be overwritten.
# Merges this cython output dir with the user's src/<pkg>/ so .so files
# and .py modules in the same package coexist.
import os as _os, sys as _sys
_here_parent = _os.path.abspath(_os.path.dirname(_os.path.dirname(__file__)))
_parts = __name__.split('.')
for _p in _sys.path:
    try:
        _abs = _os.path.abspath(_p)
    except Exception:
        continue
    if _abs == _here_parent:
        continue
    _cand = _os.path.join(_abs, *_parts)
    if _os.path.isdir(_cand) and _cand not in __path__:
        __path__.append(_cand)
del _os, _sys, _here_parent, _parts, _p, _abs, _cand
`

func writePathExtendingInit(dir string) error {
	return os.WriteFile(filepath.Join(dir, "__init__.py"), []byte(pathExtendingInitPy), 0o644)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	buf := make([]byte, 64*1024)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			break
		}
	}
	return nil
}
