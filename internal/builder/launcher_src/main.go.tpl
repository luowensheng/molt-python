// Command launcher is the target-side runner embedded into every molt binary.
//
// Responsibilities:
//   - install: unpack payload, set up Python venv, install deps, run post_install hooks
//   - run:     execute a named command from molt.yaml (or legacy -m fallback)
//   - verify:  recompute root hash over extracted payload, compare to trailer
//   - uninstall / info / version
//
// Self-contained: no imports from the rest of the molt codebase (builder /
// deps / etc.) because this binary ships alone. The small bits of logic
// shared with the main molt CLI (trailer parsing, manifest shapes) are
// re-implemented here rather than pulling in the integrity package, to
// keep the launcher binary tiny.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ── Types mirroring pkg/types (kept in sync manually) ────────────────────────

type Manifest struct {
	AppName            string      `json:"app_name"`
	Version            string      `json:"version"`
	MainModule         string      `json:"main_module"`
	Python             PythonSpec  `json:"python"`
	SystemDeps         []SystemDep `json:"system_deps"`
	PyPackages         []PyPackage `json:"py_packages"`
	Profile            string      `json:"profile"`
	MoltConfigSnapshot *MoltConfig `json:"molt_config,omitempty"`
}

type PythonSpec struct {
	Version  string `json:"version"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Embedded bool   `json:"embedded"`
}

type SystemDep struct {
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	Embedded bool   `json:"embedded"`
}

type PyPackage struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Embedded bool   `json:"embedded"`
}

// MoltConfig mirrors types.MoltConfig — only the subset the launcher uses
// at runtime (commands, env, hooks, integrity policy, deps strategy).
type MoltConfig struct {
	Version        int                    `json:"version"`
	Project        MoltProject            `json:"project"`
	Deps           *MoltDeps              `json:"deps,omitempty"`
	Commands       map[string]MoltCommand `json:"commands,omitempty"`
	Env            map[string]string      `json:"env,omitempty"`
	Hooks          MoltHooks              `json:"hooks,omitempty"`
	Integrity      *MoltIntegrity         `json:"integrity,omitempty"`
	DefaultCommand string                 `json:"default_command,omitempty"`
}

type MoltProject struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Python  string `json:"python,omitempty"`
}

type MoltDeps struct {
	Strategy  string   `json:"strategy"`
	Files     []string `json:"files,omitempty"`
	ExtraArgs []string `json:"extra_args,omitempty"`
}

type MoltCommand struct {
	Exec        []string          `json:"exec,omitempty"`
	Script      string            `json:"script,omitempty"`
	Description string            `json:"description,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Dir         string            `json:"dir,omitempty"`
}

type MoltHooks struct {
	PreInstall  []string `json:"pre_install,omitempty"`
	PostInstall []string `json:"post_install,omitempty"`
}

type MoltIntegrity struct {
	Enabled         *bool  `json:"enabled,omitempty"`
	VerifyOnLaunch  bool   `json:"verify_on_launch,omitempty"`
	VerifyOnInstall bool   `json:"verify_on_install,omitempty"`
	Algorithm       string `json:"algorithm,omitempty"`
}

// IntegrityManifest mirrors types.IntegrityManifest (payload-only subset).
type IntegrityManifest struct {
	Schema    string          `json:"schema"`
	App       ManifestApp     `json:"app"`
	RootHash  string          `json:"root_hash"`
	Algorithm string          `json:"algorithm"`
	Payload   ManifestPayload `json:"payload"`
}

type ManifestApp struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ManifestPayload struct {
	TotalFiles int64          `json:"total_files"`
	TotalBytes int64          `json:"total_bytes"`
	Files      []PackagedFile `json:"files"`
}

type PackagedFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Source string `json:"source"`
}

// ── Trailer constants (must match pkg/types) ─────────────────────────────────

const (
	TrailerMagic      = "MOLT0001"
	TrailerV1Size     = 48
	LegacyTrailerSize = 8
	RootHashBytes     = 32
)

