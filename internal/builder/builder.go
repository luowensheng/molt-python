// internal/builder/builder.go
//
// Builder orchestrates the full build pipeline:
//
//   1. Load molt.yaml (if present) and apply its settings.
//   2. Compile the launcher binary for the target platform.
//   3. Embed the project source into a deterministic tar.gz payload.
//      — In allowlist mode when molt.yaml has include/assets.
//      — In legacy denylist mode otherwise.
//   4. Compute the integrity root hash over the packaged files.
//   5. Write the external integrity manifest (JSON) next to the binary.
//   6. Assemble: launcher || payload || extended trailer (offset+hash+magic).
//
// Steps 4-6 are the new bits — the rest mirrors the original builder with
// minor adjustments for the Result/Config shape change.

package builder

import (
	"archive/tar"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"molt/embedder"
	"molt/internal/integrity"
	"molt/internal/moltcfg"
	"molt/internal/projstate"
	"molt/internal/tasks"
	"molt/pkg/types"
)

type Builder struct {
	cfg types.BuildConfig
}

func New(cfg types.BuildConfig) *Builder {
	if cfg.TargetOS == "" {
		cfg.TargetOS = runtime.GOOS
	}
	if cfg.TargetArch == "" {
		cfg.TargetArch = runtime.GOARCH
	}
	if cfg.EmbedIgnoreFile == "" {
		cfg.EmbedIgnoreFile = ".moltignore"
	}
	if !cfg.EmbedStrict {
		cfg.EmbedStrict = true
	}
	return &Builder{cfg: cfg}
}

