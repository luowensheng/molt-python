// Package syncplan orchestrates the install half of the dependency lifecycle.
// It replaces the per-project .venv produced by `uv sync` with a global
// content-addressed store at ~/.molt/pkg/ plus a per-project
// .molt/syspath.json that points into that store.
package syncplan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"os/exec"

	"molt/internal/editor"
	"molt/internal/lockparse"
	"molt/internal/native"
	"molt/internal/projstate"
	"molt/internal/pyabi"
	"molt/internal/python"
	"molt/internal/store"
	"molt/internal/syspath"
	"molt/internal/uvbin"
	"molt/internal/wheelsrc"
)

type Options struct {
	Frozen  bool
	Refresh bool // bypass store cache and reinstall every dep
	Verbose bool
}

// Sync resolves dependencies (via uv lock) and populates both the global store
// and the project's central state dir (~/.molt/projects/<base>-<hash>/).
// Does NOT create a .venv. Does NOT write inside the project tree.
func Sync(projectDir string, opts Options) error {
	absProj, err := filepath.Abs(projectDir)
	if err != nil {
		return err
	}

	// One-time legacy notice. Older molts wrote state to <project>/.molt/.
	// Don't auto-delete — just nudge the user.
	if legacy, ok := projstate.LegacyInTreeDir(absProj); ok && opts.Verbose {
		fmt.Fprintf(os.Stderr,
			"note: legacy in-tree state detected at %s — molt now stores per-project state under ~/.molt/projects/. Safe to `rm -rf %s` after this sync.\n",
			legacy, legacy)
	}

	// 1. Ensure uv binary is present (download if needed).
	if _, err := uvbin.Ensure(); err != nil {
		return fmt.Errorf("uv: %w", err)
	}

	// 2. Resolve interpreter; auto-install if .python-version is set.
	pm, err := python.New(absProj)
	if err != nil {
		return err
	}
	pyExe, err := pm.Which()
	if err != nil || pyExe == "" {
		if v := pm.Active(); v != "" {
			if instErr := pm.Install(v); instErr != nil {
				return fmt.Errorf("install python %s: %w", v, instErr)
			}
			pyExe, err = pm.Which()
		}
		if err != nil || pyExe == "" {
			return fmt.Errorf("no python interpreter resolved: %v", err)
		}
	}

	// 3a. Honour [tool.molt] requirements: merge each requirements.txt
	// file into pyproject.toml via `uv add -r <file>`. Trigger: file is
	// newer than uv.lock (i.e. user edited it since the last resolve) or
	// uv.lock doesn't exist yet. Skipped under --frozen — CI environments
	// shouldn't be mutating deps.
	if !opts.Frozen {
		if files, err := readRequirementsFiles(absProj); err == nil && len(files) > 0 {
			lockMTime := mtime(filepath.Join(absProj, "uv.lock"))
			for _, f := range files {
				ft := mtime(f)
				if ft.IsZero() {
					continue // file missing — silently skip
				}
				if !lockMTime.IsZero() && !ft.After(lockMTime) {
					continue // file unchanged since last resolve
				}
				if opts.Verbose {
					fmt.Printf("→ uv add -r %s\n", f)
				}
				if err := runUV(absProj, "add", "--no-sync", "-r", f); err != nil {
					return fmt.Errorf("uv add -r %s: %w", f, err)
				}
			}
		}
	}

	// 3b. Maybe regenerate uv.lock.
	lockPath := filepath.Join(absProj, "uv.lock")
	if !opts.Frozen {
		if needsLock(absProj, lockPath) {
			if opts.Verbose {
				fmt.Println("→ uv lock")
			}
			if err := runUV(absProj, "lock"); err != nil {
				return fmt.Errorf("uv lock: %w", err)
			}
		}
	}
	if _, err := os.Stat(lockPath); err != nil {
		return fmt.Errorf("uv.lock not found at %s — run without --frozen", lockPath)
	}

	// 4. Parse lock + detect ABI.
	pkgs, err := lockparse.Parse(lockPath)
	if err != nil {
		return fmt.Errorf("parse uv.lock: %w", err)
	}
	abi, err := pyabi.Detect(pyExe)
	if err != nil {
		return fmt.Errorf("detect interpreter ABI: %w", err)
	}

	// 5. Acquire global store lock; install missing wheels.
	st, err := store.Default()
	if err != nil {
		return err
	}
	release, err := st.Lock()
	if err != nil {
		return fmt.Errorf("acquire store lock: %w", err)
	}
	defer release()

	wr, err := wheelsrc.New()
	if err != nil {
		return err
	}

	// Skip the project itself (uv records the current package as an entry).
	projectName := readProjectName(absProj)

	var ordered []installed
	nameToDir := map[string]string{}

	for _, p := range pkgs {
		if projectName != "" && store.NormalizeName(p.Name) == store.NormalizeName(projectName) {
			continue
		}
		if p.Source == "editable" || p.Source == "path" {
			// Editable / path-source: don't unpack into the store; point syspath at the source dir.
			if p.EditablePath != "" {
				nameToDir[store.NormalizeName(p.Name)] = p.EditablePath
				ordered = append(ordered, installed{pkg: p, dir: p.EditablePath})
			}
			continue
		}
		w, err := lockparse.SelectWheel(p, abi)
		if err != nil {
			if len(p.Wheels) > 0 {
				// Wheels exist but none match this platform — the package is
				// guarded by a platform marker (e.g. nvidia-* Linux-only libs).
				// Skip it silently; it is not needed on the current OS.
				if opts.Verbose {
					fmt.Printf("  ↷ skip    %s %s (no compatible wheel for this platform)\n", p.Name, p.Version)
				}
				continue
			}
			// No wheels at all (sdist-only). If the lock has a sdist URL, try
			// building a wheel from source (e.g. abandoned docopt 0.6.2).
			if p.SdistURL == "" {
				// No wheels, no sdist — platform-excluded or incomplete lock entry.
				if opts.Verbose {
					fmt.Printf("  ↷ skip    %s %s (no wheel and no sdist for this platform)\n", p.Name, p.Version)
				}
				continue
			}
			w, err = buildWheelFromSdist(p, abi, opts.Verbose)
			if err != nil {
				return err
			}
		}
		key, err := store.ParseWheelFilename(w.Filename)
		if err != nil {
			return fmt.Errorf("parse wheel filename %s: %w", w.Filename, err)
		}
		if !opts.Refresh && st.Has(key) {
			if opts.Verbose {
				fmt.Printf("  ✓ cached  %s %s\n", p.Name, p.Version)
			}
		} else {
			if opts.Verbose {
				fmt.Printf("  ↓ install %s %s (%s)\n", p.Name, p.Version, w.Filename)
			}
			whlPath, err := wr.Locate(w.Filename, w.Hash, w.URL)
			if err != nil {
				return fmt.Errorf("locate %s: %w", w.Filename, err)
			}
			if err := st.Install(key, whlPath, w.Hash); err != nil {
				return fmt.Errorf("install %s: %w", w.Filename, err)
			}
		}
		dir := st.Path(key)
		nameToDir[store.NormalizeName(p.Name)] = dir
		ordered = append(ordered, installed{pkg: p, key: key, dir: dir})
	}

	// 6. Topo-sort: dependencies before dependents (left-to-right on PYTHONPATH).
	topo := topoSort(ordered, nameToDir)

	// 7. Build syspath spec.
	syspathDirs := make([]string, 0, len(topo)+1)
	for _, it := range topo {
		syspathDirs = append(syspathDirs, it.dir)
	}
	// Project source dir(s) — guess; fall back to project root if neither exists.
	for _, candidate := range []string{"src", "."} {
		full := filepath.Join(absProj, candidate)
		if st, err := os.Stat(full); err == nil && st.IsDir() {
			syspathDirs = append([]string{full}, syspathDirs...)
			break
		}
	}
	// User-declared extra_paths from [tool.molt] extra_paths = [...].
	// Inserted between the project source dir and the global-store dirs so
	// user code takes precedence over installed packages but installed
	// packages still resolve. Eliminates the need for sys.path.insert(...)
	// boilerplate at the top of scripts.
	if sec, err := readMoltSection(absProj); err == nil && len(sec.ExtraPaths) > 0 {
		expanded := make([]string, 0, len(sec.ExtraPaths))
		for _, p := range sec.ExtraPaths {
			path := p
			if !filepath.IsAbs(path) {
				path = filepath.Join(absProj, path)
			}
			path = filepath.Clean(path)
			if st, err := os.Stat(path); err != nil || !st.IsDir() {
				if opts.Verbose {
					fmt.Fprintf(os.Stderr, "warn: extra_paths entry %q does not exist (skipped)\n", p)
				}
				continue
			}
			expanded = append(expanded, path)
		}
		// Insert after the project source dir (index 0 if present, 0 otherwise).
		insertAt := 0
		if len(syspathDirs) > 0 {
			insertAt = 1
		}
		merged := make([]string, 0, len(syspathDirs)+len(expanded))
		merged = append(merged, syspathDirs[:insertAt]...)
		merged = append(merged, expanded...)
		merged = append(merged, syspathDirs[insertAt:]...)
		syspathDirs = merged
	}

	platTag := ""
	if len(abi.Platforms) > 0 {
		platTag = abi.Platforms[0]
	}
	spec := &syspath.Spec{
		Python:     pyExe,
		Version:    abi.Version,
		PyTag:      abi.PyTag,
		AbiTag:     abi.AbiTag,
		Platform:   platTag,
		LockHash:   fileHashOrEmpty(lockPath),
		ProjectDir: absProj,
		Syspath:    syspathDirs,
	}
	if err := syspath.Save(spec); err != nil {
		return fmt.Errorf("write syspath.json: %w", err)
	}

	// 7b. Cython compilation. Skipped silently when there are no .pyx
	// files. Warns + skips when Cython isn't a dep, or when no C compiler
	// is on PATH. Compiled artefacts are content-keyed in ~/.molt/native/
	// and symlinked into projstate.Dir(p)/cython/. That dir gets appended
	// to syspathDirs *before* sitecustomize/shim generation so the new
	// extension modules are importable from the very next `molt run`.
	if cythonDir, err := compileNativeIfPresent(absProj, pyExe, abi, syspathDirs, opts.Verbose); err != nil {
		return err
	} else if cythonDir != "" {
		// Prepend so Python resolves the cython package's __init__.py FIRST.
		// That __init__.py extends __path__ to include the user's src/<pkg>/,
		// merging .so files (from here) with .py modules (from src/) into
		// one importable package namespace.
		syspathDirs = append([]string{cythonDir}, syspathDirs...)
		spec.Syspath = syspathDirs
		if err := syspath.Save(spec); err != nil {
			return fmt.Errorf("re-write syspath.json after native build: %w", err)
		}
	}

	// 8. Write sitecustomize.py — calls site.addsitedir on each store dir so .pth files work.
	if err := writeSiteCustomize(absProj, syspathDirs); err != nil {
		return fmt.Errorf("write sitecustomize.py: %w", err)
	}

	// 9. Generate console-script shims into .molt/bin/.
	if err := writeConsoleShims(absProj, pyExe, syspathDirs, topo, st); err != nil {
		return fmt.Errorf("write shims: %w", err)
	}

	// 9b. Mojo shim — written when `mojo` is installed as a project dependency.
	// Updates spec.MojoBin + spec.MojoPythonLib and re-saves syspath.json so
	// subsequent `molt run` / `molt mojo` invocations find the correct paths.
	if err := writeMojoShimIfPresent(absProj, pyExe, spec); err != nil {
		fmt.Fprintf(os.Stderr, "warn: mojo shim: %v\n", err)
	}

	// 10. Update registry + projstate meta.
	if err := registryAdd(absProj, spec.LockHash); err != nil {
		// Non-fatal — log and continue.
		fmt.Fprintf(os.Stderr, "warn: update registry: %v\n", err)
	}
	if err := projstate.WriteMeta(absProj, ""); err != nil {
		fmt.Fprintf(os.Stderr, "warn: write projstate meta: %v\n", err)
	}

	// 11. Auto-refresh editor configs that already exist. Silent on success;
	// failure is a soft warning. Users opt in by creating .vscode/settings.json
	// or pyrightconfig.json once (or via `molt editor <name>`).
	editor.RefreshIfPresent(absProj, spec)

	if opts.Verbose {
		fmt.Printf("✓ %d package(s); store=%s\n", len(topo), st.Root)
	}
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// buildWheelFromSdist builds a wheel for pkg from its sdist URL using the
// uv binary already on disk. This handles sdist-only packages (e.g. the
// abandoned docopt 0.6.2) that have no pre-built wheels on PyPI.
// The resulting wheel is stored in the global molt store.
func buildWheelFromSdist(pkg lockparse.ResolvedPkg, abi lockparse.PyABI, verbose bool) (lockparse.Wheel, error) { //nolint:unparam
	if pkg.SdistURL == "" {
		return lockparse.Wheel{}, fmt.Errorf("%s %s: no wheels and no sdist URL in lockfile", pkg.Name, pkg.Version)
	}
	uv, err := uvbin.Ensure()
	if err != nil {
		return lockparse.Wheel{}, err
	}
	tmp, err := os.MkdirTemp("", "molt-build-*")
	if err != nil {
		return lockparse.Wheel{}, err
	}
	defer os.RemoveAll(tmp)

	if verbose {
		fmt.Printf("  ⚙ build   %s %s (sdist-only, building wheel…)\n", pkg.Name, pkg.Version)
	}

	// uv build requires a local path — download the sdist first.
	sdistPath := filepath.Join(tmp, filepath.Base(pkg.SdistURL))
	if err := downloadFile(pkg.SdistURL, sdistPath); err != nil {
		return lockparse.Wheel{}, fmt.Errorf("download sdist for %s %s: %w", pkg.Name, pkg.Version, err)
	}

	cmd := exec.Command(uv, "build", "--wheel", sdistPath, "--out-dir", tmp)
	cmd.Stdout = os.Stderr // build noise to stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return lockparse.Wheel{}, fmt.Errorf("build wheel for %s %s: %w", pkg.Name, pkg.Version, err)
	}

	// Find the wheel uv produced.
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return lockparse.Wheel{}, err
	}
	var whlPath string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".whl") {
			whlPath = filepath.Join(tmp, e.Name())
			break
		}
	}
	if whlPath == "" {
		return lockparse.Wheel{}, fmt.Errorf("uv build produced no wheel for %s %s", pkg.Name, pkg.Version)
	}

	// Install directly into the store.
	st, err := store.Default()
	if err != nil {
		return lockparse.Wheel{}, err
	}
	key, err := store.ParseWheelFilename(filepath.Base(whlPath))
	if err != nil {
		return lockparse.Wheel{}, fmt.Errorf("parse built wheel filename %s: %w", filepath.Base(whlPath), err)
	}
	if err := st.Install(key, whlPath, ""); err != nil {
		return lockparse.Wheel{}, fmt.Errorf("install built wheel %s: %w", filepath.Base(whlPath), err)
	}

	w := lockparse.Wheel{
		URL:         "file://" + whlPath,
		Filename:    filepath.Base(whlPath),
	}
	w.PyTag, w.AbiTag, w.PlatformTag = lockparse.ParseWheelTags(w.Filename)
	return w, nil
}

