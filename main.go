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
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"molt/internal/adopt"
	"molt/internal/builder"
	"molt/internal/editor"
	"molt/internal/integrity"
	"molt/internal/projstate"
	"molt/internal/python"
	"molt/internal/store"
	"molt/internal/tooldb"
	"molt/internal/nativepreset"
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

	// globalProjectOverride holds the absolute path of a project resolved
	// from a `--project <q>` flag at the top of the args. When non-empty,
	// commands that would normally operate on the cwd's project (run,
	// sync, add, info, where, etc.) operate on this path instead. Set
	// once in main() before dispatch; never mutated thereafter.
	globalProjectOverride string
)

// projectRoot returns the absolute project path that "the current command"
// should target. When the `--project` flag is set, that wins; otherwise
// fall back to cwd. Commands that take a project as a positional arg
// (init, build, package, adopt, capture, assemble) intentionally do NOT
// call this — they always work on cwd / the user-provided arg.
func projectRoot() string {
	if globalProjectOverride != "" {
		return globalProjectOverride
	}
	d, _ := os.Getwd()
	return d
}

func main() {
	// Resolve --project / -p before dispatch. Position-flexible: works as
	// `molt --project foo run dev` or `molt run --project foo dev`. Stops
	// at `--`. After this call, os.Args is rewritten with the flag pair
	// removed so each command's own flag parsing sees a clean slice.
	if rewritten, err := applyProjectFlag(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	} else {
		os.Args = rewritten
	}

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
	case "project":
		err = cmdProject(os.Args[2:])
	case "tool":
		err = cmdTool(os.Args[2:])
	case "where":
		err = cmdWhere(os.Args[2:])
	case "editor":
		err = cmdEditor(os.Args[2:])

	// ── Native presets ────────────────────────────────────────────────────
	case "native-preset":
		err = cmdNativePreset(os.Args[2:])

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

Multi-project:
  project list                     List every registered project
  project info <q>                 Detailed view of one project
  project where <q> [<key>]        Same keys as 'where', for a remote project
  project purge <q>                Drop tracking + state (source untouched)
  project purge --older-than <d>   Bulk purge by age (e.g. 30d, 8w)
  project purge --unused           Bulk purge projects whose source is missing
  project purge --dry-run          Preview without removing
  project reinit <path>            Re-register a previously purged project
  project cd <q>                   Print the project's source path

Tool registry (~/.molt/bin global shims):
  tool install [<path>]            Register the project's CLI globally
                                     --name <n>   tool name (default: project basename)
                                     --task <t>   task to run (default: default entry)
  tool list                        List registered tools
  tool show <name>                 Show one tool's details
  tool uninstall <name>            Remove a tool's shim + metadata
  tool path                        Print ~/.molt/bin (add this to your PATH)

Global flags:
  --project / -p <q>               Operate on a registered project from anywhere
                                     (skipped for: init, build, package, adopt, capture, assemble)

Dependencies & sync:
  add      [--dev] [-r <file>] <pkg...>   Add dependency (or from requirements.txt)
  remove   [--dev] <pkg...>        Remove dependency
  sync     [--frozen] [--refresh]  Install lockfile into ~/.molt/pkg + write per-project state
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

	absDir := projectRoot()
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
	absDir := projectRoot()
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
	absDir := projectRoot()
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
	absDir := projectRoot()
	return syncplan.Sync(absDir, syncplan.Options{Frozen: *frozen, Refresh: *refresh, Verbose: true})
}

func cmdLock(args []string) error {
	absDir := projectRoot()
	return internuv.Lock(absDir)
}