// ── Entry ────────────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "install":
		err = cmdInstall(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "uninstall":
		err = cmdUninstall(os.Args[2:])
	case "info":
		err = cmdInfo()
	case "version", "--version", "-v":
		err = cmdVersion()
	case "help", "--help", "-h":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	self := filepath.Base(os.Args[0])
	fmt.Fprintf(os.Stderr, `Usage: %s <command> [args...]

Commands:
  install [--prefix DIR] [--offline] [--verbose] [--no-verify]
            Extract payload and set up hermetic environment.

  run [COMMAND] [args...]
            Execute COMMAND (from molt.yaml). Without COMMAND, run the
            default command; without a default, fall back to "python -m
            <app>.main".

  verify    Recompute payload root hash and compare to trailer.
  uninstall Remove the installation.
  info      Print install metadata.
  version   Print app name and version.

Environment variables:
  MOLT_INSTALL_BASE   Base directory ($BASE/<app>/<version>/ install dir).
  MOLT_INSTALL_DIR    Full install dir (overrides MOLT_INSTALL_BASE).
  MOLT_CACHE_DIR      Cache for downloaded artefacts.
  MOLT_SKIP_VERIFY    "1" to skip the install-time integrity check.

`, self)
}

// ── Install ──────────────────────────────────────────────────────────────────

func cmdInstall(args []string) error {
	verbose := hasFlag(args, "--verbose", "-v")
	offline := hasFlag(args, "--offline")
	skipVerify := hasFlag(args, "--no-verify") || os.Getenv("MOLT_SKIP_VERIFY") == "1"

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}

	trailer, err := readTrailer(self)
	if err != nil {
		return fmt.Errorf("read trailer: %w", err)
	}

	m, err := readManifest(self, trailer.PayloadOffset)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	installDir := flagValue(args, "--prefix", "")
	if installDir == "" {
		installDir = resolveInstallDir(m.AppName, m.Version)
	}
	installDir, _ = filepath.Abs(installDir)

	step("Installing %s v%s → %s", m.AppName, m.Version, installDir)
	logv(verbose, "offline=%v  skipVerify=%v  has_integrity=%v",
		offline, skipVerify, trailer.HasIntegrity)

	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("create install dir: %w", err)
	}

	// 1. Extract payload.
	step("Extracting payload...")
	if err := extractPayload(self, trailer.PayloadOffset, installDir, verbose); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// 2. Install-time integrity check. This runs BEFORE we execute any
	// hooks so a tampered binary can't induce hook execution.
	shouldVerify := !skipVerify && trailer.HasIntegrity && shouldVerifyOnInstall(m)
	if shouldVerify {
		step("Verifying integrity...")
		if err := verifyExtracted(installDir, trailer.RootHash); err != nil {
			// Remove the extracted tree so a tampered payload doesn't linger.
			os.RemoveAll(installDir)
			return fmt.Errorf("integrity check failed (install aborted): %w", err)
		}
		logv(verbose, "✓ payload matches trailer root_hash")
	} else if !trailer.HasIntegrity {
		fmt.Fprintln(os.Stderr, "  ⚠ Legacy binary without integrity — skipping check")
	}

	// 3. Set up Python (download + venv).
	if err := installPython(installDir, m, offline, verbose); err != nil {
		return fmt.Errorf("install python: %w", err)
	}
	if err := createVenv(installDir, verbose); err != nil {
		return fmt.Errorf("create venv: %w", err)
	}

	// 4. Install dependencies via the chosen strategy.
	if !offline {
		if err := installDependencies(installDir, m, verbose); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ dep install failed: %v\n", err)
		}
	}

	// 5. Hooks.
	if m.MoltConfigSnapshot != nil {
		for _, h := range m.MoltConfigSnapshot.Hooks.PostInstall {
			fmt.Printf("  [hook] %s\n", h)
			if err := runShell(h, installDir, buildExecEnv(installDir, m, nil)); err != nil {
				return fmt.Errorf("post_install hook %q failed: %w", h, err)
			}
		}
	}

	writeReceipt(installDir, m)
	fmt.Printf("\n✓ Installed to %s\n", installDir)
	fmt.Printf("  Run:       %s run\n", filepath.Base(os.Args[0]))
	fmt.Printf("  Uninstall: %s uninstall\n", filepath.Base(os.Args[0]))
	return nil
}

// shouldVerifyOnInstall consults the embedded MoltConfig. Defaults to TRUE
// if the config is absent — free-by-default authentication.
func shouldVerifyOnInstall(m *Manifest) bool {
	if m.MoltConfigSnapshot == nil || m.MoltConfigSnapshot.Integrity == nil {
		return true
	}
	i := m.MoltConfigSnapshot.Integrity
	if i.Enabled != nil && !*i.Enabled {
		return false
	}
	return i.VerifyOnInstall
}