// downloadFile fetches a URL to a local path.
func downloadFile(url, dest string) error {
	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, url)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func runUV(dir string, args ...string) error {
	uv, err := uvbin.Ensure()
	if err != nil {
		return err
	}
	cmd := exec.Command(uv, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Redirect uv's project env into the per-project state dir so commands
	// like `uv add` don't materialise a top-level .venv/. Mirrors
	// internal/uv.projectEnv.
	cmd.Env = append(os.Environ(), "UV_PROJECT_ENVIRONMENT="+projstate.UvEnv(dir))
	return cmd.Run()
}

// mtime returns the modification time of path, or the zero time if path
// is missing/unreadable.
func mtime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// MoltSection is the parsed [tool.molt] block from pyproject.toml. All
// fields are optional; missing keys map to nil/zero. Paths in *Files /
// ExtraPaths are resolved relative to the project root.
type MoltSection struct {
	RequirementsFiles []string // [tool.molt] requirements / requirements_files
	ExtraPaths        []string // [tool.molt] extra_paths — added to PYTHONPATH
}

// readMoltSection parses the [tool.molt] table from pyproject.toml using
// the existing tiny TOML scanner. Best-effort: malformed config returns
// an empty section rather than failing sync. Recognised keys:
//
//   - requirements (string or array of strings)
//   - requirements_files (array of strings) — alias of `requirements`
//   - extra_paths (array of strings)
func readMoltSection(projectDir string) (*MoltSection, error) {
	data, err := os.ReadFile(filepath.Join(projectDir, "pyproject.toml"))
	if err != nil {
		return nil, err
	}
	out := &MoltSection{}
	lines := strings.Split(string(data), "\n")
	inMolt := false
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inMolt = (line == "[tool.molt]")
			continue
		}
		if !inMolt {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])

		// Multi-line array accumulation (used for both requirements and extra_paths).
		switch key {
		case "requirements", "requirements_files", "extra_paths":
		default:
			continue
		}
		// Single-string form (only valid for requirements / requirements_files).
		if strings.HasPrefix(val, `"`) && strings.HasSuffix(val, `"`) && len(val) >= 2 {
			f := val[1 : len(val)-1]
			if key != "extra_paths" {
				out.RequirementsFiles = append(out.RequirementsFiles, filepath.Join(projectDir, f))
			}
			continue
		}
		// Array form, possibly multi-line — accumulate until ].
		for !strings.Contains(val, "]") && i+1 < len(lines) {
			i++
			val += " " + strings.TrimSpace(lines[i])
		}
		open := strings.Index(val, "[")
		closeIdx := strings.Index(val, "]")
		if open < 0 || closeIdx < 0 || closeIdx <= open {
			continue
		}
		body := val[open+1 : closeIdx]
		var arr []string
		for _, raw := range strings.Split(body, ",") {
			t := strings.TrimSpace(raw)
			if len(t) >= 2 && strings.HasPrefix(t, `"`) && strings.HasSuffix(t, `"`) {
				arr = append(arr, t[1:len(t)-1])
			}
		}
		switch key {
		case "requirements", "requirements_files":
			for _, p := range arr {
				out.RequirementsFiles = append(out.RequirementsFiles, filepath.Join(projectDir, p))
			}
		case "extra_paths":
			out.ExtraPaths = append(out.ExtraPaths, arr...)
		}
	}
	return out, nil
}

