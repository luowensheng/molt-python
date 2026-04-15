package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"pyexec/internal/builder"
	"pyexec/internal/executor"
	"pyexec/internal/installer"
	"pyexec/internal/manifest"
	"pyexec/internal/platform"
	internuv "pyexec/internal/uv"
	"pyexec/internal/verifier"
	"pyexec/pkg/types"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	// Project lifecycle
	case "init":
		err = cmdInit(os.Args[2:])
	case "add":
		err = cmdAdd(os.Args[2:])
	case "remove":
		err = cmdRemove(os.Args[2:])
	case "sync":
		err = cmdSync(os.Args[2:])
	case "lock":
		err = cmdLock(os.Args[2:])
	case "tree":
		err = cmdTree(os.Args[2:])
	case "uv":
		err = cmdUV(os.Args[2:])
	// Distribution
	case "build":
		err = cmdBuild(os.Args[2:])
	case "capture":
		err = cmdCapture(os.Args[2:])
	case "assemble":
		err = cmdAssemble(os.Args[2:])
	case "install":
		err = cmdInstall(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "versions":
		err = cmdVersions(os.Args[2:])
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "doctor":
		err = cmdDoctor()
	case "sbom":
		err = cmdSBOM(os.Args[2:])
	// Meta
	case "version", "--version":
		fmt.Printf("pyexec %s %s\n", version, date)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`pyexec - Hermetic Python application distribution

Project:
  init     [flags] [name]          Scaffold a new Python project
  add      [--dev] <pkg...>        Add dependency
  remove   [--dev] <pkg...>        Remove dependency
  sync     [--frozen]              Sync venv with lockfile
  lock                             Regenerate uv.lock
  tree                             Show dependency tree
  uv       <args...>               Raw passthrough to uv

Distribution (native build):
  build    [flags] [project-path]  Build binary for current platform

Distribution (cross-platform — two-step):
  capture  [flags] [project-path]  Step 1: snapshot environment (run on TARGET machine)
  assemble [flags] [project-path]  Step 2: build binary from snapshot (run on any machine)

Deployment:
  install  [flags] [manifest]      Install on target machine
  run      [flags] [-- args...]    Run installed application
  versions <app-name>              List installed versions
  verify   [install-dir]           Verify installation integrity
  doctor                           System diagnostics and build capabilities
  sbom     [install-dir]           Software Bill of Materials (JSON)

Other:
  version                          Print version and platform

`)
}

// ── Project lifecycle ─────────────────────────────────────────────────────────

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	python := fs.String("python", "", "Python version constraint, e.g. 3.12")
	lib := fs.Bool("lib", false, "Library project layout (src/)")
	noLock := fs.Bool("no-lock", false, "Skip uv lock after init")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dir, name := ".", ""
	switch fs.NArg() {
	case 0:
		abs, _ := filepath.Abs(dir)
		name = filepath.Base(abs)
	case 1:
		name = fs.Arg(0)
		dir = name
	default:
		return fmt.Errorf("usage: pyexec init [flags] [name]")
	}

	fmt.Printf("Initialising project %q...\n", name)
	if err := internuv.Init(dir, name, internuv.InitOptions{Python: *python, Lib: *lib}); err != nil {
		return fmt.Errorf("uv init: %w", err)
	}
	if !*noLock {
		fmt.Println("Generating lockfile...")
		if err := internuv.Lock(dir); err != nil {
			return fmt.Errorf("uv lock: %w", err)
		}
		fmt.Println("✓ uv.lock generated")
	}
	fmt.Println("\nDone! Next steps:")
	if name != "." && name != "" {
		fmt.Printf("  cd %s\n", name)
	}
	fmt.Println("  pyexec add <package>")
	fmt.Println("  pyexec build")
	return nil
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	dev := fs.Bool("dev", false, "Development dependency")
	dir := fs.String("dir", ".", "Project directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: pyexec add [--dev] <package...>")
	}
	absDir, _ := filepath.Abs(*dir)
	return internuv.Add(absDir, fs.Args(), *dev)
}

