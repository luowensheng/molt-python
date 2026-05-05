package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"

	"molt/internal/adopt"
	"molt/internal/builder"
	"molt/internal/editor"
	"molt/internal/integrity"
	"molt/internal/projstate"
	"molt/internal/python"
	"molt/internal/store"
	"molt/internal/syncplan"
	"molt/internal/syspath"
	"molt/internal/tasks"
	"molt/internal/templates"
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
	case "package":
		err = cmdPackage(os.Args[2:])
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
	case "template":
		err = cmdTemplate(os.Args[2:])
	case "where":
		err = cmdWhere(os.Args[2:])
	case "editor":
		err = cmdEditor(os.Args[2:])

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
  init     [flags] [name]          Scaffold a new Python project (default: bare)
                                     --template <name>  apply a template
                                     --python <ver>     pin a Python version
                                     --no-lock          skip uv lock
  template list                    List available templates (built-in + user)
  template show <name>             Show a template's metadata + file tree
  template add <name> <path>       Register a directory as a user template
  template remove <name>           Delete a user template

Path discovery:
  where                            Print every path molt knows about
  where <key>                      Print one (state|python|bin|syspath|sitecustomize|uv-env|store)

Editor integration:
  editor [vscode|pyright]          Write/refresh editor configs (auto-detect if no name)
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
  python run <args...>             Run the project's Python interpreter
  python -v <ver> run <args...>    Run a specific Python version (auto-installs)
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
                                     --output <path>  output path (default: <name>)
                                     --os/--arch      cross-build target
  package  [flags]                 Build Python wheel + sdist (PyPI artifacts)
                                     --output <dir>   output dir (default: dist)
                                     --sdist | --wheel  limit to one artifact
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
		// Default to just `<name>` in cwd. The version is in the embedded
		// manifest and exposed via `<bin> molt version` — no need to
		// version-stamp the filename. No implicit `dist/` either; users
		// can pass `--output dist/foo` if they want one.
		*output = *name
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

// cmdPackage builds Python distribution artefacts (wheel + sdist) suitable
// for upload to PyPI. Thin wrapper over `uv build`. Output goes to dist/
// per uv defaults; --sdist or --wheel limit to one artefact.
func cmdPackage(args []string) error {
	fs := flag.NewFlagSet("package", flag.ExitOnError)
	output := fs.String("output", "dist", "Output directory")
	sdistOnly := fs.Bool("sdist", false, "Build only the sdist (.tar.gz)")
	wheelOnly := fs.Bool("wheel", false, "Build only the wheel (.whl)")
	fs.Parse(args)

	absDir, _ := filepath.Abs(".")
	uvArgs := []string{"build", "--out-dir", *output}
	switch {
	case *sdistOnly && *wheelOnly:
		return fmt.Errorf("--sdist and --wheel are mutually exclusive")
	case *sdistOnly:
		uvArgs = append(uvArgs, "--sdist")
	case *wheelOnly:
		uvArgs = append(uvArgs, "--wheel")
	}
	if err := internuv.Raw(absDir, uvArgs); err != nil {
		return err
	}
	fmt.Printf("✓ Packaged to %s/ — upload with `uv publish` or `twine upload %s/*`\n",
		*output, *output)
	return nil
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
	tmplName := fs.String("template", "bare", "Template to apply (run `molt template list`)")
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
		return fmt.Errorf("usage: molt init [--template <name>] [--python <ver>] [--no-lock] [name]")
	}

	tmpl, err := templates.Lookup(*tmplName)
	if err != nil {
		return fmt.Errorf("template %q: %w (run 'molt template list' to see available templates)", *tmplName, err)
	}

	fmt.Printf("Initialising project %q (template: %s)...\n", name, tmpl.Name)
	// uv init always runs first — it generates pyproject.toml, .python-version,
	// README.md and (depending on flags) hello.py or src/<pkg>/. The template
	// is applied on top: it can request --lib mode, instruct molt to clean up
	// uv's hello.py placeholder, and add files of its own.
	if err := internuv.Init(".", initName, internuv.InitOptions{Python: *pyVersion, Lib: tmpl.Meta.UseUvLib}); err != nil {
		return fmt.Errorf("uv init: %w", err)
	}

	if !tmpl.Meta.KeepHello {
		_ = os.Remove(filepath.Join(dir, "hello.py"))
	}

	pkg := pyPackageName(name)
	if err := tmpl.Apply(dir, templates.Vars{Name: name, Pkg: pkg}); err != nil {
		return fmt.Errorf("apply template %q: %w", tmpl.Name, err)
	}

	// Inject [tool.molt.tasks] from the template metadata, if any.
	if strings.TrimSpace(tmpl.Meta.Tasks) != "" {
		if err := injectTasks(filepath.Join(dir, "pyproject.toml"), tmpl.Meta.Tasks, name, pkg); err != nil {
			fmt.Fprintf(os.Stderr, "warn: inject tasks: %v\n", err)
		}
	}

	// Add packages requested by the template (deferred to avoid a sync per
	// add — `molt sync` will materialise everything once at the end).
	if len(tmpl.Meta.Add) > 0 {
		if err := internuv.Add(dir, tmpl.Meta.Add, internuv.AddOptions{}); err != nil {
			return fmt.Errorf("template add: %w", err)
		}
	}
	if len(tmpl.Meta.AddDev) > 0 {
		if err := internuv.Add(dir, tmpl.Meta.AddDev, internuv.AddOptions{Dev: true}); err != nil {
			return fmt.Errorf("template add --dev: %w", err)
		}
	}

	if !*noLock {
		if err := internuv.Lock(dir); err != nil {
			return fmt.Errorf("uv lock: %w", err)
		}
	}

	// uv init skips .gitignore when the directory is already inside a git
	// repo, and when it does create one it lists .venv — which molt never
	// produces. Ensure .gitignore exists and contains .molt/ instead.
	if err := patchGitignore(dir); err != nil {
		fmt.Fprintf(os.Stderr, "warn: could not write .gitignore: %v\n", err)
	}
	fmt.Println("✓ Done.")
	return nil
}

