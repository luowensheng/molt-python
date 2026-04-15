package builder

import (
	"archive/tar"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"pyexec/internal/capturer"
	"pyexec/internal/platform"
	"pyexec/internal/uv"
	"pyexec/pkg/types"
)

// crossBuildError is returned when a cross-build is attempted without --best-effort.
type crossBuildError struct {
	buildOS   string
	buildArch string
	targetOS  string
	targetArch string
}

func (e *crossBuildError) Error() string {
	return fmt.Sprintf(`cross-build detected (build: %s/%s → target: %s/%s)

Cross-building requires capturing the target environment on the target machine.
The system library snapshot and package wheel hashes will be incorrect if captured
on the wrong platform.

Options:

  1. Capture on the target machine, assemble here (recommended):

       [%s]$ pyexec capture --output %s.manifest.json .
       [%s]$  pyexec assemble --manifest %s.manifest.json \
                  --os %s --arch %s .

  2. Build natively in CI on each platform (best for production):

       # GitHub Actions / GitLab CI — use a matrix across OS runners.
       # See USAGE.md for a complete example.

  3. Build a best-effort binary (INACCURATE system deps — for testing only):

       pyexec build --os %s --arch %s --best-effort .
`,
		e.buildOS, e.buildArch, e.targetOS, e.targetArch,
		e.targetOS, e.targetOS,
		e.buildOS, e.targetOS,
		e.targetOS, e.targetArch,
		e.targetOS, e.targetArch,
	)
}

// ── Builder ───────────────────────────────────────────────────────────────────

// Builder produces a self-contained PyExec binary.
type Builder struct {
	cfg types.BuildConfig
	cap *capturer.Capturer
}

// New creates a Builder.
func New(cfg types.BuildConfig) *Builder {
	if cfg.TargetOS == "" {
		cfg.TargetOS = runtime.GOOS
	}
	if cfg.TargetArch == "" {
		cfg.TargetArch = runtime.GOARCH
	}
	return &Builder{
		cfg: cfg,
		cap: capturer.NewCross(true, cfg.TargetOS, cfg.TargetArch),
	}
}

// Build runs the full build pipeline.
// It errors on cross-builds unless CrossBuildMode is BestEffort.
func (b *Builder) Build() error {
	isCross := b.cfg.TargetOS != runtime.GOOS || b.cfg.TargetArch != runtime.GOARCH

	if isCross && b.cfg.CrossBuildMode != types.CrossBuildBestEffort {
		return &crossBuildError{
			buildOS:    runtime.GOOS,
			buildArch:  runtime.GOARCH,
			targetOS:   b.cfg.TargetOS,
			targetArch: b.cfg.TargetArch,
		}
	}

	if isCross {
		fmt.Printf("⚠  WARNING: best-effort cross-build (%s/%s → %s/%s)\n",
			runtime.GOOS, runtime.GOARCH, b.cfg.TargetOS, b.cfg.TargetArch)
		fmt.Println("   System dependency snapshot and wheel hashes will be inaccurate.")
		fmt.Println("   Do not use --profile full/extended with best-effort cross-builds.")
		fmt.Println()
	}

	fmt.Printf("Building %s v%s (%s/%s)...\n",
		b.cfg.Name, b.cfg.Version, b.cfg.TargetOS, b.cfg.TargetArch)

	// Phase 0: ensure uv.lock is present and up to date.
	if err := ensureLock(b.cfg.ProjectPath); err != nil {
		return err
	}

	// Phase 1: capture environment.
	snap, err := b.cap.Capture(b.cfg.ProjectPath)
	if err != nil {
		return fmt.Errorf("environment capture: %w", err)
	}
	fmt.Printf("  ✓ Captured Python %s\n", snap.Python.Version)
	fmt.Printf("  ✓ Captured %d system dependencies\n", len(snap.SystemDeps))
	fmt.Printf("  ✓ Captured %d Python packages\n", len(snap.PyPackages))

	// Phase 2: generate manifest.
	m := makeManifest(b.cfg.Name, b.cfg.Version, b.cfg.Profile, b.cfg.TargetOS, b.cfg.TargetArch, snap)

	// Phase 3: embed based on profile.
	applyProfile(m, b.cfg.Profile)

	// Phase 4–6: assemble binary.
	return assemble(types.AssembleConfig{
		ManifestPath: "", // inline — manifest passed directly
		ProjectPath:  b.cfg.ProjectPath,
		OutputPath:   b.cfg.OutputPath,
		TargetOS:     b.cfg.TargetOS,
		TargetArch:   b.cfg.TargetArch,
		Profile:      b.cfg.Profile,
		EmbedFiles:   b.cfg.EmbedFiles,
	}, m, snap)
}