// compileNativeIfPresent runs the full native build pipeline:
//  1. Auto-discover .pyx (Cython) and .rs (Rust/PyO3) files.
//  2. Build [[tool.molt.native]] external modules (Rust projects, C, C++, etc.).
//
// Returns the absolute cython view dir to prepend to syspath, or "" when
// nothing was compiled. Hard failures (syntax errors, build failures) are
// returned as errors. Soft cases (Cython/cargo not installed, no C compiler)
// print a warning and continue.
func compileNativeIfPresent(projectDir, pyExe string, abi *pyabi.Info, syspathDirs []string, verbose bool) (string, error) {
	cfg := native.LoadCythonConfig(projectDir)
	rustCfg := native.LoadRustConfig(projectDir)
	zigCfg := native.LoadZigConfig(projectDir)
	kernCfg := native.LoadKernelConfig(projectDir)

	// ── Step 1: auto-discovered .pyx and .rs files ──────────────────────────
	sources, err := native.Discover(projectDir, cfg)
	if err != nil {
		return "", fmt.Errorf("native discover: %w", err)
	}
	// Also discover kernel manifests (*.molt.toml) — language-agnostic.
	kernels, err := native.DiscoverKernels(projectDir, kernCfg)
	if err != nil {
		return "", fmt.Errorf("kernel discover: %w", err)
	}
	sources = append(sources, kernels...)

	extSuffix, err := native.PythonExtSuffix(pyExe)
	if err != nil {
		return "", err
	}
	includeDir, err := native.PythonIncludeDir(pyExe)
	if err != nil {
		return "", err
	}

	var allArts []native.Artifact

	if len(sources) > 0 {
		// Separate by language for per-toolchain availability checks.
		var pyxSources, rsSources, kernelSources []native.Source
		for _, s := range sources {
			switch s.Lang {
			case "rust":
				rsSources = append(rsSources, s)
			case "kernel":
				kernelSources = append(kernelSources, s)
			default:
				pyxSources = append(pyxSources, s)
			}
		}

		if len(pyxSources) > 0 {
			if !native.CythonAvailable(pyExe, syspathDirs) {
				fmt.Fprintln(os.Stderr,
					"warn: .pyx files found but Cython is not installed — add it with `molt add Cython`")
				pyxSources = nil
			} else {
				cc, err := native.CCompiler()
				if err != nil {
					fmt.Fprintf(os.Stderr, "warn: skipping Cython build: %v\n", err)
					pyxSources = nil
				} else {
					if verbose {
						fmt.Printf("→ cython: %d source(s)\n", len(pyxSources))
					}
					arts, err := native.Compile(pyxSources, projectDir, *abi, pyExe, cc, includeDir, extSuffix, syspathDirs, verbose, cfg, rustCfg, zigCfg, kernCfg)
					if err != nil {
						return "", err
					}
					allArts = append(allArts, arts...)
				}
			}
		}

		if len(rsSources) > 0 {
			if !native.CargoAvailable() {
				fmt.Fprintln(os.Stderr,
					"warn: .rs files found but cargo is not on PATH — install Rust from https://rustup.rs")
			} else {
				if verbose {
					fmt.Printf("→ rust:   %d source(s)\n", len(rsSources))
				}
				arts, err := native.Compile(rsSources, projectDir, *abi, pyExe, "", includeDir, extSuffix, syspathDirs, verbose, cfg, rustCfg, zigCfg, kernCfg)
				if err != nil {
					return "", err
				}
				allArts = append(allArts, arts...)
			}
		}

		if len(kernelSources) > 0 {
			if verbose {
				fmt.Printf("→ kernel: %d module(s)\n", len(kernelSources))
			}
			arts, err := native.Compile(kernelSources, projectDir, *abi, pyExe, "", includeDir, extSuffix, syspathDirs, verbose, cfg, rustCfg, zigCfg, kernCfg)
			if err != nil {
				return "", err
			}
			allArts = append(allArts, arts...)
		}
	}

	// ── Step 2: [[tool.molt.native]] external modules ───────────────────────
	extMods, err := native.LoadExternalModules(projectDir, includeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: load external modules: %v\n", err)
	} else if len(extMods) > 0 {
		abiTag := abi.AbiTag
		plat := ""
		for _, p := range abi.Platforms {
			if p != "any" && p != "" {
				plat = p
				break
			}
		}
		if verbose {
			fmt.Printf("→ native: %d external module(s)\n", len(extMods))
		}
		extArts, _, err := native.CheckAndRebuild(extMods, projectDir, abiTag, plat, extSuffix, verbose)
		if err != nil {
			return "", err
		}
		allArts = append(allArts, extArts...)
	}

	// ── Step 3: [[tool.molt.c.modules]] — manifest-free C extensions ────────
	cCfg := native.LoadCConfig(projectDir)
	if len(cCfg.Modules) > 0 {
		if verbose {
			fmt.Printf("→ c: %d module(s)\n", len(cCfg.Modules))
		}
		cArts, err := native.BuildCModules(projectDir, pyExe, includeDir, extSuffix, *abi, zigCfg, cCfg, verbose)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: C module build: %v\n", err)
		}
		allArts = append(allArts, cArts...)
	}

	if len(allArts) == 0 {
		return "", nil
	}

	if err := native.PlaceProjectView(projectDir, allArts); err != nil {
		return "", err
	}
	if err := writeNativeManifest(projectDir, allArts); err != nil {
		fmt.Fprintf(os.Stderr, "warn: write native.json: %v\n", err)
	}
	return filepath.Join(projstate.Dir(projectDir), "cython"), nil
}

