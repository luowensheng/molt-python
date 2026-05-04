package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"

	"molt/internal/adopt"
	"molt/internal/builder"
	"molt/internal/integrity"
	"molt/internal/python"
	"molt/internal/syncplan"
	"molt/internal/syspath"
	"molt/internal/tasks"
	internuv "molt/internal/uv"
	"molt/internal/uvbin"
	"molt/pkg/types"
)

var (
	version string
	date    string
	commit  string
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "build":
		err = cmdBuild(os.Args[2:])
	case "capture":
		err = cmdCapture(os.Args[2:])
	case "assemble":
		err = cmdAssemble(os.Args[2:])

	case "adopt":
		err = cmdAdopt(os.Args[2:])
	case "verify-binary":
		err = cmdVerifyBinary(os.Args[2:])
	case "inspect":
		err = cmdInspect(os.Args[2:])
	case "diff":
		err = cmdDiff(os.Args[2:])

	// ── Project lifecycle ──────────────────────────────────────────────────
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

	// ── Python version management ──────────────────────────────────────────
	case "python":
		err = cmdPython(os.Args[2:])

	// ── Task runner ────────────────────────────────────────────────────────
	case "run":
		err = cmdRun(os.Args[2:])
	case "task":
		err = cmdTask(os.Args[2:])

	// ── Global package store ──────────────────────────────────────────────
	case "gc":
		err = cmdGC(os.Args[2:])

	case "uv":
		err = cmdUV(os.Args[2:])
	case "doctor":
		err = cmdDoctor()

	// ── Meta ──────────────────────────────────────────────────────────────
	case "info":
		err = cmdInfo()
	case "version", "--version":
		v, d, c := version, date, commit
		if v == "" {
			v = "dev"
		}
		if d == "" {
			d = "unknown"
		}
		if c == "" {
			if info, ok := debug.ReadBuildInfo(); ok {
				for _, s := range info.Settings {
					if s.Key == "vcs.revision" && s.Value != "" {
						c = s.Value
						if len(c) > 12 {
							c = c[:12]
						}
					}
				}
			}
			if c == "" {
				c = "unknown"
			}
		}
		fmt.Printf("molt %s (commit %s, built %s)\n", v, c, d)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`molt — hermetic Python project toolchain

Project:
  init     [flags] [name]          Scaffold a new Python project
  add      [--dev] <pkg...>        Add dependency
  remove   [--dev] <pkg...>        Remove dependency
  sync     [--frozen] [--refresh]  Install lockfile into ~/.molt/pkg + write .molt/syspath.json
  lock                             Regenerate uv.lock
  gc       [--dry-run]             Remove ~/.molt/pkg entries no project references
  uv       <args...>               Raw passthrough to uv

Python versions:
  python list                      List all Python versions
  python install <version>         Install a Python version
  python use <version>             Set project Python version
  python use <version> --global    Set global Python version
  python remove <version>          Remove a Python version
  python which                     Show active Python path
  python audit                     Find every Python on this machine
  python conflicts                 Detect sys.path pollution
  python isolation-check           Verify environment isolation

Task runner:
  run <task>   [-- extra-args]     Run a named task from [tool.molt.tasks]
  run <binary> [args...]           Exec a binary under the project environment
  task list                        List tasks
  task add <name> <command>        Add a task
  task remove <name>               Remove a task

Build:
  build    [flags] [project-path]  Build self-contained binary
  capture  [flags] [project-path]  Capture environment manifest
  assemble [flags]                 Assemble binary from a manifest

Adoption:
  adopt    [dir] [--non-interactive] [--force]   Scaffold molt.yaml

Integrity:
  verify-binary <binary> [--deep]  Verify binary integrity
  inspect <binary> [--files] [--json]  Show embedded manifest
  diff <a> <b>                     Compare two builds/manifests

uv:
  uv path                          Print resolved uv binary path
  uv version                       Print uv version
  uv <args...>                     Raw passthrough to uv

Diagnostics:
  doctor                           System/tool diagnostics
  info                             Show project environment summary
  version                          Print molt version

`)
}

// ── Build / capture / assemble ────────────────────────────────────────────────

func cmdBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	profile := fs.String("profile", "standard", "minimal|standard|extended|full")
	name := fs.String("name", "", "Application name")
	ver := fs.String("version", "0.1.0", "Application version")
	output := fs.String("output", "", "Output path")
	targetOS := fs.String("os", runtime.GOOS, "Target OS")
	targetArch := fs.String("arch", runtime.GOARCH, "Target arch")
	bestEffort := fs.Bool("best-effort", false, "Allow cross-build")
	embedStrict := fs.Bool("embed-strict", true, "Fail build on sensitive files (.env, keys, etc.)")
	embedIgnore := fs.String("embed-ignore", ".moltignore", "Path to .moltignore file")

	fs.Parse(args)

	projectPath := "."
	if fs.NArg() > 0 {
		projectPath = fs.Arg(0)
	}
	absProject, _ := filepath.Abs(projectPath)
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
		Profile:         types.BuildProfile(*profile),
		Name:            *name,
		Version:         *ver,
		ProjectPath:     absProject,
		OutputPath:      *output,
		TargetOS:        *targetOS,
		TargetArch:      *targetArch,
		CrossBuildMode:  crossMode,
		EmbedStrict:     *embedStrict,
		EmbedIgnoreFile: *embedIgnore,
	}).Build()
}

func cmdCapture(args []string) error {
	fs := flag.NewFlagSet("capture", flag.ExitOnError)
	output := fs.String("output", "", "Output manifest path")
	targetOS := fs.String("os", runtime.GOOS, "Target OS")
	targetArch := fs.String("arch", runtime.GOARCH, "Target arch")
	fs.Parse(args)

	projectPath := "."
	if fs.NArg() > 0 {
		projectPath = fs.Arg(0)
	}
	absProject, _ := filepath.Abs(projectPath)

	return builder.Capture(types.CaptureConfig{
		ProjectPath: absProject,
		OutputPath:  *output,
		TargetOS:    *targetOS,
		TargetArch:  *targetArch,
	})
}

func cmdAssemble(args []string) error {
	fs := flag.NewFlagSet("assemble", flag.ExitOnError)
	manifestPath := fs.String("manifest", "", "Manifest path (required)")
	name := fs.String("name", "", "Application name (required)")
	ver := fs.String("version", "0.1.0", "Version")
	output := fs.String("output", "", "Output path")
	profile := fs.String("profile", "standard", "Build profile")
	targetOS := fs.String("os", "", "Target OS")
	targetArch := fs.String("arch", "", "Target arch")
	fs.Parse(args)

	if *manifestPath == "" {
		return fmt.Errorf("-manifest is required")
	}
	if *name == "" {
		return fmt.Errorf("-name is required")
	}
	if *output == "" {
		*output = fmt.Sprintf("%s-v%s", *name, *ver)
	}

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
		OutputPath:   *output,
		TargetOS:     tOS,
		TargetArch:   tArch,
		Profile:      types.BuildProfile(*profile),
	}).Assemble(*name, *ver)
}

// ── Adopt + integrity ─────────────────────────────────────────────────────────

func cmdAdopt(args []string) error {
	projectDir := "."
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			projectDir = a
			break
		}
	}
	return adopt.Run(adopt.Options{
		ProjectDir:     projectDir,
		NonInteractive: hasFlag(args, "--non-interactive"),
		Force:          hasFlag(args, "--force"),
	})
}

func cmdVerifyBinary(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt verify-binary <binary> [--deep]")
	}
	path := args[0]
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("binary not found: %w", err)
	}
	if hasFlag(args[1:], "--deep") {
		if err := integrity.VerifyBinaryAgainstManifest(path); err != nil {
			return fmt.Errorf("deep verify: %w", err)
		}
		fmt.Println("✓ Deep verification passed — every file in the payload was re-hashed.")
		return nil
	}
	if err := integrity.VerifyBinary(path); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	fmt.Println("✓ Binary integrity verified.")
	return nil
}

func cmdInspect(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt inspect <binary-or-manifest> [--files] [--json]")
	}
	m, err := loadManifestFromAnywhere(args[0])
	if err != nil {
		return err
	}
	if hasFlag(args[1:], "--json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(m)
	}
	fmt.Print(integrity.Summarize(m))
	if hasFlag(args[1:], "--files") {
		fmt.Println()
		fmt.Print(integrity.FormatFileList(m))
	}
	return nil
}