// pyPackageName normalises a project name into a valid Python package name:
// dashes and dots become underscores, lowercase.
func pyPackageName(name string) string {
	r := strings.NewReplacer("-", "_", ".", "_", " ", "_")
	return strings.ToLower(r.Replace(name))
}

// injectTasks appends a TOML chunk under [tool.molt.tasks] in pyproject.toml,
// creating the section if absent. Substitutes {{name}} and {{pkg}} in the
// chunk so templates can reference the project they're being applied to.
func injectTasks(pyprojectPath, chunk, name, pkg string) error {
	data, err := os.ReadFile(pyprojectPath)
	if err != nil {
		return err
	}
	body := strings.NewReplacer("{{name}}", name, "{{pkg}}", pkg).Replace(chunk)
	body = strings.TrimSpace(body) + "\n"

	content := string(data)
	if strings.Contains(content, "[tool.molt.tasks]") {
		content = strings.Replace(content, "[tool.molt.tasks]\n",
			"[tool.molt.tasks]\n"+body, 1)
	} else {
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "\n[tool.molt.tasks]\n" + body
	}
	return os.WriteFile(pyprojectPath, []byte(content), 0o644)
}

// patchGitignore strips uv-written `.venv` entries from <dir>/.gitignore
// (molt doesn't produce a .venv) and creates the file if missing. It used
// to also add `.molt/` — that's gone now since per-project state lives at
// ~/.molt/projects/. Existing `.molt/` entries are left alone for projects
// migrating from older molt versions.
func patchGitignore(dir string) error {
	path := filepath.Join(dir, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	content := string(data)

	var kept []string
	changed := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == ".venv" || trimmed == ".venv/" {
			changed = true
			continue
		}
		kept = append(kept, line)
	}
	if !changed && len(data) > 0 {
		return nil // nothing to do
	}
	out := strings.Join(kept, "\n")
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return os.WriteFile(path, []byte(out), 0o644)
}

// reqFilesFlag accumulates -r requirements.txt entries so the flag can
// appear multiple times on the same command line.
type reqFilesFlag []string