// CompileKernels discovers and compiles all kernel modules in projectDir using
// the given cross-compile target. target is a Go-style os_arch string
// ("linux_amd64", "darwin_arm64", …); "" means host build.
//
// Unlike a full Sync, this only touches kernel modules — it does not install
// packages, regenerate uv.lock, or update syspath.json. Intended for use by
// `molt build --target` to compile kernels for a non-host platform before
// packaging the application binary.
func CompileKernels(projectDir, target string, verbose bool) error {
	absProj, err := filepath.Abs(projectDir)
	if err != nil {
		return err
	}
	pm, err := python.New(absProj)
	if err != nil {
		return err
	}
	pyExe, err := pm.Which()
	if err != nil || pyExe == "" {
		return fmt.Errorf("no python interpreter — run molt sync first")
	}
	abi, err := pyabi.Detect(pyExe)
	if err != nil {
		return fmt.Errorf("detect interpreter ABI: %w", err)
	}
	extSuffix, err := native.PythonExtSuffix(pyExe)
	if err != nil {
		return err
	}
	includeDir, err := native.PythonIncludeDir(pyExe)
	if err != nil {
		return err
	}
	kernCfg := native.LoadKernelConfig(absProj)
	kernCfg.Target = target
	kernels, err := native.DiscoverKernels(absProj, kernCfg)
	if err != nil {
		return fmt.Errorf("kernel discover: %w", err)
	}
	if len(kernels) == 0 {
		return nil
	}
	if verbose {
		tLabel := target
		if tLabel == "" {
			tLabel = "host"
		}
		fmt.Printf("→ kernel: %d module(s) [target: %s]\n", len(kernels), tLabel)
	}
	cfg := native.LoadCythonConfig(absProj)
	rustCfg := native.LoadRustConfig(absProj)
	zigCfg := native.LoadZigConfig(absProj)
	arts, err := native.Compile(kernels, absProj, *abi, pyExe, "", includeDir, extSuffix, nil, verbose, cfg, rustCfg, zigCfg, kernCfg)
	if err != nil {
		return err
	}
	if len(arts) == 0 {
		return nil
	}
	if err := native.PlaceProjectView(absProj, arts); err != nil {
		return err
	}
	if err := writeNativeManifest(absProj, arts); err != nil {
		fmt.Fprintf(os.Stderr, "warn: write native.json: %v\n", err)
	}
	return nil
}