func cmdDiff(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: molt diff <a> <b>  (both can be binaries or .manifest.json)")
	}
	a, err := loadManifestFromAnywhere(args[0])
	if err != nil {
		return fmt.Errorf("load %s: %w", args[0], err)
	}
	b, err := loadManifestFromAnywhere(args[1])
	if err != nil {
		return fmt.Errorf("load %s: %w", args[1], err)
	}
	fmt.Printf("Comparing:\n  a: %s v%s  (root %s)\n  b: %s v%s  (root %s)\n\n",
		a.App.Name, a.App.Version, short(a.RootHash),
		b.App.Name, b.App.Version, short(b.RootHash))
	fmt.Print(integrity.FormatDiff(integrity.Diff(a, b)))
	return nil
}

func loadManifestFromAnywhere(path string) (*types.IntegrityManifest, error) {
	if im, err := integrity.ReadManifest(path); err == nil {
		return im, nil
	}
	trailer, err := integrity.ReadTrailer(path)
	if err != nil {
		return nil, fmt.Errorf("not a manifest file and not a molt binary: %w", err)
	}
	if !trailer.HasIntegrity {
		return nil, fmt.Errorf("binary has no integrity trailer (legacy build)")
	}
	return integrity.ExtractEmbeddedManifest(path, trailer.PayloadOffset)
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}

// ── Project lifecycle ─────────────────────────────────────────────────────────

func cmdInit(args []string) error {
	if _, err := uvbin.Ensure(); err != nil {
		return fmt.Errorf("failed to bootstrap uv: %w", err)
	}
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	pyVersion := fs.String("python", "", "Python version")
	lib := fs.Bool("lib", false, "Library layout")
	noLock := fs.Bool("no-lock", false, "Skip uv lock")
	fs.Parse(args)

	// initName is passed to uv init; dir is where uv lock runs afterwards.
	// Case 0 (no args): init the current directory in-place — pass no name to
	//   uv so it does not create a subdirectory. uv uses the cwd name itself.
	// Case 1 (name given): let uv create a new subdirectory called name, then
	//   lock inside it.
	var dir, name, initName string
	switch fs.NArg() {
	case 0:
		dir = "."
		abs, _ := filepath.Abs(dir)
		name = filepath.Base(abs)
		initName = "" // no name → uv init initialises the current directory
	case 1:
		name = fs.Arg(0)
		dir = name    // lock will run in the newly created subdirectory
		initName = name
	default:
		return fmt.Errorf("usage: molt init [flags] [name]")
	}

	fmt.Printf("Initialising project %q...\n", name)
	// Always run uv init from the current directory ("."); for case 1 it
	// creates the named subdirectory automatically.
	if err := internuv.Init(".", initName, internuv.InitOptions{Python: *pyVersion, Lib: *lib}); err != nil {
		return fmt.Errorf("uv init: %w", err)
	}
	if !*noLock {
		if err := internuv.Lock(dir); err != nil {
			return fmt.Errorf("uv lock: %w", err)
		}
	}
	fmt.Println("✓ Done.")
	return nil
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	dev := fs.Bool("dev", false, "Dev dependency")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: molt add <package...>")
	}
	absDir, _ := filepath.Abs(".")
	if err := internuv.Add(absDir, fs.Args(), *dev); err != nil {
		return err
	}
	return syncplan.Sync(absDir, syncplan.Options{Verbose: true})
}

func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	dev := fs.Bool("dev", false, "Dev dependency")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: molt remove <package...>")
	}
	absDir, _ := filepath.Abs(".")
	if err := internuv.Remove(absDir, fs.Args(), *dev); err != nil {
		return err
	}
	return syncplan.Sync(absDir, syncplan.Options{Verbose: true})
}

func cmdSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	frozen := fs.Bool("frozen", false, "Fail if lockfile needs updating")
	refresh := fs.Bool("refresh", false, "Force-reinstall all packages")
	fs.Parse(args)
	absDir, _ := filepath.Abs(".")
	return syncplan.Sync(absDir, syncplan.Options{Frozen: *frozen, Refresh: *refresh, Verbose: true})
}

func cmdLock(args []string) error {
	absDir, _ := filepath.Abs(".")
	return internuv.Lock(absDir)
}

func cmdTree(args []string) error {
	absDir, _ := filepath.Abs(".")
	return internuv.Tree(absDir)
}

func cmdGC(args []string) error {
	dryRun := hasFlag(args, "--dry-run") || hasFlag(args, "-n")
	return syncplan.GC(dryRun)
}