// ── Capture ───────────────────────────────────────────────────────────────────

// Capture runs only the environment snapshot step and writes a manifest JSON file.
// This must be run on the target platform.
func Capture(cfg types.CaptureConfig) error {
	if cfg.TargetOS == "" {
		cfg.TargetOS = runtime.GOOS
	}
	if cfg.TargetArch == "" {
		cfg.TargetArch = runtime.GOARCH
	}

	// Warn if capturing for a different platform than the current one —
	// that defeats the purpose.
	if cfg.TargetOS != runtime.GOOS || cfg.TargetArch != runtime.GOARCH {
		fmt.Printf("⚠  WARNING: capturing on %s/%s but --os=%s --arch=%s\n",
			runtime.GOOS, runtime.GOARCH, cfg.TargetOS, cfg.TargetArch)
		fmt.Println("   For accurate capture, run this command on the target machine.")
	}

	fmt.Printf("Capturing environment (%s/%s)...\n", runtime.GOOS, runtime.GOARCH)

	if err := ensureLock(cfg.ProjectPath); err != nil {
		return err
	}

	cap := capturer.NewCross(true, cfg.TargetOS, cfg.TargetArch)
	snap, err := cap.Capture(cfg.ProjectPath)
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}

	// We don't know name/version at capture time — leave as placeholders.
	// assemble will fill them in.
	m := &types.Manifest{
		AppName:    "__placeholder__",
		Version:    "__placeholder__",
		MainModule: "__placeholder__",
		Python:     snap.Python,
		SystemDeps: snap.SystemDeps,
		PyPackages: snap.PyPackages,
		Profile:    types.ProfileMinimal,
		TargetOS:   cfg.TargetOS,
		TargetArch: cfg.TargetArch,
		BuildTime:  time.Now().UTC().Format(time.RFC3339),
		// Embed source snapshot for assemble step.
	}

	// Write manifest JSON.
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

	// Also write the source snapshot alongside it for the assemble step.
	snapPath := outPath + ".snap"
	snapData, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(snapPath, snapData, 0o644); err != nil {
		return err
	}

	fmt.Printf("  ✓ Captured Python %s\n", snap.Python.Version)
	fmt.Printf("  ✓ Captured %d system dependencies\n", len(snap.SystemDeps))
	fmt.Printf("  ✓ Captured %d Python packages\n", len(snap.PyPackages))
	fmt.Printf("\nManifest written to: %s\n", outPath)
	fmt.Printf("Source snapshot:     %s\n", snapPath)
	fmt.Printf("\nOn your build machine, run:\n")
	fmt.Printf("  pyexec assemble --manifest %s --os %s --arch %s .\n",
		outPath, cfg.TargetOS, cfg.TargetArch)
	return nil
}

// ── Assemble ──────────────────────────────────────────────────────────────────

// Assembler builds a binary from a pre-captured manifest (no capture step).
type Assembler struct {
	cfg types.AssembleConfig
}

// NewAssembler creates an Assembler.
func NewAssembler(cfg types.AssembleConfig) *Assembler {
	if cfg.TargetOS == "" {
		cfg.TargetOS = runtime.GOOS
	}
	if cfg.TargetArch == "" {
		cfg.TargetArch = runtime.GOARCH
	}
	return &Assembler{cfg: cfg}
}