// shouldVerifyOnLaunch is the same but for every `run` invocation.
func shouldVerifyOnLaunch(m *Manifest) bool {
	if m.MoltConfigSnapshot == nil || m.MoltConfigSnapshot.Integrity == nil {
		return false
	}
	i := m.MoltConfigSnapshot.Integrity
	if i.Enabled != nil && !*i.Enabled {
		return false
	}
	return i.VerifyOnLaunch
}

// ── Run ──────────────────────────────────────────────────────────────────────

func cmdRun(args []string) error {
	verbose := hasFlag(args, "--verbose", "-v")
	args = filterFlags(args, "--verbose", "-v")

	self, err := os.Executable()
	if err != nil {
		return err
	}
	trailer, err := readTrailer(self)
	if err != nil {
		return err
	}
	m, err := readManifest(self, trailer.PayloadOffset)
	if err != nil {
		return err
	}

	installDir := resolveInstallDir(m.AppName, m.Version)
	installDir, _ = filepath.Abs(installDir)
	if _, err := os.Stat(filepath.Join(installDir, ".molt", "receipt.json")); err != nil {
		return fmt.Errorf("not installed at %s — run %q first",
			installDir, filepath.Base(os.Args[0])+" install")
	}

	// Optional launch-time integrity check.
	if trailer.HasIntegrity && shouldVerifyOnLaunch(m) {
		if err := verifyExtracted(installDir, trailer.RootHash); err != nil {
			return fmt.Errorf("launch-time integrity check failed: %w", err)
		}
		logv(verbose, "✓ launch-time integrity OK")
	}

	// Resolve the command to run.
	var cmdName string
	var cmdArgs []string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		// First positional arg may be a command name from molt.yaml.
		if m.MoltConfigSnapshot != nil {
			if _, ok := m.MoltConfigSnapshot.Commands[args[0]]; ok {
				cmdName = args[0]
				cmdArgs = args[1:]
			}
		}
	}
	if cmdName == "" && m.MoltConfigSnapshot != nil {
		cmdName = resolveDefaultCommand(m.MoltConfigSnapshot)
		cmdArgs = args
	}

	if cmdName != "" {
		return executeCommand(m, installDir, cmdName, cmdArgs, verbose)
	}

	// Legacy fallback: `python -m <app>.main`.
	logv(verbose, "no command defined — falling back to 'python -m %s'", m.MainModule)
	return executeLegacyMain(m, installDir, args, verbose)
}

func resolveDefaultCommand(cfg *MoltConfig) string {
	if cfg.DefaultCommand != "" {
		if _, ok := cfg.Commands[cfg.DefaultCommand]; ok {
			return cfg.DefaultCommand
		}
	}
	for _, try := range []string{"run", "start"} {
		if _, ok := cfg.Commands[try]; ok {
			return try
		}
	}
	if len(cfg.Commands) == 1 {
		for n := range cfg.Commands {
			return n
		}
	}
	return ""
}

// executeCommand runs a named command from molt.yaml in the installed env.
func executeCommand(m *Manifest, installDir, name string, extraArgs []string, verbose bool) error {
	cmd := m.MoltConfigSnapshot.Commands[name]
	env := buildExecEnv(installDir, m, cmd.Env)

	workDir := installDir
	if cmd.Dir != "" {
		workDir = filepath.Join(installDir, cmd.Dir)
	}

	switch {
	case len(cmd.Exec) > 0:
		argv := append([]string{}, cmd.Exec...)
		argv = append(argv, extraArgs...)
		// Resolve argv[0] against venv/bin first so users can say `exec: [pytest, ...]`
		// without full paths.
		resolved := resolveInVenv(installDir, argv[0])
		if resolved != "" {
			argv[0] = resolved
		}
		logv(verbose, "exec: %s (cwd=%s)", strings.Join(argv, " "), workDir)
		c := exec.Command(argv[0], argv[1:]...)
		c.Dir = workDir
		c.Env = env
		c.Stdin = os.Stdin
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		return c.Run()

	case cmd.Script != "":
		full := cmd.Script
		if len(extraArgs) > 0 {
			full += " " + strings.Join(extraArgs, " ")
		}
		logv(verbose, "script: %s (cwd=%s)", full, workDir)
		return runShell(full, workDir, env)
	}
	return fmt.Errorf("command %q has neither exec nor script", name)
}