func (b *Builder) Build() error {
	isCross := b.cfg.TargetOS != runtime.GOOS || b.cfg.TargetArch != runtime.GOARCH
	if isCross && b.cfg.CrossBuildMode != types.CrossBuildBestEffort {
		return fmt.Errorf("cross-build (%s/%s -> %s/%s): use --best-effort for testing",
			runtime.GOOS, runtime.GOARCH, b.cfg.TargetOS, b.cfg.TargetArch)
	}

	// Load molt.yaml if present. Absence is not an error — we fall back to
	// legacy behaviour so existing molt projects keep working.
	moltCfg, err := loadMoltConfig(b.cfg.ProjectPath)
	if err != nil {
		return err
	}
	// molt.yaml values override the CLI-supplied name/version if both are
	// present. Principle: the file is the source of truth; flags are the
	// override mechanism. We invert here to: flags supplied → respect;
	// else → use the file.
	if moltCfg != nil {
		if b.cfg.Name == "" || b.cfg.Name == filepath.Base(b.cfg.ProjectPath) {
			b.cfg.Name = moltCfg.Project.Name
		}
		if b.cfg.Version == "0.1.0" && moltCfg.Project.Version != "" {
			b.cfg.Version = moltCfg.Project.Version
		}
	}

	fmt.Printf("Building %s v%s (%s/%s)...\n",
		b.cfg.Name, b.cfg.Version, b.cfg.TargetOS, b.cfg.TargetArch)
	if moltCfg != nil {
		fmt.Printf("  Config: %s\n", moltCfg.SourcePath)
	}

	// 1. Compile launcher.
	launcherPath, err := compileLauncher(b.cfg.TargetOS, b.cfg.TargetArch)
	if err != nil {
		return fmt.Errorf("compile launcher: %w", err)
	}
	defer os.Remove(launcherPath)

	// 2. Build manifest. Detect the project layout to pick the right
	// runtime entry point — src/<pkg>/__main__.py wants `python -m <pkg>`,
	// a bare main.py wants `python main.py`, etc. Without this every
	// non-default layout used to fail with `No module named <name>.main`.
	mainModule, mainScript := detectMainEntry(b.cfg.ProjectPath, b.cfg.Name)

	// Collect [tool.molt.tasks] from pyproject.toml so the built binary
	// can dispatch them as `<app> <task>` at runtime. Best-effort — a
	// project without tasks just ships an empty map and uses the default
	// entry only.
	tasks, _ := readPyprojectTasks(b.cfg.ProjectPath)

	m := &types.Manifest{
		AppName:    b.cfg.Name,
		Version:    b.cfg.Version,
		MainModule: mainModule,
		MainScript: mainScript,
		Tasks:      tasks,
		Profile:    b.cfg.Profile,
		TargetOS:   b.cfg.TargetOS,
		TargetArch: b.cfg.TargetArch,
		BuildTime:  time.Now().UTC().Format(time.RFC3339),
	}

	// 3. Create payload and capture the packaged-file list for integrity.
	payloadPath, embedResult, err := b.createPayload(m, moltCfg)
	if err != nil {
		return fmt.Errorf("create payload: %w", err)
	}
	defer os.Remove(payloadPath)

	// Surface oversized-file warnings.
	for _, w := range embedResult.SizeWarnings {
		fmt.Printf("  ⚠ %s\n", w)
	}

	// 4. Build integrity manifest (root_hash + external manifest file).
	integrityManifest := buildIntegrityManifest(b.cfg, moltCfg, embedResult)
	rootHash := integrityManifest.RootHash

	// 5. Write external manifest file next to the binary (if enabled).
	manifestPath := b.resolveManifestPath(moltCfg)
	if integrityEnabled(moltCfg) {
		if err := integrity.WriteManifest(integrityManifest, manifestPath); err != nil {
			return fmt.Errorf("write integrity manifest: %w", err)
		}
		fmt.Printf("  ✓ Integrity manifest: %s\n", manifestPath)
	}

	// Also embed a condensed copy of the integrity manifest INSIDE the
	// binary at .molt/integrity.json, so `molt inspect` works offline and
	// the launcher can access its audit record without the sidecar file.
	if err := reinjectIntegrityIntoPayload(payloadPath, integrityManifest); err != nil {
		return fmt.Errorf("embed integrity into payload: %w", err)
	}

	// Inject any cython artefacts compiled during sync. They live OUTSIDE
	// the project tree (under projstate.Dir/cython/), so the embedder's
	// project-rooted walk doesn't pick them up — append them to the tar
	// at src/<package_path>/<so_name> where Python's import machinery
	// expects them.
	if err := injectCythonArtifacts(payloadPath, b.cfg.ProjectPath, b.cfg.TargetOS, b.cfg.TargetArch); err != nil {
		return fmt.Errorf("embed cython artefacts: %w", err)
	}

	// 6. Assemble: launcher + payload + extended trailer with root_hash.
	suffix := ""
	if b.cfg.TargetOS == "windows" {
		suffix = ".exe"
	}
	outputPath := b.cfg.OutputPath + suffix
	if err := assembleBinary(launcherPath, payloadPath, outputPath, rootHash); err != nil {
		return fmt.Errorf("assemble binary: %w", err)
	}

	info, _ := os.Stat(outputPath)
	sizeMB := float64(0)
	if info != nil {
		sizeMB = float64(info.Size()) / 1_000_000
	}
	fmt.Printf("  ✓ Created: %s (%.1fMB)\n", outputPath, sizeMB)
	fmt.Printf("    root_hash: %s\n", rootHash)
	fmt.Printf("    files:     %d   total: %.1fMB\n",
		len(embedResult.Files), float64(embedResult.TotalBytes)/1_000_000)
	fmt.Printf("    install:   molt_INSTALL_BASE=/opt ./%s install\n",
		filepath.Base(outputPath))
	return nil
}