func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	dev := fs.Bool("dev", false, "Development dependency")
	dir := fs.String("dir", ".", "Project directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: pyexec remove [--dev] <package...>")
	}
	absDir, _ := filepath.Abs(*dir)
	return internuv.Remove(absDir, fs.Args(), *dev)
}

func cmdSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	frozen := fs.Bool("frozen", false, "Fail if lockfile needs updating")
	dir := fs.String("dir", ".", "Project directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absDir, _ := filepath.Abs(*dir)
	return internuv.Sync(absDir, *frozen)
}

func cmdLock(args []string) error {
	fs := flag.NewFlagSet("lock", flag.ExitOnError)
	dir := fs.String("dir", ".", "Project directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absDir, _ := filepath.Abs(*dir)
	return internuv.Lock(absDir)
}

func cmdTree(args []string) error {
	fs := flag.NewFlagSet("tree", flag.ExitOnError)
	dir := fs.String("dir", ".", "Project directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absDir, _ := filepath.Abs(*dir)
	return internuv.Tree(absDir)
}

func cmdUV(args []string) error {
	if len(args) == 0 {
		return internuv.Raw(".", []string{"--help"})
	}
	dir := "."
	if d := os.Getenv("PYEXEC_DIR"); d != "" {
		dir = d
	}
	return internuv.Raw(dir, args)
}

// ── Distribution ──────────────────────────────────────────────────────────────

func cmdBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	profile := fs.String("profile", "standard", "minimal|standard|extended|full")
	name := fs.String("name", "", "Application name (default: dir name)")
	ver := fs.String("version", "0.1.0", "Application version")
	output := fs.String("output", "", "Output binary path")
	targetOS := fs.String("os", runtime.GOOS, "Target OS: linux|darwin|windows")
	targetArch := fs.String("arch", runtime.GOARCH, "Target arch: amd64|arm64")
	bestEffort := fs.Bool("best-effort", false, "Allow cross-build (INACCURATE deps — testing only)")
	offline := fs.Bool("offline", false, "Build for offline deployment")
	signKey := fs.String("sign-key", "", "Path to signing private key")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectPath := "."
	if fs.NArg() > 0 {
		projectPath = fs.Arg(0)
	}
	absProject, err := filepath.Abs(projectPath)
	if err != nil {
		return err
	}
	if *name == "" {
		*name = filepath.Base(absProject)
	}
	if *output == "" {
		*output = fmt.Sprintf("%s-v%s", *name, *ver)
	}

	crossMode := types.CrossBuildDeny
	if *bestEffort {
		crossMode = types.CrossBuildBestEffort
	}

	return builder.New(types.BuildConfig{
		Profile:        types.BuildProfile(*profile),
		Name:           *name,
		Version:        *ver,
		ProjectPath:    absProject,
		OutputPath:     *output,
		TargetOS:       *targetOS,
		TargetArch:     *targetArch,
		Offline:        *offline,
		SignKeyPath:    *signKey,
		CrossBuildMode: crossMode,
	}).Build()
}

func cmdCapture(args []string) error {
	fs := flag.NewFlagSet("capture", flag.ExitOnError)
	output := fs.String("output", "", "Output manifest path (default: <os>-<arch>.manifest.json)")
	targetOS := fs.String("os", runtime.GOOS, "Declare target OS (must match this machine)")
	targetArch := fs.String("arch", runtime.GOARCH, "Declare target arch (must match this machine)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectPath := "."
	if fs.NArg() > 0 {
		projectPath = fs.Arg(0)
	}
	absProject, err := filepath.Abs(projectPath)
	if err != nil {
		return err
	}

	return builder.Capture(types.CaptureConfig{
		ProjectPath: absProject,
		OutputPath:  *output,
		TargetOS:    *targetOS,
		TargetArch:  *targetArch,
	})
}

func cmdAssemble(args []string) error {
	fs := flag.NewFlagSet("assemble", flag.ExitOnError)
	manifestPath := fs.String("manifest", "", "Path to manifest JSON from `pyexec capture` (required)")
	name := fs.String("name", "", "Application name (required)")
	ver := fs.String("version", "0.1.0", "Application version")
	output := fs.String("output", "", "Output binary path")
	profile := fs.String("profile", "standard", "minimal|standard|extended|full")
	targetOS := fs.String("os", "", "Target OS from the capture step")
	targetArch := fs.String("arch", "", "Target arch from the capture step")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *manifestPath == "" {
		return fmt.Errorf("-manifest is required")
	}
	if *name == "" {
		return fmt.Errorf("-name is required")
	}

	projectPath := "."
	if fs.NArg() > 0 {
		projectPath = fs.Arg(0)
	}
	absProject, _ := filepath.Abs(projectPath)

	if *output == "" {
		*output = fmt.Sprintf("%s-v%s", *name, *ver)
	}

	// If --os/--arch not given, read from the manifest.
	tOS, tArch := *targetOS, *targetArch
	if tOS == "" || tArch == "" {
		if data, err := os.ReadFile(*manifestPath); err == nil {
			var m types.Manifest
			if json.Unmarshal(data, &m) == nil {
				if tOS == "" {
					tOS = m.TargetOS
				}
				if tArch == "" {
					tArch = m.TargetArch
				}
			}
		}
	}

	return builder.NewAssembler(types.AssembleConfig{
		ManifestPath: *manifestPath,
		ProjectPath:  absProject,
		OutputPath:   *output,
		TargetOS:     tOS,
		TargetArch:   tArch,
		Profile:      types.BuildProfile(*profile),
	}).Assemble(*name, *ver)
}

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	mode := fs.String("mode", "standalone", "minimal|standalone|exact")
	prefix := fs.String("prefix", "", "Installation directory")
	cacheDir := fs.String("cache-dir", "", "Cache directory")
	offline := fs.Bool("offline", false, "No network downloads")
	parallel := fs.Int("parallel", 4, "Parallel download workers")
	verbose := fs.Bool("verbose", false, "Verbose output")
	dryRun := fs.Bool("dry-run", false, "Dry run")
	auditLog := fs.String("audit-log", "", "Audit log path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	manifestArg := ".pyexec/manifest.json"
	if fs.NArg() > 0 {
		manifestArg = fs.Arg(0)
	}
	m, err := loadManifest(manifestArg)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}

	targetDir := *prefix
	if targetDir == "" {
		base, err := platform.DefaultInstallBase()
		if err != nil {
			return err
		}
		targetDir = filepath.Join(base, m.AppName, m.Version)
	}
	inst, err := installer.New(types.InstallConfig{
		Mode: types.InstallMode(*mode), CacheDir: *cacheDir,
		Offline: *offline, Parallel: *parallel,
		Verbose: *verbose, DryRun: *dryRun, AuditLog: *auditLog,
	})
	if err != nil {
		return err
	}
	return inst.Install(context.Background(), m, targetDir)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	appName := fs.String("app", "", "Application name")
	appVer := fs.String("version", "", "Application version")
	isolated := fs.Bool("isolated", false, "Linux namespace isolation (no-op on macOS/Windows)")
	debug := fs.Bool("debug", false, "Debug output")
	if err := fs.Parse(args); err != nil {
		return err
	}

	installDir, err := executor.DefaultInstallDir(*appName, *appVer)
	if err != nil {
		return err
	}
	if _, err := os.Stat(installDir); os.IsNotExist(err) {
		return fmt.Errorf("not installed: %s v%s (run 'pyexec install' first)", *appName, *appVer)
	}
	exec, err := executor.New(installDir, types.ExecutionConfig{UseNamespace: *isolated, Debug: *debug})
	if err != nil {
		return err
	}
	return exec.Run(fs.Args())
}

func cmdVersions(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pyexec versions <app-name>")
	}
	base, err := platform.DefaultInstallBase()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(base, args[0]))
	if err != nil {
		return fmt.Errorf("no versions installed for %s", args[0])
	}
	fmt.Printf("Installed versions of %s:\n", args[0])
	for _, e := range entries {
		if e.IsDir() {
			fmt.Printf("  • %s\n", e.Name())
		}
	}
	return nil
}

