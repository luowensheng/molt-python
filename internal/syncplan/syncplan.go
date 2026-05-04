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

	"os/exec"

	"molt/internal/lockparse"
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
// and the project's .molt/ directory. Does NOT create a .venv.
func Sync(projectDir string, opts Options) error {
	absProj, err := filepath.Abs(projectDir)
	if err != nil {
		return err
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

	// 3. Maybe regenerate uv.lock.
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

	// 8. Write sitecustomize.py — calls site.addsitedir on each store dir so .pth files work.
	if err := writeSiteCustomize(absProj, syspathDirs); err != nil {
		return fmt.Errorf("write sitecustomize.py: %w", err)
	}

	// 9. Generate console-script shims into .molt/bin/.
	if err := writeConsoleShims(absProj, pyExe, syspathDirs, topo, st); err != nil {
		return fmt.Errorf("write shims: %w", err)
	}

	// 10. Update registry.
	if err := registryAdd(absProj, spec.LockHash); err != nil {
		// Non-fatal — log and continue.
		fmt.Fprintf(os.Stderr, "warn: update registry: %v\n", err)
	}

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
	return cmd.Run()
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
	dir := filepath.Join(projectDir, syspath.DirName)
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
	binDir := filepath.Join(projectDir, syspath.DirName, syspath.BinDirName)
	// Wipe stale shims first so removed packages don't leave dead scripts.
	_ = os.RemoveAll(binDir)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	pythonPath := strings.Join(append([]string{filepath.Join(projectDir, syspath.DirName)}, syspathDirs...), string(os.PathListSeparator))

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