// Assemble builds a binary from an existing manifest file.
// This can run on any platform — no capture step, no platform requirement.
func (a *Assembler) Assemble(name, version string) error {
	// Load the manifest written by `pyexec capture`.
	manifestData, err := os.ReadFile(a.cfg.ManifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var m types.Manifest
	if err := json.Unmarshal(manifestData, &m); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}

	// Fill in name/version/module (placeholders from capture step).
	m.AppName = name
	m.Version = version
	m.MainModule = name + ".main"
	m.Profile = a.cfg.Profile

	// Load the source snapshot if it exists alongside the manifest.
	snap := &types.Snapshot{
		Python:     m.Python,
		SystemDeps: m.SystemDeps,
		PyPackages: m.PyPackages,
	}
	snapPath := a.cfg.ManifestPath + ".snap"
	if data, err := os.ReadFile(snapPath); err == nil {
		var fullSnap types.Snapshot
		if err := json.Unmarshal(data, &fullSnap); err == nil {
			snap = &fullSnap
		}
	} else if a.cfg.ProjectPath != "" {
		// Fall back to reading source from the project path directly.
		cap := capturer.NewCross(false, a.cfg.TargetOS, a.cfg.TargetArch)
		if src, err := cap.CaptureSourceOnly(a.cfg.ProjectPath); err == nil {
			snap.Source = src
		}
	}

	applyProfile(&m, a.cfg.Profile)

	outputPath := a.cfg.OutputPath
	if outputPath == "" {
		outputPath = fmt.Sprintf("%s-v%s", name, version)
	}

	fmt.Printf("Assembling %s v%s (%s/%s) from manifest...\n",
		name, version, a.cfg.TargetOS, a.cfg.TargetArch)

	return assemble(a.cfg, &m, snap)
}

// ── Shared assembly pipeline ──────────────────────────────────────────────────