func cmdTree(args []string) error {
	absDir := projectRoot()
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

	mgr, err := python.New(projectRoot())
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
		if err := ensureSynced(projectRoot()); err != nil {
			return err
		}
		spec, err := syspath.Load(projectRoot())
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
	// `--` escape: treat everything after `--` as args to the default
	// entry. `molt run --` → default entry, no args. `molt run -- --version`
	// → default entry with ["--version"]. Mirrors the launcher's escape.
	if len(args) > 0 && args[0] == "--" {
		return runDefaultEntryWithArgs(args[1:])
	}
	// Flag-shaped first arg (e.g. `demo --port 8080`) means the user is
	// passing flags to their program, not naming a task. Forward verbatim
	// to the default entry. Tasks are always plain positional names.
	if len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "--watch" {
		return runDefaultEntryWithArgs(args)
	}
	if len(args) == 0 {
		// No task / script given. Auto-resolve to the project's default
		// entry: main.py at the root, or `python -m <pkg>` for src layout.
		// This is what `<bin>` (no args) does for built binaries; mirror it
		// here so `molt run` and the tool shim form behave the same.
		return runDefaultEntry()
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
	if err := ensureSynced(projectRoot()); err != nil {
		return err
	}

	// Single-file script mode: `molt run main.py [args...]` — exec the
	// project's Python interpreter on the script. Detected by .py suffix
	// + file existing on disk. Lets users run a project with nothing but
	// pyproject.toml + main.py, no task definition required.
	//
	// When --project redirects, resolve the script relative to the project
	// root (not cwd) so the same command works regardless of where the user
	// invokes it from.
	if strings.HasSuffix(taskName, ".py") {
		candidates := []string{taskName}
		if !filepath.IsAbs(taskName) {
			candidates = append(candidates, filepath.Join(projectRoot(), taskName))
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return runPythonScript(projectRoot(), c, args[1:])
			}
		}
	}

	r := tasks.New(projectRoot())
	if err := r.Run(taskName, watch, extraArgs); err == nil {
		return nil
	} else if !errors.Is(err, tasks.ErrTaskNotFound) {
		return err
	}

	return runExec(projectRoot(), append([]string{taskName}, args[1:]...))
}

// runDefaultEntry is the no-args form. Equivalent to runDefaultEntryWithArgs(nil).
func runDefaultEntry() error { return runDefaultEntryWithArgs(nil) }

// runDefaultEntryWithArgs dispatches the project's default entry with the
// given trailing args. Mirrors the built binary's no-args behaviour:
// detect the entry from the layout and exec it.
func runDefaultEntryWithArgs(extraArgs []string) error {
	proj := projectRoot()
	if err := ensureSynced(proj); err != nil {
		return err
	}
	// Prefer main.py at the root.
	mainPy := filepath.Join(proj, "main.py")
	if _, err := os.Stat(mainPy); err == nil {
		return runPythonScript(proj, mainPy, extraArgs)
	}
	// Try src/<pkg>/__main__.py and <pkg>/__main__.py.
	pkg := pyPackageName(filepath.Base(proj))
	for _, c := range []string{
		filepath.Join(proj, "src", pkg, "__main__.py"),
		filepath.Join(proj, pkg, "__main__.py"),
	} {
		if _, err := os.Stat(c); err == nil {
			// `python -m <pkg> [extra...]` — exec via spec.Python.
			spec, err := syspath.Load(proj)
			if err != nil {
				return err
			}
			env := spec.BuildEnv(os.Environ())
			argv := append([]string{spec.Python, "-m", pkg}, extraArgs...)
			return syscall.Exec(spec.Python, argv, env)
		}
	}
	return fmt.Errorf("no default entry found at %s — expected main.py or src/%s/__main__.py", proj, pkg)
}

// runPythonScript execs spec.Python on a script path under the project env.
// Used for the `molt run main.py` single-file flow.
func runPythonScript(projectDir, script string, scriptArgs []string) error {
	nativeCheckAndSync(projectDir)
	spec, err := syspath.Load(projectDir)
	if err != nil {
		return fmt.Errorf("no .molt/syspath.json — run 'molt sync' first (%w)", err)
	}
	abs, _ := filepath.Abs(script)
	env := spec.BuildEnv(os.Environ())
	argv := append([]string{spec.Python, abs}, scriptArgs...)
	return syscall.Exec(spec.Python, argv, env)
}