// executeLegacyMain replicates the pre-molt.yaml behaviour: `python -m <app>.main`.
func executeLegacyMain(m *Manifest, installDir string, args []string, verbose bool) error {
	pythonBin := findPython(installDir)
	if pythonBin == "" {
		return fmt.Errorf("python not found in %s", installDir)
	}
	mainModule := m.MainModule
	if mainModule == "" {
		mainModule = m.AppName + ".main"
	}
	cmdArgs := append([]string{"-m", mainModule}, args...)
	c := exec.Command(pythonBin, cmdArgs...)
	c.Dir = installDir
	c.Env = buildExecEnv(installDir, m, nil)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	logv(verbose, "legacy exec: %s %s", pythonBin, strings.Join(cmdArgs, " "))
	applyIsolation(c)
	return c.Run()
}

// ── Verify / info / uninstall ────────────────────────────────────────────────

func cmdVerify(args []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	trailer, err := readTrailer(self)
	if err != nil {
		return err
	}
	if !trailer.HasIntegrity {
		return fmt.Errorf("binary has no integrity trailer (legacy build)")
	}
	m, err := readManifest(self, trailer.PayloadOffset)
	if err != nil {
		return err
	}
	installDir := flagValue(args, "--prefix", "")
	if installDir == "" {
		installDir = resolveInstallDir(m.AppName, m.Version)
	}
	installDir, _ = filepath.Abs(installDir)
	if _, err := os.Stat(installDir); err != nil {
		return fmt.Errorf("not installed at %s", installDir)
	}
	if err := verifyExtracted(installDir, trailer.RootHash); err != nil {
		return err
	}
	fmt.Println("✓ Integrity verified")
	return nil
}

func cmdInfo() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	trailer, err := readTrailer(self)
	if err != nil {
		return err
	}
	m, err := readManifest(self, trailer.PayloadOffset)
	if err != nil {
		return err
	}
	installDir := resolveInstallDir(m.AppName, m.Version)
	installed := "no"
	if _, err := os.Stat(filepath.Join(installDir, ".molt", "receipt.json")); err == nil {
		installed = "yes"
	}
	fmt.Printf("App:        %s v%s\n", m.AppName, m.Version)
	fmt.Printf("Python:     %s\n", m.Python.Version)
	fmt.Printf("Integrity:  %v\n", trailer.HasIntegrity)
	if trailer.HasIntegrity {
		fmt.Printf("Root hash:  %s\n", hex.EncodeToString(trailer.RootHash[:]))
	}
	fmt.Printf("Installed:  %s (%s)\n", installed, installDir)
	if m.MoltConfigSnapshot != nil && len(m.MoltConfigSnapshot.Commands) > 0 {
		fmt.Println()
		fmt.Println("Commands:")
		names := make([]string, 0, len(m.MoltConfigSnapshot.Commands))
		for n := range m.MoltConfigSnapshot.Commands {
			names = append(names, n)
		}
		sort.Strings(names)
		def := resolveDefaultCommand(m.MoltConfigSnapshot)
		for _, n := range names {
			marker := "  "
			if n == def {
				marker = "* "
			}
			c := m.MoltConfigSnapshot.Commands[n]
			desc := c.Description
			if desc == "" {
				if len(c.Exec) > 0 {
					desc = strings.Join(c.Exec, " ")
				} else {
					desc = c.Script
				}
			}
			fmt.Printf("  %s%-15s %s\n", marker, n, desc)
		}
	}
	return nil
}

func cmdUninstall(args []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	trailer, err := readTrailer(self)
	if err != nil {
		return err
	}
	m, err := readManifest(self, trailer.PayloadOffset)
	if err != nil {
		return err
	}
	installDir := resolveInstallDir(m.AppName, m.Version)
	installDir, _ = filepath.Abs(installDir)
	if _, err := os.Stat(installDir); err != nil {
		return fmt.Errorf("not installed at %s", installDir)
	}
	if err := os.RemoveAll(installDir); err != nil {
		return err
	}
	fmt.Printf("✓ Uninstalled %s\n", installDir)
	return nil
}