// loadMoltConfig returns the project's molt.yaml or nil if absent. Any
// parse/validation error is fatal — we don't proceed with a half-understood
// config.
// detectMainEntry inspects the project layout to pick the right runtime
// entry. Returns (mainModule, mainScript) — exactly one is non-empty.
//
// Resolution order:
//
//  1. src/<pkg>/__main__.py    → MainModule = "<pkg>"      (run `python -m <pkg>`)
//  2. <pkg>/__main__.py        → MainModule = "<pkg>"
//  3. src/<pkg>/main.py        → MainModule = "<pkg>.main"
//  4. <pkg>/main.py            → MainModule = "<pkg>.main"
//  5. <project>/main.py        → MainScript = "main.py"    (run `python main.py`)
//  6. (fallback)               → MainModule = "<pkg>.main" (legacy default)
//
// pkg is the project name normalised for Python: lowercase + dashes/dots/spaces
// converted to underscores.
func detectMainEntry(projectPath, appName string) (mainModule, mainScript string) {
	pkg := normPkg(appName)
	exists := func(rel ...string) bool {
		full := filepath.Join(append([]string{projectPath}, rel...)...)
		_, err := os.Stat(full)
		return err == nil
	}
	switch {
	case exists("src", pkg, "__main__.py"):
		return pkg, ""
	case exists(pkg, "__main__.py"):
		return pkg, ""
	case exists("src", pkg, "main.py"):
		return pkg + ".main", ""
	case exists(pkg, "main.py"):
		return pkg + ".main", ""
	case exists("main.py"):
		return "", "main.py"
	default:
		return pkg + ".main", ""
	}
}

func normPkg(name string) string {
	r := strings.NewReplacer("-", "_", ".", "_", " ", "_")
	return strings.ToLower(r.Replace(name))
}

// readPyprojectTasks reads [tool.molt.tasks] from pyproject.toml using the
// tasks package's existing parser, then converts the slice into the map
// shape the launcher expects. Best-effort: returns nil on any failure so
// builds never break because of a malformed tasks section.
func readPyprojectTasks(projectPath string) (map[string]types.Task, error) {
	r := tasks.New(projectPath)
	list, err := r.List()
	if err != nil || len(list) == 0 {
		return nil, err
	}
	out := make(map[string]types.Task, len(list))
	for _, t := range list {
		out[t.Name] = t
	}
	return out, nil
}

func loadMoltConfig(dir string) (*types.MoltConfig, error) {
	cfg, err := moltcfg.Load(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("load molt.yaml: %w", err)
	}
	return cfg, nil
}