// nativeCheckAndSync checks whether any native sources (.pyx, .rs, or
// [[tool.molt.native]] entries) have changed since the last sync. If so, it
// runs a full sync to recompile and re-wire the syspath. Fast no-op when
// nothing changed.
func nativeCheckAndSync(projectDir string) {
	if syncplan.NativeChanged(projectDir) {
		fmt.Fprintln(os.Stderr, "→ native sources changed, recompiling…")
		if err := syncplan.Sync(projectDir, syncplan.Options{Verbose: true}); err != nil {
			fmt.Fprintf(os.Stderr, "warn: native sync: %v\n", err)
		}
	}
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
	if bin == "python" || bin == "python3" {
		nativeCheckAndSync(projectDir)
	}
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
	r := tasks.New(projectRoot())
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

// ── Multi-project ops ────────────────────────────────────────────────────────

func cmdProject(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt project <list|info|where|purge|reinit|cd>")
	}
	switch args[0] {
	case "list":
		return cmdProjectList()
	case "info":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt project info <name|hash|path>")
		}
		return cmdProjectInfo(args[1])
	case "where":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt project where <name|hash|path> [<key>]")
		}
		key := ""
		if len(args) >= 3 {
			key = args[2]
		}
		return cmdProjectWhere(args[1], key)
	case "purge":
		return cmdProjectPurge(args[1:])
	case "reinit":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt project reinit <path>")
		}
		return cmdProjectReinit(args[1])
	case "cd":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt project cd <name|hash|path>")
		}
		entry, err := projstate.Resolve(args[1])
		if err != nil {
			return err
		}
		fmt.Println(entry.ProjectDir)
		return nil
	default:
		return fmt.Errorf("unknown project subcommand: %s", args[0])
	}
}

func cmdProjectList() error {
	entries, err := projstate.ListAll()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("No registered projects. Run `molt sync` in a project to register it.")
		return nil
	}
	// Sort: alive first, then by LastSync desc.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].ProjectAlive != entries[j].ProjectAlive {
			return entries[i].ProjectAlive
		}
		return entries[i].Meta.LastSync.After(entries[j].Meta.LastSync)
	})
	fmt.Printf("%-22s %-18s %-20s %-15s %s\n", "NAME", "HASH", "LAST SYNC", "STATE", "PATH")
	for _, e := range entries {
		name := filepath.Base(e.ProjectDir)
		if name == "" {
			name = "(no path)"
		}
		state := "✓ alive"
		if !e.ProjectAlive {
			state = "✗ source missing"
		}
		last := "(never)"
		if !e.Meta.LastSync.IsZero() {
			last = e.Meta.LastSync.Local().Format("2006-01-02 15:04:05")
		}
		fmt.Printf("%-22s %-18s %-20s %-15s %s\n", name, e.Hash, last, state, e.ProjectDir)
	}
	fmt.Printf("\n%d project(s); state at %s\n", len(entries), projstate.Root())
	return nil
}

func cmdProjectInfo(query string) error {
	entry, err := projstate.Resolve(query)
	if err != nil {
		return err
	}
	fmt.Printf("Name:        %s\n", filepath.Base(entry.ProjectDir))
	fmt.Printf("Path:        %s\n", entry.ProjectDir)
	fmt.Printf("Hash:        %s\n", entry.Hash)
	fmt.Printf("State dir:   %s\n", entry.Dir)
	if !entry.Meta.Created.IsZero() {
		fmt.Printf("Created:     %s\n", entry.Meta.Created.Local().Format(time.RFC3339))
	}
	if !entry.Meta.LastSync.IsZero() {
		fmt.Printf("Last sync:   %s\n", entry.Meta.LastSync.Local().Format(time.RFC3339))
	}
	if entry.ProjectAlive {
		fmt.Println("Source:      ✓ alive")
	} else {
		fmt.Println("Source:      ✗ missing — pyproject.toml not found at original path")
	}
	if spec, err := syspath.Load(entry.ProjectDir); err == nil {
		fmt.Printf("Python bin:  %s\n", spec.Python)
		fmt.Printf("Env:         %d store path(s)\n", len(spec.Syspath))
	}
	return nil
}

func cmdProjectWhere(query, key string) error {
	entry, err := projstate.Resolve(query)
	if err != nil {
		return err
	}
	if key != "" {
		path, err := wherePath(entry.ProjectDir, key)
		if err != nil {
			return err
		}
		fmt.Println(path)
		return nil
	}
	// No key — labelled table for the resolved project.
	pairs := [][2]string{
		{"path", entry.ProjectDir},
		{"state", projstate.Dir(entry.ProjectDir)},
		{"bin", projstate.Bin(entry.ProjectDir)},
		{"syspath", projstate.Syspath(entry.ProjectDir)},
		{"sitecustomize", projstate.SiteCustomize(entry.ProjectDir)},
		{"uv-env", projstate.UvEnv(entry.ProjectDir)},
	}
	if spec, err := syspath.Load(entry.ProjectDir); err == nil {
		pairs = append([][2]string{{"python", spec.Python}}, pairs...)
	}
	for _, p := range pairs {
		fmt.Printf("  %-14s %s\n", p[0], p[1])
	}
	return nil
}