func cmdVersion() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	trailer, err := readTrailer(self)
	if err != nil {
		return err
	}
	m, err := readManifest(self, trailer.PayloadOffset)
	if err != nil {
		return err
	}
	fmt.Printf("%s %s\n", m.AppName, m.Version)
	return nil
}

// ── Trailer / manifest extraction ────────────────────────────────────────────

type trailerInfo struct {
	PayloadOffset int64
	RootHash      [RootHashBytes]byte
	HasIntegrity  bool
}

func readTrailer(path string) (*trailerInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size < LegacyTrailerSize {
		return nil, fmt.Errorf("file too small")
	}
	// V1 first: check magic at the end.
	if size >= int64(TrailerV1Size) {
		buf := make([]byte, TrailerV1Size)
		if _, err := f.ReadAt(buf, size-int64(TrailerV1Size)); err != nil {
			return nil, err
		}
		magic := buf[TrailerV1Size-len(TrailerMagic):]
		if string(magic) == TrailerMagic {
			ti := &trailerInfo{HasIntegrity: true}
			ti.PayloadOffset = int64(binary.LittleEndian.Uint64(buf[0:8]))
			copy(ti.RootHash[:], buf[8:8+RootHashBytes])
			return ti, nil
		}
	}
	// Legacy 8-byte offset-only.
	buf := make([]byte, LegacyTrailerSize)
	if _, err := f.ReadAt(buf, size-int64(LegacyTrailerSize)); err != nil {
		return nil, err
	}
	return &trailerInfo{
		PayloadOffset: int64(binary.LittleEndian.Uint64(buf)),
		HasIntegrity:  false,
	}, nil
}

// readManifest streams the payload tar and pulls out .molt/manifest.json.
func readManifest(binaryPath string, offset int64) (*Manifest, error) {
	f, err := os.Open(binaryPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	targets := []string{".molt/manifest.json", "src/.molt/manifest.json"}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := filepath.ToSlash(hdr.Name)
		for _, t := range targets {
			if name == t {
				data, err := io.ReadAll(tr)
				if err != nil {
					return nil, err
				}
				var m Manifest
				if err := json.Unmarshal(data, &m); err != nil {
					return nil, err
				}
				return &m, nil
			}
		}
	}
	return nil, fmt.Errorf("manifest not found in payload")
}

// readIntegrityManifest streams the payload tar and pulls out
// .molt/integrity.json (if present). Returns nil, nil if not found.
func readIntegrityManifest(binaryPath string, offset int64) (*IntegrityManifest, error) {
	f, err := os.Open(binaryPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	targets := []string{".molt/integrity.json", "src/.molt/integrity.json"}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := filepath.ToSlash(hdr.Name)
		for _, t := range targets {
			if name == t {
				data, err := io.ReadAll(tr)
				if err != nil {
					return nil, err
				}
				var im IntegrityManifest
				if err := json.Unmarshal(data, &im); err != nil {
					return nil, err
				}
				return &im, nil
			}
		}
	}
	return nil, nil
}

// ── Payload extraction ───────────────────────────────────────────────────────