// ── Python version management ─────────────────────────────────────────────────

func cmdPython(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt python <list|install|use|remove|which|audit|conflicts|isolation-check>")
	}

	mgr, err := python.New(cwd())
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		versions, err := mgr.List()
		if err != nil {
			return err
		}
		found := false
		for _, v := range versions {
			if len(args) > 1 && args[1] == "--installed" && !v.Installed {
				continue
			}
			active := ""
			if v.Active {
				active = " ← active"
			}
			fmt.Printf("  %-12s %-12s %s%s\n", v.Version, v.Source, v.Path, active)
			found = true
		}
		if !found {
			fmt.Println("No Python versions found. Install one with: molt python install 3.12")
		}

	case "install":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt python install <version>")
		}
		return mgr.Install(args[1])

	case "use":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt python use <version> [--global]")
		}
		global := len(args) > 2 && args[2] == "--global"
		return mgr.Use(args[1], global)

	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt python remove <version>")
		}
		return mgr.Remove(args[1])

	case "which":
		path, err := mgr.Which()
		if err != nil {
			return err
		}
		fmt.Println(path)

	case "audit":
		return mgr.Audit()

	case "conflicts":
		return mgr.ConflictsCheck()

	case "isolation-check":
		return mgr.IsolationCheck()

	default:
		return fmt.Errorf("unknown python subcommand: %s", args[0])
	}
	return nil
}

// ── Task runner ───────────────────────────────────────────────────────────────

func cmdRun(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt run <task|binary> [-- extra-args]")
	}

	taskName := args[0]
	watch := hasFlag(args[1:], "--watch")

	var extraArgs []string
	for i, a := range args[1:] {
		if a == "--" {
			extraArgs = args[i+2:]
			break
		}
	}

	r := tasks.New(cwd())
	if err := r.Run(taskName, watch, extraArgs); err == nil {
		return nil
	} else if !errors.Is(err, tasks.ErrTaskNotFound) {
		return err
	}

	return runExec(cwd(), append([]string{taskName}, args[1:]...))
}

// runExec executes argv under the project's store-derived environment.
func runExec(projectDir string, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("nothing to run")
	}
	spec, err := syspath.Load(projectDir)
	if err != nil {
		return fmt.Errorf("no .molt/syspath.json — run 'molt sync' first (%w)", err)
	}
	bin := argv[0]
	resolved := ""
	if bin == "python" || bin == "python3" {
		resolved = spec.Python
	} else if p := spec.ResolveCommand(bin); p != "" {
		resolved = p
	} else if p, lookErr := exec.LookPath(bin); lookErr == nil {
		resolved = p
	} else {
		return fmt.Errorf("command %q not found in .molt/bin/ or PATH", bin)
	}
	env := spec.BuildEnv(os.Environ())
	return syscall.Exec(resolved, append([]string{resolved}, argv[1:]...), env)
}

func cmdTask(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt task <list|add|remove>")
	}
	r := tasks.New(cwd())
	switch args[0] {
	case "list":
		return r.PrintList()
	case "add":
		if len(args) < 3 {
			return fmt.Errorf("usage: molt task add <name> <command>")
		}
		return r.Add(args[1], strings.Join(args[2:], " "), "")
	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt task remove <name>")
		}
		return r.Remove(args[1])
	default:
		return fmt.Errorf("unknown task subcommand: %s", args[0])
	}
}

// ── Info ──────────────────────────────────────────────────────────────────────