func (r *reqFilesFlag) String() string     { return strings.Join(*r, ",") }
func (r *reqFilesFlag) Set(s string) error { *r = append(*r, s); return nil }

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	dev := fs.Bool("dev", false, "Dev dependency")
	var reqFiles reqFilesFlag
	fs.Var(&reqFiles, "r", "Requirements file (can be repeated)")
	fs.Parse(args)
	if fs.NArg() == 0 && len(reqFiles) == 0 {
		return fmt.Errorf("usage: molt add [--dev] [-r requirements.txt] [package...]")
	}
	absDir, _ := filepath.Abs(".")
	if err := internuv.Add(absDir, fs.Args(), internuv.AddOptions{Dev: *dev, RequirementFiles: reqFiles}); err != nil {
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
	// Pull out a `-v <version>` / `--python <version>` flag from anywhere in
	// args so users can write either:
	//   molt python -v 3.12 run -c '...'      (version-first)
	//   molt python run -v 3.12 -c '...'      (subcommand-first)
	// The flag only applies to `run`; other subcommands ignore it.
	pyVersion, args := extractPythonVersionFlag(args)

	if len(args) == 0 {
		return fmt.Errorf("usage: molt python <list|install|use|remove|which|run|audit|conflicts|isolation-check>")
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

	case "run":
		// Override path: -v <ver> resolves a specific Python via uv and
		// execs it with a CLEAN env (no project PYTHONPATH). This is the
		// "raw Python at version X" mode — useful for stdlib-only ad-hoc
		// scripts. It deliberately does NOT inject the project's store
		// dirs because they're built for the project's pinned ABI.
		if pyVersion != "" {
			pyExe, err := resolvePythonVersion(pyVersion)
			if err != nil {
				return err
			}
			return syscall.Exec(pyExe, append([]string{pyExe}, args[1:]...), cleanPythonEnv(os.Environ()))
		}
		// Default path: invoke the project's Python interpreter directly.
		// Auto-syncs if the project hasn't been materialised yet, so this
		// works immediately after `molt init` with no extra steps.
		if err := ensureSynced(cwd()); err != nil {
			return err
		}
		spec, err := syspath.Load(cwd())
		if err != nil {
			return fmt.Errorf("no .molt/syspath.json — run 'molt sync' first (%w)", err)
		}
		env := spec.BuildEnv(os.Environ())
		argv := append([]string{spec.Python}, args[1:]...)
		return syscall.Exec(spec.Python, argv, env)

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

	// Auto-sync on first run: a freshly-init'd project has no
	// .molt/syspath.json, so any task that uses `python` (or any console
	// shim) would fail with "command not found". Sync once to materialise
	// the env, then proceed.
	if err := ensureSynced(cwd()); err != nil {
		return err
	}

	// Single-file script mode: `molt run main.py [args...]` — exec the
	// project's Python interpreter on the script. Detected by .py suffix
	// + file existing on disk. Lets users run a project with nothing but
	// pyproject.toml + main.py, no task definition required.
	if strings.HasSuffix(taskName, ".py") {
		if _, err := os.Stat(taskName); err == nil {
			return runPythonScript(cwd(), taskName, args[1:])
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

// runPythonScript execs spec.Python on a script path under the project env.
// Used for the `molt run main.py` single-file flow.
func runPythonScript(projectDir, script string, scriptArgs []string) error {
	spec, err := syspath.Load(projectDir)
	if err != nil {
		return fmt.Errorf("no .molt/syspath.json — run 'molt sync' first (%w)", err)
	}
	abs, _ := filepath.Abs(script)
	env := spec.BuildEnv(os.Environ())
	argv := append([]string{spec.Python, abs}, scriptArgs...)
	return syscall.Exec(spec.Python, argv, env)
}

// ensureSynced runs `molt sync` if .molt/syspath.json is missing. No-op when
// already synced. Used to remove the "did you remember to sync?" gotcha
// after `molt init` — the user's first `molt run` should just work.
func ensureSynced(projectDir string) error {
	if _, err := syspath.Load(projectDir); err == nil {
		return nil
	}
	// Only auto-sync if there's a project here at all.
	if _, err := os.Stat(filepath.Join(projectDir, "pyproject.toml")); err != nil {
		return nil
	}
	fmt.Fprintln(os.Stderr, "→ first run, syncing project…")
	return syncplan.Sync(projectDir, syncplan.Options{Verbose: true})
}

// runExec executes argv under the project's store-derived environment.
// `python`/`python3` resolve directly to spec.Python — molt never depends
// on a system `python` being on PATH.
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

// ── Templates ─────────────────────────────────────────────────────────────────

func cmdTemplate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt template <list|show|add|remove>")
	}
	switch args[0] {
	case "list":
		return cmdTemplateList()
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt template show <name>")
		}
		return cmdTemplateShow(args[1])
	case "add":
		if len(args) < 3 {
			return fmt.Errorf("usage: molt template add <name> <path>")
		}
		return cmdTemplateAdd(args[1], args[2])
	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt template remove <name>")
		}
		if err := templates.Remove(args[1]); err != nil {
			return err
		}
		fmt.Printf("✓ removed user template %q\n", args[1])
		return nil
	default:
		return fmt.Errorf("unknown template subcommand: %s", args[0])
	}
}