func extractPayload(binaryPath string, offset int64, installDir string, verbose bool) error {
	f, err := os.Open(binaryPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		rel := filepath.FromSlash(hdr.Name)
		// Strip the "src/" prefix the builder adds.
		if strings.HasPrefix(hdr.Name, "src/") {
			rel = filepath.FromSlash(strings.TrimPrefix(hdr.Name, "src/"))
		} else if hdr.Name == "src" {
			continue
		}
		if rel == "" {
			continue
		}
		// Refuse absolute and traversal paths — belt and braces against a
		// tampered tar.
		if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
			return fmt.Errorf("unsafe path in payload: %s", hdr.Name)
		}
		target := filepath.Join(installDir, rel)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
			if err := os.Chmod(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
			count++
		case tar.TypeSymlink:
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
	logv(verbose, "extracted %d files to %s", count, installDir)
	return nil
}

// ── Integrity verification ───────────────────────────────────────────────────

// verifyExtracted reconstructs the root hash by walking the extracted tree
// and hashing every file, then compares against the trailer's expected hash.
//
// The embedded IntegrityManifest at .molt/integrity.json is the ground
// truth for what files SHOULD be there. We iterate that list rather than
// walking the disk, so missing files produce a clear "missing X" error
// rather than a silent hash mismatch.
func verifyExtracted(installDir string, expected [RootHashBytes]byte) error {
	// Try to read the embedded integrity manifest.
	manifestPath := filepath.Join(installDir, ".molt", "integrity.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("missing integrity manifest at %s: %w", manifestPath, err)
	}
	var im IntegrityManifest
	if err := json.Unmarshal(data, &im); err != nil {
		return fmt.Errorf("parse integrity manifest: %w", err)
	}

	// Re-hash every file listed in the manifest.
	actual := make([]PackagedFile, 0, len(im.Payload.Files))
	for _, f := range im.Payload.Files {
		path := filepath.Join(installDir, filepath.FromSlash(f.Path))
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("missing file %s", f.Path)
		}
		if info.Size() != f.Size {
			return fmt.Errorf("size mismatch for %s: expected %d, got %d",
				f.Path, f.Size, info.Size())
		}
		h, err := hashFile(path)
		if err != nil {
			return fmt.Errorf("hash %s: %w", f.Path, err)
		}
		actual = append(actual, PackagedFile{Path: f.Path, Size: info.Size(), SHA256: h})
	}

	got := computeRootHash(actual)
	expectedHex := hex.EncodeToString(expected[:])
	if got != expectedHex {
		return fmt.Errorf("root hash mismatch: computed %s, expected %s",
			shortHash(got), shortHash(expectedHex))
	}
	return nil
}