// writeNativeManifest emits projstate.Dir(p)/native.json — read by the
// builder's injectCythonArtifacts and by `molt gc`'s reachability scan.
func writeNativeManifest(projectDir string, arts []native.Artifact) error {
	type entry struct {
		Hash      string `json:"hash"`
		Src       string `json:"src"`
		So        string `json:"so"`
		Module    string `json:"module"`
		Abi       string `json:"abi"`
		Platform  string `json:"platform"`
		LastBuilt string `json:"last_built"`
	}
	doc := struct {
		Cython   map[string]entry `json:"cython,omitempty"`
		Rust     map[string]entry `json:"rust,omitempty"`
		External map[string]entry `json:"external,omitempty"`
	}{
		Cython:   map[string]entry{},
		Rust:     map[string]entry{},
		External: map[string]entry{},
	}
	now := nowRFC3339()
	for _, a := range arts {
		rel, _ := filepath.Rel(projectDir, a.Source.Path)
		soRel := "cython/"
		if a.Source.PackagePath != "" {
			soRel += a.Source.PackagePath + "/"
		}
		soRel += a.SoName
		e := entry{
			Hash:      a.Hash,
			Src:       rel,
			So:        soRel,
			Module:    a.Source.Module,
			Abi:       a.AbiTag,
			Platform:  a.Plat,
			LastBuilt: now,
		}
		switch a.Source.Lang {
		case "rust":
			doc.Rust[a.Source.Module] = e
		case "external":
			doc.External[a.Source.Module] = e
		default:
			doc.Cython[a.Source.Module] = e
		}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(projstate.Native(projectDir), data, 0o644)
}

// NativeChanged reports whether any native source (.pyx, .rs, or
// [[tool.molt.native]] entry) is newer than the last native.json written by
// sync. Uses mtime comparison — fast and dependency-free.
// Returns false on any error (conservative: avoids spurious rebuilds).
func NativeChanged(projectDir string) bool {
	nativeJsonPath := projstate.Native(projectDir)
	info, err := os.Stat(nativeJsonPath)
	if err != nil {
		// No native.json yet — changed only if there are sources to compile.
		cfg := native.LoadCythonConfig(projectDir)
		srcs, _ := native.Discover(projectDir, cfg)
		extMods, _ := native.LoadExternalModules(projectDir, "")
		return len(srcs) > 0 || len(extMods) > 0
	}
	cutoff := info.ModTime()

	// Auto-discovered .pyx and .rs sources.
	cfg := native.LoadCythonConfig(projectDir)
	srcs, _ := native.Discover(projectDir, cfg)
	for _, s := range srcs {
		if fi, err := os.Stat(s.Path); err == nil && fi.ModTime().After(cutoff) {
			return true
		}
	}

	// [[tool.molt.native]] external modules.
	extMods, _ := native.LoadExternalModules(projectDir, "")
	for _, m := range extMods {
		if native.AnyFileNewerThan(m.SrcDir, cutoff) {
			return true
		}
	}
	return false
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// readRequirementsFiles is a thin wrapper preserved for callers that
// only need that subset.
func readRequirementsFiles(projectDir string) ([]string, error) {
	sec, err := readMoltSection(projectDir)
	if err != nil {
		return nil, err
	}
	return sec.RequirementsFiles, nil
}

func needsLock(projectDir, lockPath string) bool {
	pyproj, err := os.Stat(filepath.Join(projectDir, "pyproject.toml"))
	if err != nil {
		return false
	}
	lock, err := os.Stat(lockPath)
	if err != nil {
		return true
	}
	return pyproj.ModTime().After(lock.ModTime())
}

func fileHashOrEmpty(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// readProjectName extracts the project's own name from pyproject.toml so we
// can skip its lock entry. Best-effort regex-style scan; uv.lock's name field
// is canonical.
func readProjectName(projectDir string) string {
	data, err := os.ReadFile(filepath.Join(projectDir, "pyproject.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "name") {
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		v := strings.TrimSpace(line[eq+1:])
		v = strings.Trim(v, `"' `)
		if v != "" {
			return v
		}
	}
	return ""
}

type installed struct {
	pkg lockparse.ResolvedPkg
	key store.Key
	dir string
}

func topoSort(items []installed, _ map[string]string) []installed {
	indexByName := map[string]int{}
	for i, it := range items {
		indexByName[store.NormalizeName(it.pkg.Name)] = i
	}
	visited := make([]int, len(items)) // 0=unseen, 1=in-progress, 2=done
	out := make([]installed, 0, len(items))
	var visit func(i int)
	visit = func(i int) {
		if visited[i] == 2 {
			return
		}
		visited[i] = 1
		for _, dep := range items[i].pkg.Dependencies {
			if j, ok := indexByName[store.NormalizeName(dep)]; ok {
				visit(j)
			}
		}
		visited[i] = 2
		out = append(out, items[i])
	}
	order := make([]int, len(items))
	for i := range items {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return items[order[a]].pkg.Name < items[order[b]].pkg.Name })
	for _, i := range order {
		visit(i)
	}
	return out
}

func writeSiteCustomize(projectDir string, syspathDirs []string) error {
	dir := projstate.Dir(projectDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Auto-generated by molt sync. Edits will be overwritten.\n")
	b.WriteString("import site\n")
	b.WriteString("for _d in [\n")
	for _, d := range syspathDirs {
		fmt.Fprintf(&b, "    %q,\n", d)
	}
	b.WriteString("]:\n    site.addsitedir(_d)\n")
	return os.WriteFile(filepath.Join(dir, syspath.SiteCustomize), []byte(b.String()), 0o644)
}

func writeConsoleShims(projectDir, pyExe string, syspathDirs []string, items []installed, st *store.Store) error {
	binDir := projstate.Bin(projectDir)
	// Wipe stale shims first so removed packages don't leave dead scripts.
	_ = os.RemoveAll(binDir)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	pythonPath := strings.Join(append([]string{projstate.Dir(projectDir)}, syspathDirs...), string(os.PathListSeparator))

	// Always create python/python3 shims so tasks like `python -m foo` work
	// when run via molt run / molt task. Without these, the shell only sees
	// the system `python` (or none at all), missing the global store.
	if err := writePythonShim(binDir, "python", pyExe, pythonPath); err != nil {
		return err
	}
	if err := writePythonShim(binDir, "python3", pyExe, pythonPath); err != nil {
		return err
	}

	for _, it := range items {
		if it.key.Name == "" {
			continue // editable / path source — no .meta.json
		}
		meta, err := st.Meta(it.key)
		if err != nil {
			continue
		}
		cs := meta.EntryPoints["console_scripts"]
		for name, target := range cs {
			module, attr, ok := strings.Cut(target, ":")
			if !ok {
				continue
			}
			// Strip extras like "name = mod:attr [extra]".
			if i := strings.IndexByte(attr, ' '); i >= 0 {
				attr = attr[:i]
			}
			attr = strings.TrimSpace(attr)
			module = strings.TrimSpace(module)
			if err := writeShim(binDir, name, pyExe, pythonPath, module, attr); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeShim(binDir, name, pyExe, pythonPath, module, attr string) error {
	if runtime.GOOS == "windows" {
		path := filepath.Join(binDir, name+".cmd")
		body := fmt.Sprintf("@echo off\r\nset PYTHONPATH=%s\r\nset VIRTUAL_ENV=\r\nset PYTHONHOME=\r\n\"%s\" -c \"import sys; from %s import %s as _m; sys.exit(_m())\" %%*\r\n",
			pythonPath, pyExe, module, attr)
		return os.WriteFile(path, []byte(body), 0o755)
	}
	path := filepath.Join(binDir, name)
	body := fmt.Sprintf(`#!/bin/sh
export PYTHONPATH=%s
unset VIRTUAL_ENV PYTHONHOME
exec %s -c 'import sys; from %s import %s as _m; sys.exit(_m())' "$@"
`, shellQuote(pythonPath), shellQuote(pyExe), module, attr)
	return os.WriteFile(path, []byte(body), 0o755)
}

// writePythonShim generates a name (`python` / `python3`) shim that execs the
// project's interpreter with PYTHONPATH set, forwarding all args verbatim.
// Unlike console-script shims this does NOT wrap a specific entry point —
// `python <args...>` works exactly as if invoked directly.
func writePythonShim(binDir, name, pyExe, pythonPath string) error {
	if runtime.GOOS == "windows" {
		path := filepath.Join(binDir, name+".cmd")
		body := fmt.Sprintf("@echo off\r\nset PYTHONPATH=%s\r\nset VIRTUAL_ENV=\r\nset PYTHONHOME=\r\n\"%s\" %%*\r\n",
			pythonPath, pyExe)
		return os.WriteFile(path, []byte(body), 0o755)
	}
	path := filepath.Join(binDir, name)
	body := fmt.Sprintf(`#!/bin/sh
export PYTHONPATH=%s
unset VIRTUAL_ENV PYTHONHOME
exec %s "$@"
`, shellQuote(pythonPath), shellQuote(pyExe))
	return os.WriteFile(path, []byte(body), 0o755)
}

// writeMojoShimIfPresent detects whether the `mojo` package was installed by
// checking for its console-script shim in the project's bin/ directory (written
// by writeConsoleShims at step 9 of Sync).
//
// When mojo is installed, the mojo-compiler wheel stores its SDK assets under a
// *.data/platlib/modular/ subdirectory rather than directly in site-packages —
// the layout pip would produce after a normal install.  This means the Python
// entry-point wrapper (mojo/_package_root.py) cannot locate the SDK root via
// its usual heuristics, so we bypass the Python wrapper entirely: we locate the
// real mojo binary and the Mojo standard-library import path ourselves, then
// overwrite the shim with a direct-exec version that sets all necessary
// MODULAR_* env vars before invoking the binary.
func writeMojoShimIfPresent(projDir, pyExe string, spec *syspath.Spec) error {
	binDir := projstate.Bin(projDir)
	shimPath := filepath.Join(binDir, "mojo")
	if runtime.GOOS == "windows" {
		shimPath = filepath.Join(binDir, "mojo.cmd")
	}
	if _, err := os.Stat(shimPath); err != nil {
		// console-script shim not present → mojo not installed
		if spec.MojoBin != "" || spec.MojoPythonLib != "" {
			spec.MojoBin = ""
			spec.MojoPythonLib = ""
			_ = syspath.Save(spec)
		}
		return nil
	}

	// Locate the real mojo binary and the Mojo stdlib import path.
	sdkRoot, mojoImportPath := findMojoSDKRoots(spec.Syspath)
	if sdkRoot == "" {
		fmt.Fprintf(os.Stderr,
			"warning: mojo console-script shim found but SDK assets not located "+
				"in .data/platlib/ — `molt run *.mojo` may fail\n")
		return nil
	}
	realMojoBin := filepath.Join(sdkRoot, "bin", "mojo")

	lib, err := deriveMojoPythonLib(pyExe)
	if err != nil || lib == "" {
		fmt.Fprintf(os.Stderr,
			"warning: mojo installed but could not locate libpython — "+
				"set MOJO_PYTHON_LIBRARY manually if `mojo run` fails\n")
	}

	spec.MojoBin = shimPath
	spec.MojoPythonLib = lib
	if err := syspath.Save(spec); err != nil {
		return fmt.Errorf("re-save syspath.json after mojo detection: %w", err)
	}

	// Overwrite the generic console-script shim with a direct-exec shim that
	// sets all MODULAR_* env vars the mojo binary needs and exec's it directly,
	// bypassing the Python entry-point wrapper that fails in molt's layout.
	if err := writeMojoShim(binDir, realMojoBin, sdkRoot, mojoImportPath, lib, spec.BuildPythonPath()); err != nil {
		return err
	}

	// Patch every other mojo-related console-script shim (crash reporter, lld,
	// mojo-lldb, etc.) that references mojo._entrypoints: replace the
	// "unset VIRTUAL_ENV" line with "export VIRTUAL_ENV=<sdkRoot>" so that
	// get_package_root() inside those sub-processes can locate the SDK assets.
	return patchMojoRelatedShims(binDir, sdkRoot)
}

// patchMojoRelatedShims fixes up console-script shims written by
// writeConsoleShims that invoke mojo._entrypoints functions.  Those shims do
// "unset VIRTUAL_ENV", which causes get_package_root() to fail in molt's store
// layout.  Replacing it with an export makes the Python wrapper find the SDK
// root via the VIRTUAL_ENV fallback path in _package_root.py.
func patchMojoRelatedShims(binDir, sdkRoot string) error {
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return err
	}
	replacement := "export VIRTUAL_ENV=" + shellQuote(sdkRoot) + "\nunset PYTHONHOME"
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip the mojo shim itself — it is already a direct-exec shim that
		// sets VIRTUAL_ENV correctly and does not use mojo._entrypoints.
		if name == "mojo" || name == "mojo.cmd" {
			continue
		}
		path := filepath.Join(binDir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		body := string(content)
		if !strings.Contains(body, "mojo._entrypoints") {
			continue
		}
		patched := strings.Replace(body, "unset VIRTUAL_ENV PYTHONHOME", replacement, 1)
		if patched == body {
			continue // already patched or different format
		}
		if err := os.WriteFile(path, []byte(patched), 0o755); err != nil {
			return fmt.Errorf("patch mojo shim %s: %w", name, err)
		}
	}
	return nil
}

// findMojoSDKRoots searches syspath entries for the mojo-compiler and
// mojo-compiler-mojo-libs packages and returns:
//
//	sdkRoot      — path to the modular/ dir that contains bin/mojo
//	importPath   — path to the modular/lib/mojo stdlib directory
//
// In molt's store, wheel .data/platlib/ content is not merged into the package
// root, so the modular/ dir lives at <pkgdir>/<wheel>.data/platlib/modular/.
func findMojoSDKRoots(syspathEntries []string) (sdkRoot, importPath string) {
	for _, dir := range syspathEntries {
		base := filepath.Base(dir)
		// mojo-compiler (platform wheel): has bin/mojo and runtime dylibs
		if strings.Contains(dir, "mojo-compiler") &&
			!strings.Contains(dir, "mojo-compiler-mojo-libs") &&
			!strings.Contains(base, "mojo-compiler-mojo-libs") {
			if root := findDataPlatlib(dir, "modular"); root != "" {
				if _, err := os.Stat(filepath.Join(root, "bin", "mojo")); err == nil {
					sdkRoot = root
				}
			}
		}
		// mojo-compiler-mojo-libs (pure-Python wheel): has lib/mojo stdlib
		if strings.Contains(dir, "mojo-compiler-mojo-libs") {
			if root := findDataPlatlib(dir, "modular"); root != "" {
				candidate := filepath.Join(root, "lib", "mojo")
				if _, err := os.Stat(candidate); err == nil {
					importPath = candidate
				}
			}
		}
	}
	return
}

// findDataPlatlib looks for <pkgDir>/<something>.data/platlib/<subdir> and
// returns the first match found, or "" if none exist.
func findDataPlatlib(pkgDir, subdir string) string {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), ".data") {
			continue
		}
		candidate := filepath.Join(pkgDir, e.Name(), "platlib", subdir)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// deriveMojoPythonLib asks the project's Python interpreter for the absolute
// path to its libpython shared library. Run once at sync time; result stored
// in syspath.json so subsequent runs need not shell out to Python again.
func deriveMojoPythonLib(pyExe string) (string, error) {
	script := `import sysconfig, os, sys, platform
libdir = sysconfig.get_config_var('LIBDIR') or ''
ldver = sysconfig.get_config_var('LDVERSION') or ''
if not ldver:
    ldver = f'{sys.version_info.major}.{sys.version_info.minor}'
ext = '.dylib' if platform.system() == 'Darwin' else '.so'
for name in [f'libpython{ldver}{ext}', f'libpython{ldver}.so.1.0']:
    path = os.path.join(libdir, name)
    if os.path.exists(path):
        print(path)
        break
`
	out, err := exec.Command(pyExe, "-c", script).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// writeMojoShim writes a self-contained shim to binDir/mojo that bypasses the
// mojo Python entry-point wrapper (which cannot find SDK assets in molt's store
// layout) and instead exec-s the real mojo binary directly with all required
// MODULAR_* environment variables pre-set.
//
// Parameters:
//
//	binDir       — project bin/ directory (shim written here)
//	realMojoBin  — absolute path to the actual mojo native binary
//	sdkRoot      — modular/ SDK root (parent of bin/ and lib/)
//	importPath   — path to the Mojo stdlib (modular/lib/mojo)
//	pythonLib    — absolute path to libpython shared library
//	pythonPath   — colon-separated PYTHONPATH string
func writeMojoShim(binDir, realMojoBin, sdkRoot, importPath, pythonLib, pythonPath string) error {
	if runtime.GOOS == "windows" {
		path := filepath.Join(binDir, "mojo.cmd")
		body := fmt.Sprintf(
			"@echo off\r\n"+
				"set MODULAR_MAX_PACKAGE_ROOT=%s\r\n"+
				"set MODULAR_MOJO_MAX_PACKAGE_ROOT=%s\r\n"+
				"set MODULAR_MOJO_MAX_DRIVER_PATH=%s\r\n"+
				"set MODULAR_MOJO_MAX_IMPORT_PATH=%s\r\n"+
				"set MOJO_PYTHON_LIBRARY=%s\r\n"+
				"set PYTHONPATH=%s\r\n"+
				"set VIRTUAL_ENV=\r\nset PYTHONHOME=\r\n"+
				"\"%s\" %%*\r\n",
			sdkRoot, sdkRoot, realMojoBin, importPath,
			pythonLib, pythonPath, realMojoBin)
		return os.WriteFile(path, []byte(body), 0o755)
	}
	libDir := ""
	if pythonLib != "" {
		libDir = filepath.Dir(pythonLib)
	}
	path := filepath.Join(binDir, "mojo")
	body := fmt.Sprintf(`#!/bin/sh
# molt-managed Mojo shim — exec real binary, bypass Python entry-point wrapper
export MODULAR_MAX_PACKAGE_ROOT=%s
export MODULAR_MOJO_MAX_PACKAGE_ROOT=%s
export MODULAR_MOJO_MAX_DRIVER_PATH=%s
export MODULAR_MOJO_MAX_IMPORT_PATH=%s
export MOJO_PYTHON_LIBRARY=%s
export PYTHONPATH=%s
export LD_LIBRARY_PATH=%s${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}
export DYLD_FALLBACK_LIBRARY_PATH=%s${DYLD_FALLBACK_LIBRARY_PATH:+:$DYLD_FALLBACK_LIBRARY_PATH}
# VIRTUAL_ENV is set to sdkRoot so mojo sub-processes (e.g. the crash reporter)
# that call get_package_root() can find the SDK assets via the VIRTUAL_ENV fallback.
export VIRTUAL_ENV=%s
unset PYTHONHOME
exec %s "$@"
`,
		shellQuote(sdkRoot),
		shellQuote(sdkRoot),
		shellQuote(realMojoBin),
		shellQuote(importPath),
		shellQuote(pythonLib),
		shellQuote(pythonPath),
		shellQuote(libDir)+":", shellQuote(libDir)+":",
		shellQuote(sdkRoot),
		shellQuote(realMojoBin))
	return os.WriteFile(path, []byte(body), 0o755)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ── registry ─────────────────────────────────────────────────────────────────

type registry struct {
	Projects map[string]string `json:"projects"` // projectDir -> lock_hash
}

func registryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".molt", "registry.json"), nil
}

func loadRegistry() (*registry, error) {
	p, err := registryPath()
	if err != nil {
		return nil, err
	}
	r := &registry{Projects: map[string]string{}}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, err
	}
	_ = json.Unmarshal(data, r)
	if r.Projects == nil {
		r.Projects = map[string]string{}
	}
	return r, nil
}

func saveRegistry(r *registry) error {
	p, err := registryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

func registryAdd(projectDir, lockHash string) error {
	r, err := loadRegistry()
	if err != nil {
		return err
	}
	r.Projects[projectDir] = lockHash
	return saveRegistry(r)
}