func cmdVerify(args []string) error {
	installDir := "."
	if len(args) > 0 {
		installDir = args[0]
	}
	m, err := manifest.Load(installDir)
	if err != nil {
		return err
	}
	if err := verifier.New().Verify(installDir, m); err != nil {
		return fmt.Errorf("verification failed: %w", err)
	}
	fmt.Println("✓ Installation verified")
	return nil
}

func cmdDoctor() error {
	uvVer := "not found"
	if v, err := internuv.Version(); err == nil {
		uvVer = v
	}

	fmt.Printf("PyExec %s\n", version)
	fmt.Printf("Platform:  %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println("─────────────────────────────────────")

	installBase, _ := platform.DefaultInstallBase()
	cacheDir, _ := platform.DefaultCacheDir()
	fmt.Printf("  Install base: %s\n", installBase)
	fmt.Printf("  Cache dir:    %s\n", cacheDir)
	fmt.Println()

	// Core tool checks.
	checks := []struct {
		label string
		fn    func() (string, bool)
	}{
		{platform.PythonBinaryName(runtime.GOOS), func() (string, bool) {
			p, e := lookPath(platform.PythonBinaryName(runtime.GOOS))
			return p, e == nil
		}},
		{"uv", func() (string, bool) { return uvVer, internuv.Available() }},
		{"go", func() (string, bool) { p, e := lookPath("go"); return p, e == nil }},
		{depTool(), func() (string, bool) { p, e := lookPath(depTool()); return p, e == nil }},
		{"namespaces", func() (string, bool) {
			return "kernel feature", platform.IsolationSupported()
		}},
	}

	allCore := true
	for _, c := range checks {
		loc, ok := c.fn()
		sym := "✓"
		if !ok {
			sym = "✗"
			allCore = false
		}
		fmt.Printf("  %s %-22s %s\n", sym, c.label, loc)
	}

	// Cross-build capability matrix.
	fmt.Println()
	fmt.Println("Cross-build capability:")
	goAvailable := func() bool { _, e := lookPath("go"); return e == nil }()
	targets := []struct{ os, arch string }{
		{"linux", "amd64"}, {"linux", "arm64"},
		{"darwin", "amd64"}, {"darwin", "arm64"},
		{"windows", "amd64"},
	}
	for _, t := range targets {
		native := t.os == runtime.GOOS && t.arch == runtime.GOARCH
		label := fmt.Sprintf("%s/%s", t.os, t.arch)
		if native {
			fmt.Printf("  ✓ %-20s (native)\n", label)
		} else if goAvailable {
			fmt.Printf("  ~ %-20s (launcher only — use capture+assemble for accurate deps)\n", label)
		} else {
			fmt.Printf("  ✗ %-20s (go required)\n", label)
		}
	}

	fmt.Println()
	if allCore {
		fmt.Println("Core checks passed ✓")
	} else {
		fmt.Println("Some checks failed.")
	}
	fmt.Println()
	fmt.Println("For cross-platform builds, see: pyexec capture --help")
	return nil
}

func cmdSBOM(args []string) error {
	installDir := "."
	if len(args) > 0 {
		installDir = args[0]
	}
	m, err := manifest.Load(installDir)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(verifier.New().SBOM(installDir, m))
}

// ── helpers ───────────────────────────────────────────────────────────────────

func loadManifest(arg string) (*types.Manifest, error) {
	if data, err := os.ReadFile(arg); err == nil {
		return manifest.Decode(data)
	}
	return manifest.Load(arg)
}

func lookPath(name string) (string, error) {
	search := []string{"/usr/bin", "/usr/local/bin", "/bin"}
	for _, dir := range strings.Split(os.Getenv("PATH"), string(filepath.ListSeparator)) {
		search = append(search, dir)
	}
	for _, dir := range search {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("not found: %s", name)
}

func depTool() string {
	switch runtime.GOOS {
	case "darwin":
		return "otool"
	case "windows":
		return "dumpbin"
	default:
		return "ldd"
	}
}