// createPayload invokes the embedder with the right mode (allowlist from
// molt.yaml if present; legacy otherwise). Also writes the .molt/manifest.json
// into the project temporarily so it gets swept up as part of the payload.
func (b *Builder) createPayload(
	m *types.Manifest,
	moltCfg *types.MoltConfig,
) (string, *embedder.Result, error) {
	tmp, err := os.CreateTemp("", "molt-payload-*.tar.gz")
	if err != nil {
		return "", nil, err
	}
	tmp.Close()
	payloadPath := tmp.Name()

	// Stage manifest.json (and optional config.json) into a transient
	// <project>/.molt/ directory so the embedder picks them up at the
	// path the launcher expects (src/.molt/...). Per-project state moved
	// out of the tree, but the build embedder walks RootDir = projectPath,
	// so a transient in-tree dir is the simplest way to inject files at a
	// specific tar path. Remove it again on exit so the user's project
	// tree stays clean.
	manifestDir := filepath.Join(b.cfg.ProjectPath, ".molt")
	dirExisted := false
	if st, err := os.Stat(manifestDir); err == nil && st.IsDir() {
		dirExisted = true
	}
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		return "", nil, err
	}
	// Register dir cleanup FIRST so it runs LAST (defers are LIFO).
	// Only sweep the dir if we created it; if it pre-existed (legacy
	// install), the user owns it.
	if !dirExisted {
		defer func() { _ = os.Remove(manifestDir) }()
	}

	manifestPath := filepath.Join(manifestDir, "manifest.json")
	manifestData, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(manifestPath, manifestData, 0o644); err != nil {
		return "", nil, err
	}
	defer os.Remove(manifestPath)

	// Also serialise molt.yaml (if any) into .molt/ so the LAUNCHER can
	// read runtime commands and hook definitions without needing the
	// original file on disk.
	if moltCfg != nil {
		cfgJSON, _ := json.MarshalIndent(moltCfg, "", "  ")
		cfgPath := filepath.Join(manifestDir, "config.json")
		if err := os.WriteFile(cfgPath, cfgJSON, 0o644); err != nil {
			return "", nil, err
		}
		defer os.Remove(cfgPath)
	}

	ecfg := embedder.Config{
		RootDir:         b.cfg.ProjectPath,
		OutputFile:      payloadPath,
		PrefixInTar:     "src/",
		IgnoreFile:      b.cfg.EmbedIgnoreFile,
		FailOnSensitive: b.cfg.EmbedStrict,
	}
	if moltCfg != nil {
		// Allowlist mode. Include globs replace the default ignore list.
		// We augment with ".molt/**" so the transient build-time .molt/
		// dir (manifest.json, config.json) ships in the embedded payload.
		ecfg.IncludeGlobs = append(ecfg.IncludeGlobs, moltCfg.Include...)
		if len(ecfg.IncludeGlobs) > 0 {
			ecfg.IncludeGlobs = append(ecfg.IncludeGlobs, ".molt/**")
		}
		ecfg.ExcludeGlobs = moltCfg.Exclude
		if moltCfg.Assets != nil {
			ecfg.Assets = moltCfg.Assets.Files
			ecfg.MaxFileSizeMB = moltCfg.Assets.MaxFileSizeMB
			ecfg.MaxTotalSizeMB = moltCfg.Assets.MaxTotalSizeMB
		}
	}

	fmt.Printf("  → Embedding files (strict=%v%s)...\n",
		b.cfg.EmbedStrict,
		func() string {
			if len(ecfg.IncludeGlobs) > 0 {
				return ", allowlist mode"
			}
			return ", legacy denylist"
		}(),
	)
	res, err := embedder.Run(ecfg)
	if err != nil {
		return "", nil, fmt.Errorf("embed payload: %w", err)
	}

	info, err := os.Stat(payloadPath)
	if err != nil || info.Size() == 0 {
		return "", nil, fmt.Errorf("payload is empty")
	}
	fmt.Printf("  ✓ Payload: %.1f KB (%d files)\n",
		float64(info.Size())/1024, len(res.Files))

	return payloadPath, res, nil
}

// buildIntegrityManifest constructs the JSON audit record.
func buildIntegrityManifest(
	cfg types.BuildConfig,
	moltCfg *types.MoltConfig,
	res *embedder.Result,
) *types.IntegrityManifest {
	opts := integrity.ManifestOptions{
		MoltVersion: "dev",
	}
	if moltCfg != nil && moltCfg.Deps != nil {
		opts.Deps = &types.IntegrityDeps{
			Strategy: string(moltCfg.Deps.Strategy),
		}
	}
	return integrity.BuildManifest(
		types.IntegrityApp{Name: cfg.Name, Version: cfg.Version},
		res.Files,
		opts,
	)
}

// resolveManifestPath returns where the external integrity manifest should
// be written. Honors molt.yaml's `integrity.output` template, defaulting to
// a path alongside the binary.
func (b *Builder) resolveManifestPath(moltCfg *types.MoltConfig) string {
	outDir := filepath.Dir(b.cfg.OutputPath)
	base := fmt.Sprintf("%s-v%s.manifest.json", b.cfg.Name, b.cfg.Version)
	if moltCfg != nil && moltCfg.Integrity != nil && moltCfg.Integrity.Output != "" {
		base = moltcfg.ResolveOutputName(moltCfg)
	}
	return filepath.Join(outDir, base)
}

func integrityEnabled(moltCfg *types.MoltConfig) bool {
	if moltCfg == nil || moltCfg.Integrity == nil || moltCfg.Integrity.Enabled == nil {
		return true // default on
	}
	return *moltCfg.Integrity.Enabled
}