// computeRootHash mirrors integrity.ComputeRootHash — kept duplicated here
// so the launcher binary doesn't need to import the integrity package.
//
// Algorithm: sort by Path, for each file feed sha256(path||0x00||hash) to
// an outer sha256, hex-encode the outer digest.
func computeRootHash(files []PackagedFile) string {
	sorted := make([]PackagedFile, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	outer := sha256.New()
	for _, f := range sorted {
		inner := sha256.New()
		inner.Write([]byte(f.Path))
		inner.Write([]byte{0x00})
		raw, err := hex.DecodeString(f.SHA256)
		if err != nil || len(raw) != sha256.Size {
			inner.Write([]byte("INVALID:" + f.SHA256))
		} else {
			inner.Write(raw)
		}
		outer.Write(inner.Sum(nil))
	}
	return hex.EncodeToString(outer.Sum(nil))
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ── Python install ───────────────────────────────────────────────────────────

func installPython(installDir string, m *Manifest, offline, verbose bool) error {
	pyDir := filepath.Join(installDir, "python")
	pyBin := filepath.Join(pyDir, "bin", pythonBinaryName())
	if runtime.GOOS == "windows" {
		pyBin = filepath.Join(pyDir, "python.exe")
	}
	if _, err := os.Stat(pyBin); err == nil {
		logv(verbose, "python already present — skipping")
		return nil
	}
	// Fall back to system python — this is the path for minimal profiles
	// and for offline mode. We don't try to download Python from here.
	sys, err := exec.LookPath(pythonBinaryName())
	if err != nil {
		return fmt.Errorf("no bundled python and system %s not found", pythonBinaryName())
	}
	os.MkdirAll(filepath.Join(pyDir, "bin"), 0o755)
	return os.Symlink(sys, pyBin)
}

// createVenv is optional: if the payload already contained a venv (rare),
// skip; otherwise use `python -m venv`.
func createVenv(installDir string, verbose bool) error {
	venvDir := filepath.Join(installDir, ".venv")
	if _, err := os.Stat(filepath.Join(venvDir, "bin")); err == nil {
		logv(verbose, "venv already present — skipping")
		return nil
	}
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(filepath.Join(venvDir, "Scripts")); err == nil {
			return nil
		}
	}

	py := findPython(installDir)
	if py == "" {
		return fmt.Errorf("python not found to create venv")
	}
	c := exec.Command(py, "-m", "venv", venvDir)
	if verbose {
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
	}
	return c.Run()
}

// ── Dependency install ───────────────────────────────────────────────────────

// installDependencies dispatches on the MoltConfig deps strategy. All modes
// operate against the venv pip — we don't assume uv is present on the
// target. For legacy builds (no MoltConfig), falls back to installing the
// PyPackages from the manifest.
func installDependencies(installDir string, m *Manifest, verbose bool) error {
	pip := findPip(installDir)
	if pip == "" {
		return fmt.Errorf("pip not found in venv")
	}

	// Legacy: no molt.yaml snapshot → use m.PyPackages.
	if m.MoltConfigSnapshot == nil || m.MoltConfigSnapshot.Deps == nil {
		return installFromPyPackages(pip, m.PyPackages, verbose)
	}

	d := m.MoltConfigSnapshot.Deps
	switch d.Strategy {
	case "none":
		logv(verbose, "deps.strategy=none — skipping")
		return nil

	case "pyproject":
		// Prefer pyproject.toml that we unpacked. If uv.lock is present,
		// install from that via `pip install -r <(uv export)`? Too fragile.
		// Fall back to `pip install -e .` which respects pyproject deps.
		args := []string{"install", "-e", "."}
		args = append(args, d.ExtraArgs...)
		return runPip(pip, args, installDir, verbose)

	case "requirements":
		if len(d.Files) == 0 {
			return fmt.Errorf("requirements strategy has no files")
		}
		for _, rf := range d.Files {
			args := []string{"install", "-r", rf}
			args = append(args, d.ExtraArgs...)
			if err := runPip(pip, args, installDir, verbose); err != nil {
				return err
			}
		}
		return nil

	case "poetry", "pipenv":
		// Without poetry/pipenv on the target we can't use their native
		// tooling; both should have been exported to requirements.txt at
		// build time by the builder (a known TODO). Warn and no-op here.
		fmt.Fprintf(os.Stderr, "  ⚠ deps.strategy=%s needs requirements export at build time\n",
			d.Strategy)
		return nil

	default:
		return fmt.Errorf("unknown deps strategy %q", d.Strategy)
	}
}

func installFromPyPackages(pip string, pkgs []PyPackage, verbose bool) error {
	if len(pkgs) == 0 {
		return nil
	}
	args := []string{"install"}
	for _, p := range pkgs {
		args = append(args, fmt.Sprintf("%s==%s", p.Name, p.Version))
	}
	return runPip(pip, args, "", verbose)
}

func runPip(pip string, args []string, cwd string, verbose bool) error {
	c := exec.Command(pip, args...)
	if cwd != "" {
		c.Dir = cwd
	}
	if verbose {
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
	}
	return c.Run()
}

// ── Env / path helpers ───────────────────────────────────────────────────────

// buildExecEnv constructs the environment for a subprocess running inside
// the hermetic install. Strips PYTHONHOME (breaks venv stdlib), prepends
// venv/bin to PATH, applies molt.yaml env block then per-command overrides.
func buildExecEnv(installDir string, m *Manifest, overrides map[string]string) []string {
	srcDir := installDir
	venvDir := filepath.Join(installDir, ".venv")
	venvBin := filepath.Join(venvDir, "bin")
	if runtime.GOOS == "windows" {
		venvBin = filepath.Join(venvDir, "Scripts")
	}

	out := make([]string, 0, 32)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "PYTHONHOME=") ||
			strings.HasPrefix(e, "PYTHONPATH=") ||
			strings.HasPrefix(e, "VIRTUAL_ENV=") {
			continue
		}
		out = append(out, e)
	}

	// PATH: venv/bin first.
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	pathVal := venvBin + sep + os.Getenv("PATH")
	setEnvVar(&out, "PATH", pathVal)
	setEnvVar(&out, "VIRTUAL_ENV", venvDir)
	setEnvVar(&out, "PYTHONPATH", srcDir)
	setEnvVar(&out, "PYTHONNOUSERSITE", "1")
	setEnvVar(&out, "PYTHONDONTWRITEBYTECODE", "1")

	// molt.yaml env block — applied before per-command to let commands override.
	if m.MoltConfigSnapshot != nil {
		for k, v := range m.MoltConfigSnapshot.Env {
			setEnvVar(&out, k, v)
		}
	}
	for k, v := range overrides {
		setEnvVar(&out, k, v)
	}
	return out
}

func setEnvVar(env *[]string, key, val string) {
	prefix := key + "="
	for i, e := range *env {
		if strings.HasPrefix(e, prefix) {
			(*env)[i] = prefix + val
			return
		}
	}
	*env = append(*env, prefix+val)
}