func cmdTemplateList() error {
	ts, err := templates.List()
	if err != nil {
		return err
	}
	if len(ts) == 0 {
		fmt.Println("No templates available.")
		return nil
	}
	fmt.Println("Available templates:")
	fmt.Println()
	for _, t := range ts {
		desc := t.Meta.Description
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Printf("  %-12s %-9s %s\n", t.Name, "["+t.Source+"]", desc)
	}
	udir, _ := templates.UserDir()
	fmt.Printf("\nUser templates dir: %s\n", udir)
	fmt.Println("Apply with:        molt init --template <name>")
	return nil
}

func cmdTemplateShow(name string) error {
	t, err := templates.Lookup(name)
	if err != nil {
		return err
	}
	fmt.Printf("Template: %s [%s]\n", t.Name, t.Source)
	if t.Meta.Description != "" {
		fmt.Printf("  %s\n", t.Meta.Description)
	}
	if t.Meta.UseUvLib {
		fmt.Println("  (runs `uv init --lib`)")
	}
	if t.Meta.KeepHello {
		fmt.Println("  (keeps uv's hello.py)")
	}
	if len(t.Meta.Add) > 0 {
		fmt.Printf("  add:     %s\n", strings.Join(t.Meta.Add, ", "))
	}
	if len(t.Meta.AddDev) > 0 {
		fmt.Printf("  add-dev: %s\n", strings.Join(t.Meta.AddDev, ", "))
	}
	fmt.Println("\nFiles:")
	return iofs.WalkDir(t.Root, ".", func(p string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		marker := ""
		if d.IsDir() {
			marker = "/"
		}
		fmt.Printf("  %s%s\n", p, marker)
		return nil
	})
}

func cmdTemplateAdd(name, path string) error {
	if err := templates.AddTo(name, path); err != nil {
		return err
	}
	udir, _ := templates.UserDir()
	fmt.Printf("✓ added user template %q from %s\n  → %s\n", name, path, filepath.Join(udir, name))
	return nil
}

// ── Path discovery ────────────────────────────────────────────────────────────

func cmdWhere(args []string) error {
	proj := cwd()

	// Single-key form: print one path + newline. Script-friendly.
	if len(args) >= 1 {
		key := args[0]
		path, err := wherePath(proj, key)
		if err != nil {
			return err
		}
		fmt.Println(path)
		return nil
	}

	// Tabular form: every path molt knows about for this project.
	type row struct{ label, path, note string }
	rows := []row{
		{"state", projstate.Dir(proj), ""},
		{"bin", projstate.Bin(proj), ""},
		{"syspath", projstate.Syspath(proj), ""},
		{"sitecustomize", projstate.SiteCustomize(proj), ""},
		{"uv-env", projstate.UvEnv(proj), ""},
	}
	if spec, err := syspath.Load(proj); err == nil {
		rows = append([]row{{"python", spec.Python, ""}}, rows...)
	} else {
		rows = append([]row{{"python", "(not synced — run 'molt sync')", ""}}, rows...)
	}
	if st, err := store.Default(); err == nil {
		rows = append(rows, row{"store", st.Root, ""})
	}
	if legacy, ok := projstate.LegacyInTreeDir(proj); ok {
		rows = append(rows, row{"legacy", legacy, "(in-tree state from old molt — safe to remove)"})
	}
	for _, r := range rows {
		fmt.Printf("  %-14s %s", r.label, r.path)
		if r.note != "" {
			fmt.Printf("  %s", r.note)
		}
		fmt.Println()
	}
	return nil
}

// wherePath resolves a single key. Path-derived keys never fail; keys that
// require a successful sync (`python`, `syspath`) error if syspath.json is
// missing.
func wherePath(proj, key string) (string, error) {
	switch key {
	case "state":
		return projstate.Dir(proj), nil
	case "bin":
		return projstate.Bin(proj), nil
	case "sitecustomize":
		return projstate.SiteCustomize(proj), nil
	case "uv-env":
		return projstate.UvEnv(proj), nil
	case "syspath":
		return projstate.Syspath(proj), nil
	case "python":
		spec, err := syspath.Load(proj)
		if err != nil {
			return "", fmt.Errorf("not synced — run 'molt sync' first")
		}
		return spec.Python, nil
	case "store":
		st, err := store.Default()
		if err != nil {
			return "", err
		}
		return st.Root, nil
	default:
		return "", fmt.Errorf(
			"unknown key %q (valid: state, python, bin, syspath, sitecustomize, uv-env, store)",
			key)
	}
}

// ── Editor integration ────────────────────────────────────────────────────────