func cmdProjectPurge(args []string) error {
	fs := flag.NewFlagSet("project purge", flag.ExitOnError)
	olderThan := fs.String("older-than", "", "Bulk purge: projects whose last sync is older than this duration (e.g. 30d, 12h)")
	unused := fs.Bool("unused", false, "Bulk purge: projects whose source dir no longer exists")
	dryRun := fs.Bool("dry-run", false, "Preview only; nothing is removed")
	stateOnly := fs.Bool("state-only", false, "Keep registry entry; remove only the materialised state dir")
	yes := fs.Bool("yes", false, "Skip confirmation prompt for bulk operations")
	fs.Parse(args)

	// Decide which entries to act on.
	bulk := *olderThan != "" || *unused
	var targets []projstate.Entry

	if bulk {
		all, err := projstate.ListAll()
		if err != nil {
			return err
		}
		var cutoff time.Time
		if *olderThan != "" {
			d, err := parseDuration(*olderThan)
			if err != nil {
				return fmt.Errorf("--older-than %q: %w", *olderThan, err)
			}
			cutoff = time.Now().Add(-d)
		}
		for _, e := range all {
			if *unused && !e.ProjectAlive {
				targets = append(targets, e)
				continue
			}
			if !cutoff.IsZero() && !e.Meta.LastSync.IsZero() && e.Meta.LastSync.Before(cutoff) {
				targets = append(targets, e)
			}
		}
		if fs.NArg() > 0 {
			return fmt.Errorf("--older-than / --unused don't take a positional name argument")
		}
	} else {
		if fs.NArg() == 0 {
			return fmt.Errorf("usage: molt project purge <name|hash|path>\n   or: molt project purge --older-than <duration>\n   or: molt project purge --unused")
		}
		entry, err := projstate.Resolve(fs.Arg(0))
		if err != nil {
			return err
		}
		targets = []projstate.Entry{entry}
	}

	if len(targets) == 0 {
		fmt.Println("No projects matched.")
		return nil
	}

	// Print and possibly confirm.
	fmt.Printf("%d project(s) to purge:\n", len(targets))
	for _, e := range targets {
		fmt.Printf("  - %s [%s]  %s\n", filepath.Base(e.ProjectDir), e.Hash, e.ProjectDir)
	}
	if *dryRun {
		fmt.Println("(dry-run — nothing removed)")
		return nil
	}
	if bulk && !*yes {
		fmt.Print("Proceed? [y/N] ")
		var resp string
		fmt.Scanln(&resp)
		if !strings.EqualFold(strings.TrimSpace(resp), "y") && !strings.EqualFold(strings.TrimSpace(resp), "yes") {
			fmt.Println("Aborted.")
			return nil
		}
	}

	for _, e := range targets {
		if err := os.RemoveAll(e.Dir); err != nil {
			fmt.Fprintf(os.Stderr, "warn: remove %s: %v\n", e.Dir, err)
			continue
		}
		if !*stateOnly {
			if err := registryDrop(e.ProjectDir); err != nil {
				fmt.Fprintf(os.Stderr, "warn: drop registry for %s: %v\n", e.ProjectDir, err)
			}
		}
		fmt.Printf("✓ purged %s\n", filepath.Base(e.ProjectDir))
	}
	return nil
}

// registryDrop removes a project from ~/.molt/registry.json. Best-effort —
// missing registry / missing entry are silent successes.
func registryDrop(projectDir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".molt", "registry.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var reg struct {
		Projects map[string]string `json:"projects"`
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		return err
	}
	delete(reg.Projects, projectDir)
	out, err := json.MarshalIndent(&reg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func cmdProjectReinit(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(abs, "pyproject.toml")); err != nil {
		return fmt.Errorf("no pyproject.toml at %s — not a molt project", abs)
	}
	fmt.Printf("Reinitialising project at %s...\n", abs)
	return syncplan.Sync(abs, syncplan.Options{Verbose: true})
}

