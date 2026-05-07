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
			return err
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

	// ── Step 1: auto-discovered .pyx and .rs files ──────────────────────────
	sources, err := native.Discover(projectDir, cfg)
	if err != nil {
		return "", fmt.Errorf("native discover: %w", err)
	}

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
		// Separate Cython and Rust sources for availability checks.
		var pyxSources, rsSources []native.Source
		for _, s := range sources {
			if s.Lang == "rust" {
				rsSources = append(rsSources, s)
			} else {
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
					arts, err := native.Compile(pyxSources, *abi, pyExe, cc, includeDir, extSuffix, syspathDirs, verbose, cfg, rustCfg)
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
				arts, err := native.Compile(rsSources, *abi, pyExe, "", includeDir, extSuffix, syspathDirs, verbose, cfg, rustCfg)
				if err != nil {
					return "", err
				}
				allArts = append(allArts, arts...)
			}
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