// reinjectIntegrityIntoPayload appends .molt/integrity.json to the tar.gz.
// This is slightly wasteful (we rewrite the archive) but gives the launcher
// offline access to the full audit manifest — useful for `molt inspect`
// on a binary that's been moved away from its sidecar file.
func reinjectIntegrityIntoPayload(payloadPath string, m *types.IntegrityManifest) error {
	// Read existing payload into memory. Payloads are typically small
	// (tens of MB); if we ever ship gigabyte payloads we'll switch to a
	// streaming rewrite, but the simpler code wins for now.
	origData, err := os.ReadFile(payloadPath)
	if err != nil {
		return err
	}

	newFile, err := os.Create(payloadPath)
	if err != nil {
		return err
	}
	defer newFile.Close()

	// Write the original entries, then our extra file, into a fresh tar.gz.
	gzr, err := gzip.NewReader(strings.NewReader(string(origData)))
	if err != nil {
		return err
	}
	defer gzr.Close()

	gzw := gzip.NewWriter(newFile)
	defer gzw.Close()
	tw := tar.NewWriter(gzw)
	defer tw.Close()

	tr := tar.NewReader(gzr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}

	// Append integrity.json under the same prefix as other .molt metadata.
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	hdr := &tar.Header{
		Name:    "src/.molt/integrity.json",
		Mode:    0o644,
		Size:    int64(len(data)),
		ModTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	return nil
}

// nativeManifestDoc mirrors the JSON shape written by syncplan's
// writeNativeManifest. Covers Cython, Rust, and external (generic) modules.
type nativeManifestDoc struct {
	Cython   map[string]nativeEntry `json:"cython"`
	Rust     map[string]nativeEntry `json:"rust"`
	External map[string]nativeEntry `json:"external"`
}

type nativeEntry struct {
	Hash     string `json:"hash"`
	Src      string `json:"src"`
	So       string `json:"so"`
	Module   string `json:"module"`
	Abi      string `json:"abi"`
	Platform string `json:"platform"`
}

// allEntries flattens all three buckets into one slice.
func (d nativeManifestDoc) allEntries() []nativeEntry {
	var out []nativeEntry
	for _, e := range d.Cython {
		out = append(out, e)
	}
	for _, e := range d.Rust {
		out = append(out, e)
	}
	for _, e := range d.External {
		out = append(out, e)
	}
	return out
}

// injectCythonArtifacts reads projstate.Dir(p)/native.json and appends each
// listed .so to the payload tar at src/<package_path>/<so_name> — exactly
// where Python's import machinery expects extension modules in the tree.
//
// Cross-build refusal: if any artefact's recorded platform/abi doesn't
// match the build target, the function errors with a clear message rather
// than ship a broken binary.
//
// No-op when native.json is absent (project has no .pyx files).
func injectCythonArtifacts(payloadPath, projectPath, targetOS, targetArch string) error {
	manifestPath := filepath.Join(projstate.Dir(projectPath), "native.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var doc nativeManifestDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse native.json: %w", err)
	}
	entries := doc.allEntries()
	if len(entries) == 0 {
		return nil
	}
	if err := refuseCrossBuild(doc, targetOS, targetArch); err != nil {
		return err
	}

	// Resolve each artefact's on-disk .so path and the tar destination.
	type pair struct{ tarPath, diskPath string }
	var pairs []pair
	stateRoot := projstate.Dir(projectPath)
	for _, e := range entries {
		// e.So is "cython/<pkg>/<file>.so" (project-view path); resolve
		// to the symlinked .so under projstate.Dir(p).
		viewPath := filepath.Join(stateRoot, filepath.FromSlash(e.So))
		// Follow the symlink to land on the real file in the cache.
		real, err := filepath.EvalSymlinks(viewPath)
		if err != nil {
			return fmt.Errorf("resolve cython artefact %s: %w", viewPath, err)
		}
		// The embedder uses PrefixInTar="src/" against RootDir = project
		// root. So a project file at <root>/src/foo/bar.py lands in the
		// tar as "src/src/foo/bar.py". The launcher strips one "src/"
		// during extraction → install/src/foo/bar.py. Match that layout
		// so cython .so files end up next to their .py siblings.
		rel := strings.TrimPrefix(e.So, "cython/")
		pairs = append(pairs, pair{tarPath: "src/src/" + rel, diskPath: real})
	}

	// Rewrite the tar in-memory adding the new entries at the end.
	origData, err := os.ReadFile(payloadPath)
	if err != nil {
		return err
	}
	out, err := os.Create(payloadPath)
	if err != nil {
		return err
	}
	defer out.Close()

	gzr, err := gzip.NewReader(strings.NewReader(string(origData)))
	if err != nil {
		return err
	}
	defer gzr.Close()
	gzw := gzip.NewWriter(out)
	defer gzw.Close()
	tw := tar.NewWriter(gzw)
	defer tw.Close()

	tr := tar.NewReader(gzr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
	for _, p := range pairs {
		body, err := os.ReadFile(p.diskPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", p.diskPath, err)
		}
		hdr := &tar.Header{
			Name:    p.tarPath,
			Mode:    0o755,
			Size:    int64(len(body)),
			ModTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(body); err != nil {
			return err
		}
	}
	return nil
}

// refuseCrossBuild fails the build when cython artefacts were compiled for
// a different OS+arch than the build target. macOS version digits in the
// PEP 425 platform tag (e.g. "macosx_14_0_arm64" vs "macosx_11_0_arm64")
// are ignored — they're the macOS deployment-target floor, not the host
// version, and don't matter for a build performed on this machine.
func refuseCrossBuild(doc nativeManifestDoc, targetOS, targetArch string) error {
	for _, e := range doc.allEntries() {
		if e.Platform == "" {
			continue
		}
		if !platCompatible(e.Platform, targetOS, targetArch) {
			return fmt.Errorf(
				"native artefact %s was compiled for %s; cannot embed in a %s/%s build "+
					"(run molt build on the target host, or remove native source files)",
				e.Module, e.Platform, targetOS, targetArch)
		}
	}
	return nil
}

// platCompatible matches a PEP 425 platform tag against a Go target os/arch.
// Examples:
//   ("macosx_14_0_arm64",  "darwin", "arm64") → true
//   ("macosx_10_12_x86_64", "darwin", "amd64") → true
//   ("macosx_14_0_arm64",  "linux",  "arm64") → false (different OS)
//   ("linux_aarch64",      "linux",  "arm64") → true
func platCompatible(plat, goOS, goArch string) bool {
	switch goOS {
	case "darwin":
		if !strings.HasPrefix(plat, "macosx_") {
			return false
		}
		return strings.HasSuffix(plat, archSuffixDarwin(goArch))
	case "linux":
		if !strings.HasPrefix(plat, "linux_") && !strings.HasPrefix(plat, "manylinux") {
			return false
		}
		return strings.Contains(plat, archSuffixLinux(goArch))
	case "windows":
		return strings.HasPrefix(plat, "win") && strings.Contains(plat, archSuffixWindows(goArch))
	}
	return false
}

func archSuffixDarwin(goArch string) string {
	if goArch == "amd64" {
		return "x86_64"
	}
	return goArch
}

func archSuffixLinux(goArch string) string {
	switch goArch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return goArch
}

func archSuffixWindows(goArch string) string {
	if goArch == "amd64" {
		return "amd64"
	}
	return goArch
}

// assembleBinary writes launcher+payload+extendedTrailer. The trailer carries
// the payload offset, the integrity root hash, and the magic bytes that tell
// the launcher this is a v1-formatted binary.
func assembleBinary(launcherPath, archivePath, outputPath, rootHash string) error {
	launcher, err := os.ReadFile(launcherPath)
	if err != nil {
		return err
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		return err
	}
	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := out.Write(launcher); err != nil {
		return err
	}
	archiveOffset := int64(len(launcher))
	if _, err := out.Write(archive); err != nil {
		return err
	}

	// Write extended trailer: offset, root_hash, magic.
	if err := integrity.WriteExtendedTrailer(out, archiveOffset, rootHash); err != nil {
		return fmt.Errorf("write trailer: %w", err)
	}

	return out.Chmod(0o755)
}

// ── Capture / Assemble (unchanged in intent — included for completeness) ─────

func Capture(cfg types.CaptureConfig) error {
	if cfg.TargetOS == "" {
		cfg.TargetOS = runtime.GOOS
	}
	if cfg.TargetArch == "" {
		cfg.TargetArch = runtime.GOARCH
	}
	fmt.Printf("Capturing environment (%s/%s)...\n", cfg.TargetOS, cfg.TargetArch)
	m := &types.Manifest{
		AppName: "__placeholder__", Version: "__placeholder__",
		MainModule: "__placeholder__", TargetOS: cfg.TargetOS, TargetArch: cfg.TargetArch,
		BuildTime: time.Now().UTC().Format(time.RFC3339),
	}
	outPath := cfg.OutputPath
	if outPath == "" {
		outPath = fmt.Sprintf("%s-%s.manifest.json", runtime.GOOS, runtime.GOARCH)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("  Manifest: %s\n", outPath)
	return nil
}

type Assembler struct{ cfg types.AssembleConfig }

func NewAssembler(cfg types.AssembleConfig) *Assembler {
	if cfg.TargetOS == "" {
		cfg.TargetOS = runtime.GOOS
	}
	if cfg.TargetArch == "" {
		cfg.TargetArch = runtime.GOARCH
	}
	return &Assembler{cfg: cfg}
}

func (a *Assembler) Assemble(name, version string) error {
	manifestData, err := os.ReadFile(a.cfg.ManifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var m types.Manifest
	if err := json.Unmarshal(manifestData, &m); err != nil {
		return err
	}
	m.AppName, m.Version, m.MainModule, m.Profile = name, version, name+".main", a.cfg.Profile

	launcherPath, err := compileLauncher(a.cfg.TargetOS, a.cfg.TargetArch)
	if err != nil {
		return err
	}
	defer os.Remove(launcherPath)

	payloadPath, err := a.createPayload(&m)
	if err != nil {
		return err
	}
	defer os.Remove(payloadPath)

	suffix := ""
	if a.cfg.TargetOS == "windows" {
		suffix = ".exe"
	}
	// Empty payload → empty-root hash; still use extended trailer for
	// consistency with `build`.
	return assembleBinary(launcherPath, payloadPath,
		a.cfg.OutputPath+suffix, integrity.ComputeRootHash(nil))
}

func (a *Assembler) createPayload(m *types.Manifest) (string, error) {
	tmp, err := os.CreateTemp("", "molt-payload-*.tar.gz")
	if err != nil {
		return "", err
	}
	tmp.Close()

	manifestData, _ := json.MarshalIndent(m, "", "  ")
	gz := gzip.NewWriter(mustOpenWrite(tmp.Name()))
	tw := tar.NewWriter(gz)
	_ = addToTar(tw, ".molt/manifest.json", manifestData)
	tw.Close()
	gz.Close()
	return tmp.Name(), nil
}

func mustOpenWrite(path string) *os.File {
	f, _ := os.Create(path)
	return f
}

// ── Launcher compilation (unchanged) ─────────────────────────────────────────

func compileLauncher(targetOS, targetArch string) (string, error) {
	if err := installGOIfNotInstalled(); err != nil {
		return "", fmt.Errorf("bootstrap go: %w", err)
	}

	suffix := ""
	if targetOS == "windows" {
		suffix = ".exe"
	}
	tmp, err := os.CreateTemp("", "molt-launcher-*"+suffix)
	if err != nil {
		return "", err
	}
	tmp.Close()

	goBin := filepath.Join(getGOOutputDir(), "bin", "go")
	if runtime.GOOS == "windows" {
		goBin += ".exe"
	}

	home, _ := os.UserHomeDir()
	if home == "" {
		home = os.TempDir()
	}
	goCache := filepath.Join(home, ".cache", "go-build")
	goPath := filepath.Join(home, "go")
	os.MkdirAll(goCache, 0o755)
	os.MkdirAll(goPath, 0o755)

	// Materialise the embedded launcher source into a temp module, then build
	// from there. We can't rely on a `molt/internal/launcher` import path
	// existing on the user's GOPATH — molt is shipped as a single binary, not
	// a Go source tree.
	srcDir, err := materialiseLauncherSrc()
	if err != nil {
		return "", fmt.Errorf("stage launcher source: %w", err)
	}
	defer os.RemoveAll(srcDir)

	cmd := exec.Command(goBin, "build", "-o", tmp.Name(), ".")
	cmd.Dir = srcDir
	cmd.Env = []string{
		"GOOS=" + targetOS,
		"GOARCH=" + targetArch,
		"CGO_ENABLED=0",
		"HOME=" + home,
		"GOCACHE=" + goCache,
		"GOPATH=" + goPath,
		"GO111MODULE=on",
	}
	for _, key := range []string{"PATH", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "TMPDIR", "USER", "LOGNAME"} {
		if v := os.Getenv(key); v != "" {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go build launcher: %w\n%s", err, out)
	}
	fmt.Printf("  Compiled launcher (%s/%s)\n", targetOS, targetArch)
	return tmp.Name(), nil
}

// materialiseLauncherSrc unpacks the embedded launcher source into a fresh
// temp directory together with a minimal go.mod, returning the directory
// path. Caller is responsible for removing the directory.
func materialiseLauncherSrc() (string, error) {
	dir, err := os.MkdirTemp("", "molt-launcher-src-")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(launcherGoMod), 0o644); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	entries, err := launcherSrc.ReadDir("launcher_src")
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// strip the .tpl suffix used to keep Go tooling from compiling the
		// embedded source as part of the host module.
		name := e.Name()
		if !strings.HasSuffix(name, ".go.tpl") {
			continue
		}
		out := name[:len(name)-len(".tpl")]
		data, err := launcherSrc.ReadFile("launcher_src/" + name)
		if err != nil {
			os.RemoveAll(dir)
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, out), data, 0o644); err != nil {
			os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

func addToTar(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(data)),
	}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// AssembleLegacy is a stub kept to maintain compatibility with the older
// binary.Write-based trailer. Callers should prefer assembleBinary().
func writeLegacyTrailer(out *os.File, archiveOffset int64) error {
	return binary.Write(out, binary.LittleEndian, archiveOffset)
}

// ── Go toolchain bootstrap (unchanged) ────────────────────────────────────────

const goVersion = "1.24.1"

func getGOOutputDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".molt", "go", goVersion)
}

func installGOIfNotInstalled() error {
	goRoot := getGOOutputDir()
	binGo := filepath.Join(goRoot, "bin", "go")
	if runtime.GOOS == "windows" {
		binGo += ".exe"
	}
	if _, err := os.Stat(binGo); err == nil {
		return nil
	}
	fmt.Printf("  Bootstrapping Go %s...\n", goVersion)
	return downloadAndExtractGo(goVersion, goRoot, runtime.GOOS, runtime.GOARCH)
}

func downloadAndExtractGo(version, destDir, hostOS, hostArch string) error {
	platform := fmt.Sprintf("%s-%s", hostOS, hostArch)
	url := fmt.Sprintf("https://go.dev/dl/go%s.%s.tar.gz", version, platform)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(hdr.Name, "go/")
		if rel == "" {
			continue
		}
		outPath := filepath.Join(destDir, rel)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(outPath, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return err
			}
			os.Remove(outPath)
			if err := os.Symlink(hdr.Linkname, outPath); err != nil {
				return err
			}
		}
	}
	return nil
}