// ── Tool registry ────────────────────────────────────────────────────────────

func cmdTool(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt tool <install|list|show|uninstall|path>")
	}
	switch args[0] {
	case "install":
		return cmdToolInstall(args[1:])
	case "list":
		return cmdToolList()
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt tool show <name>")
		}
		return cmdToolShow(args[1])
	case "uninstall":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt tool uninstall <name>")
		}
		if err := tooldb.Uninstall(args[1]); err != nil {
			return err
		}
		fmt.Printf("✓ uninstalled tool %q\n", args[1])
		return nil
	case "path":
		bin, err := tooldb.BinDir()
		if err != nil {
			return err
		}
		fmt.Println(bin)
		return nil
	default:
		return fmt.Errorf("unknown tool subcommand: %s", args[0])
	}
}

func cmdToolInstall(args []string) error {
	fs := flag.NewFlagSet("tool install", flag.ExitOnError)
	name := fs.String("name", "", "Tool name (default: project basename)")
	task := fs.String("task", "", "Task to dispatch (default: project's default entry)")
	force := fs.Bool("force", false, "Overwrite an existing tool of the same name")
	fs.Parse(args)

	src := "."
	if fs.NArg() > 0 {
		src = fs.Arg(0)
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	resolvedName := *name
	if resolvedName == "" {
		resolvedName = filepath.Base(abs)
	}
	t, err := tooldb.Install(resolvedName, abs, *task, *force)
	if err != nil {
		return err
	}
	fmt.Printf("✓ installed tool %q\n", t.Name)
	fmt.Printf("  shim:    %s\n", t.ShimPath())
	fmt.Printf("  source:  %s\n", t.ProjectDir)
	if t.Task != "" {
		fmt.Printf("  task:    %s\n", t.Task)
	}

	// PATH bootstrap hint — print once if BinDir() isn't on PATH.
	bin, _ := tooldb.BinDir()
	if !pathContains(os.Getenv("PATH"), bin) {
		fmt.Printf("\n%s is not on your PATH. Add this to your shell profile:\n", bin)
		fmt.Printf("  export PATH=\"%s:$PATH\"\n", bin)
	}
	return nil
}

// pathContains reports whether the OS PATH env var contains dir as one
// of its components.
func pathContains(pathEnv, dir string) bool {
	if pathEnv == "" || dir == "" {
		return false
	}
	sep := string(os.PathListSeparator)
	for _, p := range strings.Split(pathEnv, sep) {
		if filepath.Clean(p) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}

func cmdToolList() error {
	tools, err := tooldb.List()
	if err != nil {
		return err
	}
	if len(tools) == 0 {
		fmt.Println("No tools installed. Try: molt tool install")
		return nil
	}
	fmt.Printf("%-20s %-10s %-20s %-15s %s\n", "NAME", "TASK", "LAST UPDATE", "STATE", "SOURCE")
	for _, t := range tools {
		state := "✓ alive"
		if !t.ProjectAlive() {
			state = "✗ source missing"
		}
		task := t.Task
		if task == "" {
			task = "(default)"
		}
		updated := "(never)"
		if !t.Updated.IsZero() {
			updated = t.Updated.Local().Format("2006-01-02 15:04:05")
		}
		fmt.Printf("%-20s %-10s %-20s %-15s %s\n", t.Name, task, updated, state, t.ProjectDir)
	}
	bin, _ := tooldb.BinDir()
	fmt.Printf("\n%d tool(s); shims at %s\n", len(tools), bin)
	return nil
}

func cmdToolShow(name string) error {
	t, err := tooldb.Get(name)
	if err != nil {
		return fmt.Errorf("tool %q not found", name)
	}
	fmt.Printf("Name:        %s\n", t.Name)
	fmt.Printf("Source:      %s\n", t.ProjectDir)
	fmt.Printf("Shim:        %s\n", t.ShimPath())
	if t.Task != "" {
		fmt.Printf("Task:        %s\n", t.Task)
	} else {
		fmt.Println("Task:        (default entry)")
	}
	if !t.Created.IsZero() {
		fmt.Printf("Created:     %s\n", t.Created.Local().Format(time.RFC3339))
	}
	if !t.Updated.IsZero() {
		fmt.Printf("Updated:     %s\n", t.Updated.Local().Format(time.RFC3339))
	}
	if t.ProjectAlive() {
		fmt.Println("State:       ✓ alive")
		if spec, err := syspath.Load(t.ProjectDir); err == nil {
			fmt.Printf("Python bin:  %s\n", spec.Python)
		}
	} else {
		fmt.Println("State:       ✗ source missing")
	}
	return nil
}

// parseDuration extends time.ParseDuration with day/week suffixes commonly
// expected by humans (e.g. "30d", "8w"). Falls back to time.ParseDuration
// for everything else.
func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	last := s[len(s)-1]
	switch last {
	case 'd', 'D':
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w', 'W':
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// ── Path discovery ────────────────────────────────────────────────────────────

func cmdWhere(args []string) error {
	proj := projectRoot()

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

	proj := projectRoot()
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
	dir := projectRoot()

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
// applyProjectFlag scans args for `--project <q>` (or `-p <q>`), resolves
// the query via projstate.Resolve, and stores the absolute project path
// in globalProjectOverride. Returns args with the flag pair removed.
//
// Skipped for commands that don't operate on a registered project (init,
// build, package, adopt, capture, assemble) — those receive the unmodified
// flag-stripped slice anyway, but the override is harmless if accidentally
// set since they never call projectRoot().
func applyProjectFlag(argv []string) ([]string, error) {
	if len(argv) <= 1 {
		return argv, nil
	}
	out := make([]string, 0, len(argv))
	out = append(out, argv[0]) // program name
	query := ""
	i := 1
	for i < len(argv) {
		a := argv[i]
		if a == "--" {
			out = append(out, argv[i:]...)
			break
		}
		if (a == "--project" || a == "-p") && i+1 < len(argv) {
			query = argv[i+1]
			i += 2
			continue
		}
		out = append(out, a)
		i++
	}
	if query == "" {
		return out, nil
	}
	entry, err := projstate.Resolve(query)
	if err != nil {
		return out, err
	}
	abs, _ := filepath.Abs(entry.ProjectDir)
	globalProjectOverride = abs
	return out, nil
}

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

func cmdNativePreset(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: molt native-preset <list|show|add|remove> [args]")
		return nil
	}
	switch args[0] {
	case "list":
		presets, err := nativepreset.Load()
		if err != nil {
			return err
		}
		if len(presets) == 0 {
			fmt.Println("no presets defined")
			return nil
		}
		for _, p := range presets {
			fmt.Printf("%-12s  build: %s\n", p.Name, p.Build)
		}
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt native-preset show <name>")
		}
		p, err := nativepreset.Find(args[1])
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("preset %q not found", args[1])
		}
		fmt.Printf("name:         %s\n", p.Name)
		fmt.Printf("build:        %s\n", p.Build)
		fmt.Printf("output:       %s\n", p.Output)
		if len(p.SrcPatterns) > 0 {
			fmt.Printf("src_patterns: %s\n", strings.Join(p.SrcPatterns, ", "))
		}
	case "add":
		fs := flag.NewFlagSet("native-preset add", flag.ContinueOnError)
		name := fs.String("name", "", "preset name (required)")
		build := fs.String("build", "", "build command template (required)")
		output := fs.String("output", "", "output path template (required)")
		var srcPats []string
		fs.Func("src-patterns", "glob patterns for change detection (comma-separated)", func(s string) error {
			for _, p := range strings.Split(s, ",") {
				if p = strings.TrimSpace(p); p != "" {
					srcPats = append(srcPats, p)
				}
			}
			return nil
		})
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" || *build == "" || *output == "" {
			return fmt.Errorf("--name, --build, and --output are required")
		}
		return nativepreset.Add(nativepreset.Preset{
			Name: *name, Build: *build, Output: *output, SrcPatterns: srcPats,
		})
	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt native-preset remove <name>")
		}
		return nativepreset.Remove(args[1])
	default:
		return fmt.Errorf("unknown native-preset command %q", args[0])
	}
	return nil
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