func cmdInfo() error {
	dir := cwd()

	pyprojectPath := filepath.Join(dir, "pyproject.toml")
	data, err := os.ReadFile(pyprojectPath)
	if err != nil {
		fmt.Printf("\nNo molt project here (%s)\n", dir)
		fmt.Println("Run 'molt init' to scaffold one.")
		return nil
	}

	name, ver := "", ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name = ") {
			name = strings.Trim(strings.TrimPrefix(line, "name = "), `"`)
		}
		if strings.HasPrefix(line, "version = ") {
			ver = strings.Trim(strings.TrimPrefix(line, "version = "), `"`)
		}
	}
	if name == "" {
		name = filepath.Base(dir)
	}
	if ver == "" {
		ver = "(no version)"
	}

	pyVer := "(unset — run 'molt python use <version>')"
	if data, err := os.ReadFile(filepath.Join(dir, ".python-version")); err == nil {
		pyVer = strings.TrimSpace(string(data))
	}

	r := tasks.New(dir)
	taskList, _ := r.List()

	fmt.Printf("\n%s %s\n", name, ver)
	fmt.Printf("Python:     %s\n", pyVer)
	fmt.Printf("Platform:   %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("Directory:  %s\n", dir)

	if spec, err := syspath.Load(dir); err == nil {
		fmt.Printf("Env:        %d store path(s); store=~/.molt/pkg\n", len(spec.Syspath))
		fmt.Printf("Python bin: %s\n", spec.Python)
	} else {
		// Differentiate "no deps yet" from "deps declared but not synced".
		// `dependencies = []` (empty array) counts as "no deps yet"; only a
		// non-empty array means we should prompt for sync.
		if hasNonEmptyDeps(string(data)) {
			fmt.Println("Env:        not synced (run 'molt sync')")
		} else {
			fmt.Println("Env:        no dependencies declared yet")
		}
	}

	if len(taskList) > 0 {
		names := make([]string, len(taskList))
		for i, t := range taskList {
			names[i] = t.Name
		}
		fmt.Printf("Tasks:      %s\n", strings.Join(names, ", "))
	}
	fmt.Println()
	return nil
}

// hasNonEmptyDeps reports whether the pyproject.toml content has at least one
// dependency listed under [project] dependencies. Avoids false positives from
// `dependencies = []` (an empty array, no deps yet).
func hasNonEmptyDeps(content string) bool {
	idx := strings.Index(content, "dependencies = [")
	if idx < 0 {
		return false
	}
	rest := content[idx+len("dependencies = ["):]
	end := strings.Index(rest, "]")
	if end < 0 {
		return true // malformed — assume deps to be safe
	}
	return strings.TrimSpace(rest[:end]) != ""
}

// ── uv ────────────────────────────────────────────────────────────────────────

func cmdUV(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt uv <path|version|...uv-args>")
	}

	switch args[0] {
	case "path":
		p, err := uvbin.Find()
		if err != nil {
			return err
		}
		fmt.Println(p)
		return nil

	case "version":
		v, err := uvbin.Version()
		if err != nil {
			return err
		}
		fmt.Println(v)
		return nil

	default:
		return internuv.Raw(".", args)
	}
}

// ── doctor ────────────────────────────────────────────────────────────────────

func cmdDoctor() error {
	v := version
	if v == "" {
		v = "dev"
	}
	fmt.Printf("molt %s\n", v)
	fmt.Printf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println("─────────────────────────────────────")

	uvPath, uvErr := uvbin.Find()
	if uvErr == nil {
		uvVer, _ := uvbin.Version()
		uvVer = strings.TrimSpace(uvVer)
		managed := filepath.Join(func() string {
			h, _ := os.UserHomeDir()
			return h
		}(), ".molt", "uv")
		src := "system PATH"
		if override := os.Getenv(uvbin.EnvOverride); override != "" {
			src = uvbin.EnvOverride + " (override)"
			_ = override
		} else if strings.HasPrefix(uvPath, managed) {
			src = "molt-managed"
		}
		fmt.Printf("  ✓ %-20s %s  [%s — %s]\n", "uv", uvPath, uvVer, src)
	} else {
		fmt.Printf("  ✗ %-20s not found — set %s=/path/to/uv\n", "uv", uvbin.EnvOverride)
	}

	checks := []struct {
		label string
		fn    func() (string, bool)
	}{
		{"python3", func() (string, bool) { p, e := exec.LookPath("python3"); return p, e == nil }},
		{"go", func() (string, bool) { p, e := exec.LookPath("go"); return p, e == nil }},
		{"git", func() (string, bool) { p, e := exec.LookPath("git"); return p, e == nil }},
		{"ldd", func() (string, bool) { p, e := exec.LookPath("ldd"); return p, e == nil }},
		{"curl", func() (string, bool) { p, e := exec.LookPath("curl"); return p, e == nil }},
	}
	for _, c := range checks {
		loc, ok := c.fn()
		sym := "✓"
		if !ok {
			sym = "✗"
			loc = "not found"
		}
		fmt.Printf("  %s %-20s %s\n", sym, c.label, loc)
	}
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func cwd() string {
	d, _ := os.Getwd()
	return d
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