func findPython(installDir string) string {
	candidates := []string{
		filepath.Join(installDir, ".venv", "bin", pythonBinaryName()),
		filepath.Join(installDir, ".venv", "Scripts", pythonBinaryName()),
		filepath.Join(installDir, "python", "bin", pythonBinaryName()),
		filepath.Join(installDir, "python", pythonBinaryName()),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	// Recursive fallback.
	var found string
	filepath.WalkDir(installDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Base(p) == pythonBinaryName() {
			dir := filepath.Base(filepath.Dir(p))
			if dir == "bin" || dir == "Scripts" {
				found = p
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

func findPip(installDir string) string {
	name := "pip"
	if runtime.GOOS == "windows" {
		name = "pip.exe"
	}
	for _, p := range []string{
		filepath.Join(installDir, ".venv", "bin", name),
		filepath.Join(installDir, ".venv", "Scripts", name),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// resolveInVenv returns the absolute path to a binary inside the venv's
// bin/Scripts directory, or "" if not there. Lets `exec: [pytest, ...]`
// find the right pytest.
func resolveInVenv(installDir, name string) string {
	if filepath.IsAbs(name) {
		return ""
	}
	for _, sub := range []string{"bin", "Scripts"} {
		p := filepath.Join(installDir, ".venv", sub, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
		if runtime.GOOS == "windows" {
			if _, err := os.Stat(p + ".exe"); err == nil {
				return p + ".exe"
			}
		}
	}
	return ""
}

func pythonBinaryName() string {
	if runtime.GOOS == "windows" {
		return "python.exe"
	}
	return "python3"
}

// ── Install dir resolution ───────────────────────────────────────────────────

func resolveInstallDir(appName, version string) string {
	if d := os.Getenv("MOLT_INSTALL_DIR"); d != "" {
		return d
	}
	base := os.Getenv("MOLT_INSTALL_BASE")
	if base == "" {
		base = defaultInstallBase()
	}
	return filepath.Join(base, appName, version)
}

func defaultInstallBase() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("APPDATA"); d != "" {
			return d
		}
		return filepath.Join(home, "AppData", "Roaming")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support")
	default:
		return filepath.Join(home, ".local", "share")
	}
}

// ── Receipt ──────────────────────────────────────────────────────────────────

func writeReceipt(installDir string, m *Manifest) {
	type receipt struct {
		AppName    string    `json:"app_name"`
		Version    string    `json:"version"`
		InstallDir string    `json:"install_dir"`
		InstalledAt time.Time `json:"installed_at"`
	}
	r := receipt{
		AppName:     m.AppName,
		Version:     m.Version,
		InstallDir:  installDir,
		InstalledAt: time.Now().UTC(),
	}
	data, _ := json.MarshalIndent(r, "", "  ")
	p := filepath.Join(installDir, ".molt", "receipt.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, data, 0o644)
}

// ── Shell / arg helpers ──────────────────────────────────────────────────────

func runShell(cmd, cwd string, env []string) error {
	shell, flag := "/bin/sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/C"
	}
	c := exec.Command(shell, flag, cmd)
	c.Dir = cwd
	c.Env = env
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func hasFlag(args []string, flags ...string) bool {
	for _, a := range args {
		for _, f := range flags {
			if a == f {
				return true
			}
		}
	}
	return false
}

func flagValue(args []string, flag, def string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, flag+"=") {
			return strings.TrimPrefix(a, flag+"=")
		}
	}
	return def
}

func filterFlags(args []string, flags ...string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		skip := false
		for _, f := range flags {
			if a == f {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, a)
		}
	}
	return out
}

func step(format string, a ...any) { fmt.Printf("[molt] "+format+"\n", a...) }

func logv(verbose bool, format string, a ...any) {
	if verbose {
		fmt.Printf("  "+format+"\n", a...)
	}
}

func shortHash(h string) string {
	if len(h) > 16 {
		return h[:16] + "…"
	}
	return h
}

// readIntegrityManifestFromDisk tries .molt/integrity.json next to the
// binary (fallback for older extracted trees). Not currently called —
// present as a future hook for a --deep verify.
func readIntegrityManifestFromDisk(path string) (*IntegrityManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var im IntegrityManifest
	if err := json.Unmarshal(data, &im); err != nil {
		return nil, err
	}
	return &im, nil
}