func assemble(cfg types.AssembleConfig, m *types.Manifest, snap *types.Snapshot) error {
	// Cross-compile the launcher.
	launcherPath, err := compileLauncher(cfg.TargetOS, cfg.TargetArch)
	if err != nil {
		return fmt.Errorf("compile launcher: %w", err)
	}
	defer os.Remove(launcherPath)

	// Build the payload archive.
	archivePath, err := createArchive(m, snap, cfg.EmbedFiles)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer os.Remove(archivePath)

	// Concatenate: launcher | archive | 8-byte-LE offset trailer.
	outputPath := cfg.OutputPath + platform.ExeSuffix(cfg.TargetOS)
	if err := assembleBinary(launcherPath, archivePath, outputPath); err != nil {
		return fmt.Errorf("assemble binary: %w", err)
	}

	info, _ := os.Stat(outputPath)
	sizeMB := float64(0)
	if info != nil {
		sizeMB = float64(info.Size()) / 1_000_000
	}
	fmt.Printf("Created: %s (%.0fMB)\n", outputPath, sizeMB)
	return nil
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func ensureLock(projectPath string) error {
	lockPath := filepath.Join(projectPath, "uv.lock")
	if !uv.Available() {
		if _, err := os.Stat(lockPath); os.IsNotExist(err) {
			return fmt.Errorf(
				"uv.lock not found and uv is not installed.\n" +
					"Install uv (https://docs.astral.sh/uv/) or provide a uv.lock file.")
		}
		fmt.Println("  ⚠ uv not found — using existing uv.lock")
		return nil
	}
	ran, err := uv.LockIfStale(projectPath)
	if err != nil {
		return fmt.Errorf("uv lock: %w", err)
	}
	if ran {
		fmt.Println("  ✓ uv.lock updated")
	} else {
		fmt.Println("  ✓ uv.lock up to date")
	}
	return nil
}

func makeManifest(name, version string, profile types.BuildProfile, targetOS, targetArch string, snap *types.Snapshot) *types.Manifest {
	return &types.Manifest{
		AppName:    name,
		Version:    version,
		MainModule: name + ".main",
		Python:     snap.Python,
		SystemDeps: snap.SystemDeps,
		PyPackages: snap.PyPackages,
		Profile:    profile,
		TargetOS:   targetOS,
		TargetArch: targetArch,
		BuildTime:  time.Now().UTC().Format(time.RFC3339),
	}
}

func applyProfile(m *types.Manifest, profile types.BuildProfile) {
	switch profile {
	case types.ProfileMinimal:
		// nothing embedded
	case types.ProfileStandard:
		m.Python.Embedded = true
	case types.ProfileExtended:
		m.Python.Embedded = true
		for i := range m.SystemDeps {
			if isCommonLib(m.SystemDeps[i].Name) {
				m.SystemDeps[i].Embedded = true
			}
		}
	case types.ProfileFull:
		m.Python.Embedded = true
		for i := range m.SystemDeps {
			m.SystemDeps[i].Embedded = true
		}
		for i := range m.PyPackages {
			m.PyPackages[i].Embedded = true
		}
	}
}

func isCommonLib(name string) bool {
	common := []string{"libssl", "libcrypto", "libsqlite3", "libz", "libffi", "libm", "libc"}
	for _, c := range common {
		if len(name) >= len(c) && name[:len(c)] == c {
			return true
		}
	}
	return false
}

func compileLauncher(targetOS, targetArch string) (string, error) {
	tmp, err := os.CreateTemp("", "pyexec-launcher-*"+platform.ExeSuffix(targetOS))
	if err != nil {
		return "", err
	}
	tmp.Close()

	if !isGoAvailable() {
		stub := []byte(fmt.Sprintf("#!/bin/sh\necho 'PyExec: Go required to build launcher'\nexit 1\n"))
		if err := os.WriteFile(tmp.Name(), stub, 0o755); err != nil {
			return "", err
		}
		fmt.Println("  ⚠ go not found — using stub launcher (install Go for real binaries)")
		return tmp.Name(), nil
	}

	launcherPkg := "pyexec/internal/launcher"
	cmd := exec.Command("go", "build", "-o", tmp.Name(), launcherPkg)
	cmd.Env = append(os.Environ(),
		"GOOS="+targetOS,
		"GOARCH="+targetArch,
		"CGO_ENABLED=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go build launcher (%s/%s): %w\n%s", targetOS, targetArch, err, string(out))
	}
	fmt.Printf("  ✓ Compiled launcher (%s/%s)\n", targetOS, targetArch)
	return tmp.Name(), nil
}

func createArchive(m *types.Manifest, snap *types.Snapshot, embedFiles []string) (string, error) {
	tmp, err := os.CreateTemp("", "pyexec-archive-*.tar.gz")
	if err != nil {
		return "", err
	}
	defer tmp.Close()

	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)

	manifestData, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	if err := addToTar(tw, ".pyexec/manifest.json", manifestData); err != nil {
		return "", err
	}
	for _, sf := range snap.Source {
		if err := addToTar(tw, "src/"+sf.RelPath, sf.Data); err != nil {
			return "", err
		}
	}
	for _, ef := range embedFiles {
		data, err := os.ReadFile(ef)
		if err != nil {
			return "", fmt.Errorf("embed file %s: %w", ef, err)
		}
		if err := addToTar(tw, "extras/"+filepath.Base(ef), data); err != nil {
			return "", err
		}
	}

	tw.Close()
	gz.Close()
	return tmp.Name(), nil
}

func assembleBinary(launcherPath, archivePath, outputPath string) error {
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
	if err := binary.Write(out, binary.LittleEndian, archiveOffset); err != nil {
		return err
	}
	return out.Chmod(0o755)
}

func isGoAvailable() bool {
	_, err := exec.LookPath("go")
	return err == nil
}

func addToTar(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}