func cmdEditor(args []string) error {
	fs := flag.NewFlagSet("editor", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite VS Code settings even if it contains JSON comments (jsonc)")
	fs.Parse(args)
	rest := fs.Args()

	proj := cwd()
	spec, err := syspath.Load(proj)
	if err != nil {
		return fmt.Errorf("project not synced — run 'molt sync' first (no syspath.json)")
	}

	// No name → auto-detect from existing files.
	if len(rest) == 0 {
		done, err := editor.AutoDetect(proj, spec)
		if err != nil {
			return err
		}
		if len(done) == 0 {
			fmt.Println("No editor configs found. Pass `vscode` or `pyright` explicitly:")
			fmt.Println("  molt editor vscode    # writes .vscode/settings.json")
			fmt.Println("  molt editor pyright   # writes pyrightconfig.json")
			return nil
		}
		for _, name := range done {
			fmt.Printf("✓ refreshed %s config\n", name)
		}
		return nil
	}

	switch rest[0] {
	case "vscode":
		if err := editor.WriteVSCode(proj, spec, *force); err != nil {
			return err
		}
		fmt.Printf("✓ wrote %s\n", filepath.Join(proj, ".vscode", "settings.json"))
		return nil
	case "pyright":
		if err := editor.WritePyright(proj, spec); err != nil {
			return err
		}
		fmt.Printf("✓ wrote %s\n", filepath.Join(proj, "pyrightconfig.json"))
		return nil
	default:
		return fmt.Errorf("unknown editor: %s (valid: vscode, pyright)", rest[0])
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
	fmt.Printf("State dir:  %s\n", projstate.Dir(dir))

	if spec, err := syspath.Load(dir); err == nil {
		fmt.Printf("Env:        %d store path(s); store=~/.molt/pkg\n", len(spec.Syspath))
		fmt.Printf("Python bin: %s\n", spec.Python)
	} else {
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

	// One-line nudge if a stale in-tree .molt/ from old molt is still around.
	if legacy, ok := projstate.LegacyInTreeDir(dir); ok {
		fmt.Printf("\nnote: legacy in-tree state at %s — safe to `rm -rf %s`\n", legacy, legacy)
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

// extractPythonVersionFlag pulls `-v <ver>` or `--python <ver>` out of args
// and returns the version (or "") plus args with that pair removed.
// Stops scanning at "--" so user-supplied script args aren't accidentally
// consumed (e.g. `molt python run -- -v` should pass -v to the script).
func extractPythonVersionFlag(args []string) (string, []string) {
	out := make([]string, 0, len(args))
	version := ""
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		if (a == "-v" || a == "--python") && i+1 < len(args) {
			version = args[i+1]
			i += 2
			continue
		}
		out = append(out, a)
		i++
	}
	return version, out
}

// resolvePythonVersion uses uv to find an interpreter for a given version
// spec (e.g. "3.12", "3.12.3"). Auto-installs via uv if not present, since
// molt's promise is "no external Python required". Runs uv from a neutral
// cwd to avoid uv creating an unwanted project venv.
func resolvePythonVersion(version string) (string, error) {
	uv, err := uvbin.Ensure()
	if err != nil {
		return "", err
	}
	tmp := os.TempDir()
	// Try to find first; if not installed, install then find.
	find := exec.Command(uv, "python", "find", version)
	find.Dir = tmp
	out, err := find.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	fmt.Fprintf(os.Stderr, "→ installing Python %s (via uv)…\n", version)
	install := exec.Command(uv, "python", "install", version)
	install.Dir = tmp
	install.Stdout = os.Stderr
	install.Stderr = os.Stderr
	if err := install.Run(); err != nil {
		return "", fmt.Errorf("uv python install %s: %w", version, err)
	}
	find = exec.Command(uv, "python", "find", version)
	find.Dir = tmp
	out, err = find.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("uv python find %s after install: %w (output: %s)",
			version, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// cleanPythonEnv returns parent with VIRTUAL_ENV / PYTHONHOME / PYTHONPATH
// stripped — used when `molt python -v <ver> run` execs a Python that is
// NOT the project's pinned interpreter. We don't inject the project's
// store dirs because they're ABI-specific to the project's Python.
func cleanPythonEnv(parent []string) []string {
	out := make([]string, 0, len(parent))
	for _, kv := range parent {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			out = append(out, kv)
			continue
		}
		switch kv[:i] {
		case "VIRTUAL_ENV", "PYTHONHOME", "PYTHONPATH":
			continue
		}
		out = append(out, kv)
	}
	return out
}
