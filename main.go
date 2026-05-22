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
	"molt/internal/mcpserver"
	"molt/internal/globalenv"
	"molt/internal/integrity"
	"molt/internal/kernelbuilder"
	"molt/internal/libcache"
	"molt/internal/moltenv"
	"molt/internal/nativepreset"
	"molt/internal/projstate"
	"molt/internal/python"
	"molt/internal/store"
	"molt/internal/syncplan"
	"molt/internal/syspath"
	"molt/internal/tasks"
	"molt/internal/templates"
	"molt/internal/tooldb"
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
	case "exec":
		err = cmdExec(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "activate":
		err = cmdActivate(os.Args[2:])
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

	// ── Mojo ──────────────────────────────────────────────────────────────
	case "mojo":
		err = cmdMojo(os.Args[2:])

	// ── AI / MCP server ───────────────────────────────────────────────────
	case "mcp":
		err = mcpserver.RunMCPServer(version)

	// ── Native presets ────────────────────────────────────────────────────
	case "native-preset":
		err = cmdNativePreset(os.Args[2:])
	case "kernel-builder":
		err = cmdKernelBuilder(os.Args[2:])
	case "env":
		err = cmdEnv(os.Args[2:])
	case "envs":
		err = cmdEnvs(os.Args[2:])

	// ── Global package store ──────────────────────────────────────────────
	case "gc":
		err = cmdGC(os.Args[2:])
	case "cache":
		err = cmdCache(os.Args[2:])

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

Kernel builders (~/.molt/kernel-builders.yaml — per-extension recipes):
  kernel-builder list                       List builders (global + built-in)
  kernel-builder show <ext>                 Show one builder's command
  kernel-builder add <ext> <command>        Add/update a builder globally
                                  --local             write to project pyproject.toml
                                  --from-template     use molt's suggested command
  kernel-builder remove <ext> [--local]     Remove a builder
  kernel-builder edit                       $EDITOR ~/.molt/kernel-builders.yaml
  kernel-builder reset                      Restore the seeded defaults
  kernel-builder path                       Print the global YAML path
  kernel-builder settings                   Show ~/.molt/kernel.yaml (cross-compile flags)
  kernel-builder settings set target-flags <ext> <os_arch> <flags>
  kernel-builder settings unset target-flags <ext> <os_arch>
  kernel-builder settings clear             Remove all global kernel settings

Environment variables (~/.molt/env.yaml — applied to every molt-spawned process):
  env list                                  List global env vars
  env get <NAME>                            Print one var's value
  env set <NAME> <VALUE>                    Set a global var
                                  --local   write to project [tool.molt.runtime.env]
  env unset <NAME> [--local]                Remove a var
  env edit                                  $EDITOR ~/.molt/env.yaml
  env path                                  Print the global YAML path

Global flags:
  --project / -p <q>               Operate on a registered project from anywhere
                                     (skipped for: init, build, package, adopt, capture, assemble)

Dependencies & sync:
  add      [--dev] [-r <file>] <pkg...>   Add dependency (or from requirements.txt)
  remove   [--dev] <pkg...>        Remove dependency
  sync     [--frozen] [--refresh]  Install lockfile into ~/.molt/pkg + write per-project state
  lock                             Regenerate uv.lock
  gc       [--dry-run]             Remove ~/.molt/pkg entries no project references
  cache    info [project]          Show disk usage of all molt-managed caches
           libs [project]          Scan library caches (torch, HuggingFace, etc.) vs installed packages
           clear libs <name>       Delete a library cache dir (with confirmation)
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
  run main.py  [args...]           Run a Python script (auto-dispatch by extension)
  run main.mojo [args...]          Run a Mojo script   (requires: molt add mojo)
  run <binary> [args...]           Exec a binary under the project environment
  task list                        List tasks
  task add <name> <command>        Add a task
  task remove <name>               Remove a task

Mojo:
  mojo <args...>                   Direct mojo passthrough under the project environment
                                     (requires: molt add mojo)
                                     e.g. molt mojo build ops.mojo --emit shared-lib -o ops.so
                                          molt mojo --help

Build:
  build    [flags] [project-path]  Build self-contained binary
                                     --output <path>  output path (default: <name>)
                                     --os/--arch      cross-build target for Go launcher
                                     --target <os_arch>  kernel cross-compile target (e.g. linux_amd64)
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

AI integration:
  mcp                              Start stdio MCP server (for Claude Code, Cursor, etc.)
                                     Exposes: init, sync, add, remove, run, exec, build, info, python
                                     Claude Code: add .claude/mcp.json to your project
                                     See: docs/mcp-server.md

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
	kernelTarget := fs.String("target", "", "Kernel cross-compile target (os_arch, e.g. linux_amd64)")
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

	// Derive kernel target from --os/--arch when --target is not explicit.
	// Cross-builds always need kernels compiled for the target platform.
	kt := *kernelTarget
	if kt == "" && (*targetOS != runtime.GOOS || *targetArch != runtime.GOARCH) {
		kt = *targetOS + "_" + *targetArch
	}
	if kt != "" {
		if err := syncplan.CompileKernels(absProject, kt, true); err != nil {
			return fmt.Errorf("kernel build for %s: %w", kt, err)
		}
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

// ── cache: molt-managed and library cache visibility ─────────────────────────

func cmdCache(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: molt cache <info|libs|clear> [args]")
		fmt.Println("  info  [project]          disk usage of molt-managed caches")
		fmt.Println("  libs  [project]          scan library caches vs installed packages")
		fmt.Println("  clear libs <name> [--yes]  delete a library cache dir")
		return nil
	}
	switch args[0] {
	case "info":
		return cmdCacheInfo(args[1:])
	case "libs":
		return cmdCacheLibs(args[1:])
	case "clear":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt cache clear libs <name> [--yes]")
		}
		if args[1] == "libs" {
			return cmdCacheClearLibs(args[2:])
		}
		return fmt.Errorf("usage: molt cache clear libs <name> [--yes]")
	}
	return fmt.Errorf("unknown cache command %q — use info, libs, or clear", args[0])
}

// cmdCacheInfo prints disk usage of all molt-managed cache directories.
func cmdCacheInfo(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	moltDir := filepath.Join(home, ".molt")

	type entry struct {
		label string
		dir   string
		note  string
	}
	entries := []entry{
		{"~/.molt/pkg/", filepath.Join(moltDir, "pkg"), "wheels"},
		{"~/.molt/native/", filepath.Join(moltDir, "native"), "compiled .so"},
		{"~/.molt/projects/", filepath.Join(moltDir, "projects"), "project state"},
		{"~/.molt/toolchains/", filepath.Join(moltDir, "toolchains"), "build toolchains"},
		{"~/.molt/python/", filepath.Join(moltDir, "python"), "managed Python"},
	}

	var totalBytes int64
	type row struct {
		label string
		bytes int64
		note  string
	}
	var rows []row
	for _, e := range entries {
		sz, _ := libcache.DirSize(e.dir)
		rows = append(rows, row{e.label, sz, e.note})
		totalBytes += sz
	}

	fmt.Println()
	labelW := 28
	sizeW := 10
	for _, r := range rows {
		fmt.Printf("  %-*s  %*s   (%s)\n",
			labelW, r.label,
			sizeW, libcache.FormatBytes(r.bytes),
			r.note)
	}
	fmt.Printf("  %s\n", strings.Repeat("─", labelW+sizeW+6))
	fmt.Printf("  %-*s  %*s\n\n", labelW, "Total", sizeW, libcache.FormatBytes(totalBytes))
	fmt.Println("Run `molt gc` to remove unused wheels and native objects.")
	fmt.Println()
	return nil
}

// cmdCacheLibs scans well-known library cache locations and cross-references
// them against the packages installed in the given project (or all live
// projects when no project path is supplied).
func cmdCacheLibs(args []string) error {
	projectPath := "."
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		projectPath = args[0]
	}
	absProject, err := filepath.Abs(projectPath)
	if err != nil {
		return err
	}

	installed := installedPackagesForCache(absProject)

	results, err := libcache.Scan(installed)
	if err != nil {
		return err
	}

	// Get store root for the relative path label.
	st, _ := store.Default()
	storeRoot := ""
	if st != nil {
		storeRoot = st.Root
	}

	fmt.Println()
	if len(results) == 0 {
		fmt.Println("  No known library caches found.")
		fmt.Println()
		return nil
	}

	// Print context line.
	spec, specErr := syspath.Load(absProject)
	if specErr == nil && spec.ProjectDir != "" {
		fmt.Printf("Library caches  (cross-referenced with project: %s)\n\n", spec.ProjectDir)
	} else {
		fmt.Print("Library caches  (cross-referenced with all registered projects)\n\n")
	}

	nameW, dirW, sizeW := 20, 40, 8
	fmt.Printf("  %-*s  %-*s  %*s  %s\n", nameW, "NAME", dirW, "DIR", sizeW, "SIZE", "STATUS")
	fmt.Printf("  %s\n", strings.Repeat("─", nameW+dirW+sizeW+12))

	var totalBytes int64
	for _, r := range results {
		if !r.Exists {
			continue
		}
		status := "installed"
		if !r.Installed {
			status = "not installed (orphan)"
		}
		// Shorten dir for display.
		displayDir := r.Dir
		if home, err := os.UserHomeDir(); err == nil {
			if rel, err := filepath.Rel(home, r.Dir); err == nil && !strings.HasPrefix(rel, "..") {
				displayDir = "~/" + rel
			}
		}
		fmt.Printf("  %-*s  %-*s  %*s  %s\n",
			nameW, r.Name,
			dirW, displayDir,
			sizeW, libcache.FormatBytes(r.Bytes),
			status)
		totalBytes += r.Bytes
	}
	fmt.Printf("\n  Total library cache: %s\n\n", libcache.FormatBytes(totalBytes))
	_ = storeRoot

	// Show installed-but-absent entries as hints.
	var hints []libcache.ScanResult
	for _, r := range results {
		if !r.Exists && r.Installed {
			hints = append(hints, r)
		}
	}
	if len(hints) > 0 {
		fmt.Println("  Installed but no cache yet:")
		for _, h := range hints {
			displayDir := h.Dir
			if home, err := os.UserHomeDir(); err == nil {
				if rel, err := filepath.Rel(home, h.Dir); err == nil && !strings.HasPrefix(rel, "..") {
					displayDir = "~/" + rel
				}
			}
			fmt.Printf("    %-*s  %s\n", nameW, h.Name, displayDir)
		}
		fmt.Println()
	}

	fmt.Println("  To redirect a cache: set the env var in [tool.molt.runtime.env] or `molt env set <VAR> <path>`")
	fmt.Println("  To clear:            molt cache clear libs <name>")
	fmt.Println()
	return nil
}

// cmdCacheClearLibs deletes a named library cache directory.
func cmdCacheClearLibs(args []string) error {
	yes := hasFlag(args, "--yes") || hasFlag(args, "-y")
	// Filter flags out to get positional args.
	var pos []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 {
		return fmt.Errorf("usage: molt cache clear libs <name> [--yes]")
	}
	name := pos[0]

	// Find the matching entry.
	var found *libcache.KnownCache
	for _, kc := range libcache.AllKnownCaches() {
		if strings.EqualFold(kc.Name, name) {
			c := kc
			found = &c
			break
		}
	}
	if found == nil {
		return fmt.Errorf("unknown library cache %q — run `molt cache libs` to see available names", name)
	}

	dir := found.ResolvedDir()
	if dir == "" {
		return fmt.Errorf("cannot resolve cache directory for %q", name)
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		fmt.Printf("  %s cache directory does not exist (%s)\n", name, dir)
		return nil
	}

	sz, _ := libcache.DirSize(dir)
	displayDir := dir
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, dir); err == nil && !strings.HasPrefix(rel, "..") {
			displayDir = "~/" + rel
		}
	}

	if !yes {
		fmt.Printf("\n  This will delete %s (%s). Continue? [y/N] ", displayDir, libcache.FormatBytes(sz))
		var resp string
		fmt.Fscan(os.Stdin, &resp)
		if !strings.EqualFold(strings.TrimSpace(resp), "y") {
			fmt.Println("  Aborted.")
			return nil
		}
	}

	if err := libcache.Clear(dir); err != nil {
		return fmt.Errorf("clear %s: %w", dir, err)
	}
	fmt.Printf("  ✓ cleared %s\n\n", displayDir)
	return nil
}

// installedPackagesForCache returns the set of normalised package names
// installed in the project at absProject. Falls back to a union across all
// registered live projects when the project's syspath.json is unavailable.
func installedPackagesForCache(absProject string) map[string]bool {
	st, err := store.Default()
	if err != nil {
		return map[string]bool{}
	}
	spec, err := syspath.Load(absProject)
	if err == nil && len(spec.Syspath) > 0 {
		return libcache.InstalledPackages(spec.Syspath, st.Root)
	}
	// Fall back: union across all live projects.
	all := map[string]bool{}
	entries, _ := projstate.ListAll()
	for _, e := range entries {
		if !e.ProjectAlive {
			continue
		}
		s, err := syspath.Load(e.ProjectDir)
		if err != nil {
			continue
		}
		for k, v := range libcache.InstalledPackages(s.Syspath, st.Root) {
			if v {
				all[k] = true
			}
		}
	}
	return all
}

// ── Python version management ─────────────────────────────────────────────────

// cmdMojo passes all arguments directly to the `mojo` binary installed in the
// project's uv-env, running it under the project's full environment
// (PYTHONPATH, MOJO_PYTHON_LIBRARY, dynamic-linker paths). Intended for
// `mojo build`, `mojo package`, `mojo doc`, and other mojo subcommands.
//
// For running a .mojo source file, the shorter `molt run main.mojo` form is
// preferred — it dispatches automatically by extension.
func cmdMojo(args []string) error {
	spec, err := syspath.Load(projectRoot())
	if err != nil {
		return fmt.Errorf("project not synced — run 'molt sync' first: %w", err)
	}
	if spec.MojoBin == "" {
		return fmt.Errorf("mojo is not installed in this project\n" +
			"Add it with: molt add mojo")
	}
	env := spec.BuildEnv(os.Environ())
	return syscall.Exec(spec.MojoBin, append([]string{"mojo"}, args...), env)
}

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
	// Pull --env <query> out of args before any other processing.
	envQuery, args := extractFlag(args, "--env")

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

	// Resolve --env override once so we can pass it to all run helpers.
	var envSpec *syspath.Spec
	if envQuery != "" {
		var err error
		envSpec, err = resolveEnvSpec(envQuery)
		if err != nil {
			return err
		}
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
	// Skip when --env is set: the env has its own syspath.
	if envSpec == nil {
		if err := ensureSynced(projectRoot()); err != nil {
			return err
		}
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
				return runPythonScript(projectRoot(), c, args[1:], envSpec)
			}
		}
	}

	// Single-file Mojo script mode: `molt run main.mojo [args...]`.
	// Mirrors the .py case above — dispatches by extension, resolves relative
	// paths against the project root, then exec-s `mojo run <file>` with the
	// project's full environment (PYTHONPATH, MOJO_PYTHON_LIBRARY, etc.).
	if strings.HasSuffix(taskName, ".mojo") || strings.HasSuffix(taskName, ".🔥") {
		candidates := []string{taskName}
		if !filepath.IsAbs(taskName) {
			candidates = append(candidates, filepath.Join(projectRoot(), taskName))
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return runMojoScript(projectRoot(), c, args[1:], envSpec)
			}
		}
	}

	// When an env override is active, fall through to direct exec with the
	// env's Python rather than trying task lookup (tasks belong to projects).
	if envSpec != nil {
		return runExec(projectRoot(), append([]string{taskName}, args[1:]...), envSpec)
	}

	r := tasks.New(projectRoot())
	if err := r.Run(taskName, watch, extraArgs); err == nil {
		return nil
	} else if !errors.Is(err, tasks.ErrTaskNotFound) {
		return err
	}

	return runExec(projectRoot(), append([]string{taskName}, args[1:]...), nil)
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
		return runPythonScript(proj, mainPy, extraArgs, nil)
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

// runPythonScript execs Python on a script path.
// override, when non-nil, supplies the interpreter + syspath instead of
// loading from the project. When both override and project syspath are absent
// and there is no pyproject.toml, falls back to the system Python
// (projectless mode).
func runPythonScript(projectDir, script string, scriptArgs []string, override *syspath.Spec) error {
	spec := override
	if spec == nil {
		nativeCheckAndSync(projectDir)
		var err error
		spec, err = syspath.Load(projectDir)
		if err != nil {
			// Projectless fallback: no syspath and no pyproject → bare exec.
			if _, statErr := os.Stat(filepath.Join(projectDir, "pyproject.toml")); os.IsNotExist(statErr) {
				return runBareScript(script, scriptArgs)
			}
			return fmt.Errorf("no .molt/syspath.json — run 'molt sync' first (%w)", err)
		}
	}
	abs, _ := filepath.Abs(script)
	env := spec.BuildEnv(os.Environ())
	argv := append([]string{spec.Python, abs}, scriptArgs...)
	return syscall.Exec(spec.Python, argv, env)
}

// runMojoScript exec-s `mojo run <script> [args...]` under the project
// environment. The project's shim sets MOJO_PYTHON_LIBRARY and PYTHONPATH so
// all managed Python packages are visible to Mojo's embedded CPython.
func runMojoScript(projectDir, script string, scriptArgs []string, override *syspath.Spec) error {
	spec := override
	if spec == nil {
		var err error
		spec, err = syspath.Load(projectDir)
		if err != nil {
			return fmt.Errorf("no .molt/syspath.json — run 'molt sync' first (%w)", err)
		}
	}
	if spec.MojoBin == "" {
		return fmt.Errorf("mojo is not installed in this project\n" +
			"Add it with: molt add mojo")
	}
	abs, _ := filepath.Abs(script)
	env := spec.BuildEnv(os.Environ())
	argv := append([]string{"mojo", "run", abs}, scriptArgs...)
	return syscall.Exec(spec.MojoBin, argv, env)
}

// runBareScript runs a Python script with no molt env — just the system (or
// global molt) Python with a clean environment. Used when there is no
// pyproject.toml and no --env flag.
func runBareScript(script string, scriptArgs []string) error {
	mgr, _ := python.New(os.TempDir())
	pyExe := ""
	if mgr != nil {
		pyExe, _ = mgr.Which()
	}
	if pyExe == "" {
		var err error
		pyExe, err = exec.LookPath("python3")
		if err != nil {
			pyExe, err = exec.LookPath("python")
		}
		if err != nil {
			return fmt.Errorf("no Python interpreter found — install Python or run `molt python install`")
		}
	}
	abs, _ := filepath.Abs(script)
	env := stripPythonVenvEnv(os.Environ())
	argv := append([]string{pyExe, abs}, scriptArgs...)
	return syscall.Exec(pyExe, argv, env)
}

// stripPythonVenvEnv returns a copy of env with VIRTUAL_ENV, PYTHONHOME, and
// PYTHONPATH removed so stale venv state can't leak into a bare execution.
func stripPythonVenvEnv(env []string) []string {
	out := env[:0:len(env)]
	for _, e := range env {
		k := strings.SplitN(e, "=", 2)[0]
		if k == "VIRTUAL_ENV" || k == "PYTHONHOME" || k == "PYTHONPATH" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// resolveEnvSpec resolves --env <query> to a *syspath.Spec.
//
//  1. Named env in ~/.molt/envs/<query>/
//  2. Existing registered project by name/path/hash
func resolveEnvSpec(query string) (*syspath.Spec, error) {
	// 1. Named env.
	if moltenv.Exists(query) {
		spec, err := moltenv.LoadSpec(query)
		if err != nil {
			return nil, err
		}
		return spec, nil
	}
	// 2. Registered project.
	if entry, err := projstate.Resolve(query); err == nil {
		spec, err := syspath.Load(entry.ProjectDir)
		if err != nil {
			return nil, fmt.Errorf("project %q not synced — run `molt sync --project %s` first", query, query)
		}
		return spec, nil
	}
	// 3. Maybe it's a plain path to a molt project on disk.
	if abs, err := filepath.Abs(query); err == nil {
		if _, statErr := os.Stat(filepath.Join(abs, "pyproject.toml")); statErr == nil {
			spec, err := syspath.Load(abs)
			if err != nil {
				return nil, fmt.Errorf("project at %q not synced — run `molt sync` in that directory first", abs)
			}
			return spec, nil
		}
	}
	return nil, fmt.Errorf("env or project %q not found\n  create an env with: molt envs create %s [packages...]", query, query)
}

// extractFlag removes --flag <value> from args and returns (value, remaining).
// Returns ("", args) if the flag is not present.
func extractFlag(args []string, flag string) (string, []string) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			remaining := append(args[:i:i], args[i+2:]...)
			return args[i+1], remaining
		}
		if strings.HasPrefix(a, flag+"=") {
			val := strings.TrimPrefix(a, flag+"=")
			remaining := append(args[:i:i], args[i+1:]...)
			return val, remaining
		}
	}
	return "", args
}

// cmdExec runs an arbitrary binary from the molt environment, bypassing task
// lookup. Use `molt exec uvicorn --port 8000` to reach any console-script shim.
func cmdExec(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt exec <command> [args...]")
	}
	if err := ensureSynced(projectRoot()); err != nil {
		return err
	}
	return runExec(projectRoot(), args, nil)
}

// cmdActivate prints shell export commands that wire the molt environment into
// the caller's current shell session. Run: eval $(molt activate)
func cmdActivate(args []string) error {
	fs := flag.NewFlagSet("activate", flag.ExitOnError)
	shellFlag := fs.String("shell", "", "shell syntax: bash, zsh, fish (default: auto-detect)")
	fs.Parse(args) //nolint:errcheck

	proj := projectRoot()
	if err := ensureSynced(proj); err != nil {
		return err
	}
	spec, err := syspath.Load(proj)
	if err != nil {
		return fmt.Errorf("no .molt/syspath.json — run 'molt sync' first (%w)", err)
	}

	shell := *shellFlag
	if shell == "" {
		shell = detectShell()
	}

	stateDir := spec.StateDir()
	binDir := filepath.Join(stateDir, syspath.BinDirName)
	pyPath := strings.Join(append([]string{stateDir}, spec.Syspath...), string(os.PathListSeparator))

	switch shell {
	case "fish":
		fmt.Printf("set -x PYTHONPATH %q;\n", pyPath)
		fmt.Printf("set -x PATH %q $PATH;\n", binDir)
		fmt.Printf("set -x MOLT_PROJECT %q;\n", proj)
		fmt.Printf("set -x MOLT_PYTHON %q;\n", spec.Python)
		fmt.Println("set -e VIRTUAL_ENV 2>/dev/null; true")
		fmt.Println("set -e PYTHONHOME 2>/dev/null; true")
	default: // bash / zsh / posix
		fmt.Printf("export PYTHONPATH=%q\n", pyPath)
		fmt.Printf("export PATH=%q:\"$PATH\"\n", binDir)
		fmt.Printf("export MOLT_PROJECT=%q\n", proj)
		fmt.Printf("export MOLT_PYTHON=%q\n", spec.Python)
		fmt.Println("unset VIRTUAL_ENV PYTHONHOME 2>/dev/null; true")
	}
	return nil
}

func detectShell() string {
	if strings.Contains(os.Getenv("SHELL"), "fish") {
		return "fish"
	}
	return "bash"
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
// override, when non-nil, supplies the interpreter + syspath instead of
// loading from the project.
func runExec(projectDir string, argv []string, override *syspath.Spec) error {
	if len(argv) == 0 {
		return fmt.Errorf("nothing to run")
	}
	spec := override
	if spec == nil {
		var err error
		spec, err = syspath.Load(projectDir)
		if err != nil {
			// Projectless fallback for `python`/`python3` bare exec.
			bin := argv[0]
			if bin == "python" || bin == "python3" {
				if _, statErr := os.Stat(filepath.Join(projectDir, "pyproject.toml")); os.IsNotExist(statErr) {
					return runBareScript(argv[1], argv[2:])
				}
			}
			return fmt.Errorf("no .molt/syspath.json — run 'molt sync' first (%w)", err)
		}
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

	st, _ := store.Default()
	storeRoot := ""
	if st != nil {
		storeRoot = st.Root
	}

	fmt.Printf("%-22s  %-10s  %-20s  %-16s  %-7s  %s\n",
		"NAME", "PKGS", "LAST SYNC", "STATE", "SIZE", "PATH")
	fmt.Println(strings.Repeat("─", 110))
	for _, e := range entries {
		name := filepath.Base(e.ProjectDir)
		if name == "" {
			name = "(no path)"
		}
		state := "✓ alive"
		if !e.ProjectAlive {
			state = "✗ missing"
		}
		last := "(never)"
		if !e.Meta.LastSync.IsZero() {
			last = e.Meta.LastSync.Local().Format("2006-01-02 15:04")
		}
		// Disk: state dir + sum of this project's wheel store entries.
		stateSz, _ := libcache.DirSize(e.Dir)
		var wheelSz int64
		pkgCount := 0
		if spec, err := syspath.Load(e.ProjectDir); err == nil {
			installed := libcache.InstalledPackages(spec.Syspath, storeRoot)
			pkgCount = len(installed)
			for _, dir := range spec.Syspath {
				if storeRoot != "" && strings.HasPrefix(filepath.Clean(dir), filepath.Clean(storeRoot)) {
					sz, _ := libcache.DirSize(dir)
					wheelSz += sz
				}
			}
		}
		total := stateSz + wheelSz
		fmt.Printf("%-22s  %-10s  %-20s  %-16s  %-7s  %s\n",
			name,
			fmt.Sprintf("%d pkgs", pkgCount),
			last, state,
			libcache.FormatBytes(total),
			e.ProjectDir)
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

	st, _ := store.Default()
	storeRoot := ""
	if st != nil {
		storeRoot = st.Root
	}

	if spec, err := syspath.Load(entry.ProjectDir); err == nil {
		fmt.Printf("Python bin:  %s\n", spec.Python)

		// ── Disk usage ────────────────────────────────────────────────────
		stateSz, _ := libcache.DirSize(entry.Dir)
		var wheelSz int64
		var wheelDirs []string
		for _, dir := range spec.Syspath {
			if storeRoot != "" && strings.HasPrefix(filepath.Clean(dir), filepath.Clean(storeRoot)) {
				sz, _ := libcache.DirSize(dir)
				wheelSz += sz
				wheelDirs = append(wheelDirs, dir)
			}
		}
		fmt.Printf("\nDisk usage:\n")
		fmt.Printf("  State dir   %s  (%s)\n", libcache.FormatBytes(stateSz), entry.Dir)
		fmt.Printf("  Packages    %s  (%d entries in store)\n", libcache.FormatBytes(wheelSz), len(wheelDirs))
		fmt.Printf("  Total       %s\n", libcache.FormatBytes(stateSz+wheelSz))

		// ── Installed packages ────────────────────────────────────────────
		installed := libcache.InstalledPackages(spec.Syspath, storeRoot)
		if len(installed) > 0 {
			pkgs := make([]string, 0, len(installed))
			for p := range installed {
				pkgs = append(pkgs, p)
			}
			sort.Strings(pkgs)
			fmt.Printf("\nPackages (%d):\n", len(pkgs))
			for _, p := range pkgs {
				// Find version from the store path.
				ver := ""
				for _, dir := range wheelDirs {
					rel, _ := filepath.Rel(storeRoot, dir)
					parts := strings.SplitN(rel, string(os.PathSeparator), 3)
					if len(parts) >= 2 && libcache.NormalizeName(parts[0]) == p {
						ver = parts[1]
						break
					}
				}
				if ver != "" {
					fmt.Printf("  %-30s  %s\n", p, ver)
				} else {
					fmt.Printf("  %s\n", p)
				}
			}
		}
	}

	// ── Produced files: shims ────────────────────────────────────────────
	binDir := projstate.Bin(entry.ProjectDir)
	if shims, err := os.ReadDir(binDir); err == nil && len(shims) > 0 {
		fmt.Printf("\nShims (in %s):\n", binDir)
		for _, s := range shims {
			fmt.Printf("  %s\n", s.Name())
		}
	}

	// ── Produced files: native modules ───────────────────────────────────
	nativeData, err := os.ReadFile(projstate.Native(entry.ProjectDir))
	if err == nil {
		var nativeDoc struct {
			Cython   map[string]struct{ So string `json:"so"` } `json:"cython"`
			Rust     map[string]struct{ So string `json:"so"` } `json:"rust"`
			External map[string]struct{ So string `json:"so"` } `json:"external"`
		}
		if json.Unmarshal(nativeData, &nativeDoc) == nil {
			var modules []string
			for mod, e := range nativeDoc.Cython {
				modules = append(modules, fmt.Sprintf("  %-30s  %s", mod, e.So))
			}
			for mod, e := range nativeDoc.Rust {
				modules = append(modules, fmt.Sprintf("  %-30s  %s", mod, e.So))
			}
			for mod, e := range nativeDoc.External {
				modules = append(modules, fmt.Sprintf("  %-30s  %s", mod, e.So))
			}
			if len(modules) > 0 {
				sort.Strings(modules)
				fmt.Printf("\nNative modules:\n")
				for _, m := range modules {
					fmt.Println(m)
				}
			}
		}
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
		return fmt.Errorf("usage: molt tool <install|list|show|uninstall|set-env|unset-env|path>")
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
	case "set-env":
		if len(args) < 3 {
			return fmt.Errorf("usage: molt tool set-env <tool-name> <env-name>")
		}
		t, err := tooldb.SetEnv(args[1], args[2])
		if err != nil {
			return err
		}
		fmt.Printf("✓ tool %q now uses env %q\n", t.Name, t.Env)
		return nil
	case "unset-env":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt tool unset-env <tool-name>")
		}
		t, err := tooldb.UnsetEnv(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("✓ tool %q env binding removed\n", t.Name)
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
	// Use manual flag extraction so flags work regardless of position
	// relative to the positional src argument. Go's flag package stops
	// parsing at the first non-flag arg, which breaks `molt tool install
	// script.py --env myenv`.
	envName, args := extractFlag(args, "--env")
	toolName, args := extractFlag(args, "--name")
	taskName, args := extractFlag(args, "--task")
	force := hasFlag(args, "--force")
	// Strip --force from positional args.
	var positional []string
	for _, a := range args {
		if a != "--force" && !strings.HasPrefix(a, "--") {
			positional = append(positional, a)
		}
	}

	src := "."
	if len(positional) > 0 {
		src = positional[0]
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return err
	}

	// Env-only script tool: positional arg is a .py file + --env is set.
	if strings.HasSuffix(src, ".py") && envName != "" {
		resolvedName := toolName
		if resolvedName == "" {
			base := filepath.Base(abs)
			resolvedName = strings.TrimSuffix(base, ".py")
		}
		t, err := tooldb.InstallScript(resolvedName, abs, envName, force)
		if err != nil {
			return err
		}
		printToolInstalled(t)
		return nil
	}

	// Project-based tool (existing behaviour + optional --env).
	resolvedName := toolName
	if resolvedName == "" {
		resolvedName = filepath.Base(abs)
	}
	t, err := tooldb.Install(resolvedName, abs, taskName, envName, force)
	if err != nil {
		return err
	}
	printToolInstalled(t)
	return nil
}

func printToolInstalled(t *tooldb.Tool) {
	fmt.Printf("✓ installed tool %q\n", t.Name)
	fmt.Printf("  shim:  %s\n", t.ShimPath())
	if t.Script != "" {
		fmt.Printf("  script: %s\n", t.Script)
	} else {
		fmt.Printf("  source: %s\n", t.ProjectDir)
	}
	if t.Task != "" {
		fmt.Printf("  task:  %s\n", t.Task)
	}
	if t.Env != "" {
		fmt.Printf("  env:   %s\n", t.Env)
	}

	// PATH bootstrap hint — print once if BinDir() isn't on PATH.
	bin, _ := tooldb.BinDir()
	if !pathContains(os.Getenv("PATH"), bin) {
		fmt.Printf("\n%s is not on your PATH. Add this to your shell profile:\n", bin)
		fmt.Printf("  export PATH=\"%s:$PATH\"\n", bin)
	}
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
	fmt.Printf("%-20s %-12s %-12s %-20s %-14s %s\n", "NAME", "TASK", "ENV", "LAST UPDATE", "STATE", "SOURCE")
	for _, t := range tools {
		state := "✓ alive"
		if !t.ProjectAlive() {
			state = "✗ source missing"
		}
		task := t.Task
		if task == "" {
			if t.IsEnvTool() {
				task = "(script)"
			} else {
				task = "(default)"
			}
		}
		envCol := t.Env
		if envCol == "" {
			envCol = "-"
		}
		updated := "(never)"
		if !t.Updated.IsZero() {
			updated = t.Updated.Local().Format("2006-01-02 15:04:05")
		}
		src := t.ProjectDir
		if t.Script != "" {
			src = t.Script
		}
		fmt.Printf("%-20s %-12s %-12s %-20s %-14s %s\n", t.Name, task, envCol, updated, state, src)
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
	if t.Script != "" {
		fmt.Printf("Script:      %s\n", t.Script)
	} else {
		fmt.Printf("Source:      %s\n", t.ProjectDir)
	}
	fmt.Printf("Shim:        %s\n", t.ShimPath())
	if t.Task != "" {
		fmt.Printf("Task:        %s\n", t.Task)
	} else if !t.IsEnvTool() {
		fmt.Println("Task:        (default entry)")
	}
	if t.Env != "" {
		fmt.Printf("Env:         %s\n", t.Env)
	}
	if !t.Created.IsZero() {
		fmt.Printf("Created:     %s\n", t.Created.Local().Format(time.RFC3339))
	}
	if !t.Updated.IsZero() {
		fmt.Printf("Updated:     %s\n", t.Updated.Local().Format(time.RFC3339))
	}
	if t.ProjectAlive() {
		fmt.Println("State:       ✓ alive")
		lookupDir := t.ProjectDir
		if t.IsEnvTool() {
			if d, err := moltenv.Dir(t.Env); err == nil {
				lookupDir = d
			}
		}
		if spec, err := syspath.Load(lookupDir); err == nil {
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

// ── kernel-builder: per-extension kernel build recipes ──────────────────────

func cmdKernelBuilder(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: molt kernel-builder <list|show|add|remove|edit|reset|path|settings> [args]")
		fmt.Println("       --local on add/remove writes the project's pyproject.toml instead")
		return nil
	}
	switch args[0] {
	case "list":
		return cmdKernelBuilderList()
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt kernel-builder show <ext>")
		}
		return cmdKernelBuilderShow(args[1])
	case "add":
		return cmdKernelBuilderAdd(args[1:])
	case "remove":
		return cmdKernelBuilderRemove(args[1:])
	case "edit":
		return cmdKernelBuilderEdit()
	case "reset":
		if err := kernelbuilder.Reset(); err != nil {
			return err
		}
		fmt.Println("✓ kernel-builders.yaml reset to defaults")
		return nil
	case "path":
		p, err := kernelbuilder.GlobalPath()
		if err != nil {
			return err
		}
		fmt.Println(p)
		return nil
	case "settings":
		return cmdKernelBuilderSettings(args[1:])
	}
	return fmt.Errorf("unknown kernel-builder command %q", args[0])
}

// cmdKernelBuilderSettings manages ~/.molt/kernel.yaml — the global table of
// per-compiler cross-compile flags.
//
//	molt kernel-builder settings                                  print settings
//	molt kernel-builder settings set target-flags <ext> <os_arch> <flags>
//	molt kernel-builder settings unset target-flags <ext> <os_arch>
//	molt kernel-builder settings clear
func cmdKernelBuilderSettings(args []string) error {
	if len(args) == 0 {
		return cmdKernelBuilderSettingsShow()
	}
	switch args[0] {
	case "set":
		return cmdKernelBuilderSettingsSet(args[1:])
	case "unset":
		return cmdKernelBuilderSettingsUnset(args[1:])
	case "clear":
		if err := kernelbuilder.SaveGlobalSettings(kernelbuilder.GlobalKernelSettings{}); err != nil {
			return err
		}
		fmt.Println("✓ ~/.molt/kernel.yaml cleared")
		return nil
	}
	return fmt.Errorf("usage: molt kernel-builder settings [set|unset|clear]")
}

func cmdKernelBuilderSettingsShow() error {
	gs, err := kernelbuilder.LoadGlobalSettings()
	if err != nil {
		return err
	}
	p, _ := kernelbuilder.GlobalSettingsPath()
	fmt.Printf("# %s\n", p)
	if len(gs.TargetFlags) == 0 {
		fmt.Println("(no target flags configured)")
		return nil
	}
	// Print sorted for stability.
	exts := make([]string, 0, len(gs.TargetFlags))
	for ext := range gs.TargetFlags {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	for _, ext := range exts {
		osMap := gs.TargetFlags[ext]
		keys := make([]string, 0, len(osMap))
		for k := range osMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("target_flags.%s.%s = %q\n", ext, k, osMap[k])
		}
	}
	return nil
}

func cmdKernelBuilderSettingsSet(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: molt kernel-builder settings set target-flags <ext> <os_arch> <flags>")
	}
	switch args[0] {
	case "target-flags":
		if len(args) < 4 {
			return fmt.Errorf("usage: molt kernel-builder settings set target-flags <ext> <os_arch> <flags>")
		}
		ext, osArch, flags := args[1], args[2], strings.Join(args[3:], " ")
		gs, err := kernelbuilder.LoadGlobalSettings()
		if err != nil {
			return err
		}
		if gs.TargetFlags == nil {
			gs.TargetFlags = map[string]map[string]string{}
		}
		if gs.TargetFlags[ext] == nil {
			gs.TargetFlags[ext] = map[string]string{}
		}
		gs.TargetFlags[ext][osArch] = flags
		if err := kernelbuilder.SaveGlobalSettings(gs); err != nil {
			return err
		}
		fmt.Printf("✓ target_flags.%s.%s = %q\n", ext, osArch, flags)
		return nil
	}
	return fmt.Errorf("unknown settings key %q — supported: target-flags", args[0])
}

func cmdKernelBuilderSettingsUnset(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: molt kernel-builder settings unset target-flags <ext> <os_arch>")
	}
	switch args[0] {
	case "target-flags":
		if len(args) < 3 {
			return fmt.Errorf("usage: molt kernel-builder settings unset target-flags <ext> <os_arch>")
		}
		ext, osArch := args[1], args[2]
		gs, err := kernelbuilder.LoadGlobalSettings()
		if err != nil {
			return err
		}
		if gs.TargetFlags != nil && gs.TargetFlags[ext] != nil {
			delete(gs.TargetFlags[ext], osArch)
			if len(gs.TargetFlags[ext]) == 0 {
				delete(gs.TargetFlags, ext)
			}
		}
		if err := kernelbuilder.SaveGlobalSettings(gs); err != nil {
			return err
		}
		fmt.Printf("✓ target_flags.%s.%s removed\n", ext, osArch)
		return nil
	}
	return fmt.Errorf("unknown settings key %q — supported: target-flags", args[0])
}

func cmdKernelBuilderList() error {
	builders, err := kernelbuilder.Load()
	if err != nil {
		return err
	}

	// Build a quick "what does the global file declare?" map and a
	// fallback-to-built-in summary so users can see the layered picture.
	declared := map[string]bool{}
	for _, b := range builders {
		declared[b.Ext] = true
	}

	fmt.Printf("%-6s  %-22s  %s\n", "ext", "source", "command")
	fmt.Println(strings.Repeat("─", 80))

	// Print global entries.
	for _, b := range builders {
		fmt.Printf("%-6s  %-22s  %s\n", b.Ext, "global", b.Command)
	}
	// Print built-ins not also declared globally (this only happens if
	// the user trimmed the global file to remove a default).
	for _, d := range kernelbuilder.DefaultBuilders() {
		if declared[d.Ext] {
			continue
		}
		fmt.Printf("%-6s  %-22s  %s\n", d.Ext, "built-in (fallback)", d.Command)
	}
	return nil
}

func cmdKernelBuilderShow(ext string) error {
	ext = strings.TrimPrefix(ext, ".")
	if b, ok, err := kernelbuilder.Find(ext); err == nil && ok {
		path, _ := kernelbuilder.GlobalPath()
		fmt.Printf("ext      %s\n", b.Ext)
		fmt.Printf("source   %s\n", path)
		fmt.Printf("command  %s\n", b.Command)
		return nil
	}
	for _, d := range kernelbuilder.DefaultBuilders() {
		if d.Ext == ext {
			fmt.Printf("ext      %s\n", d.Ext)
			fmt.Println("source   built-in (fallback)")
			fmt.Printf("command  %s\n", d.Command)
			return nil
		}
	}
	return fmt.Errorf("no builder for .%s — add one with `molt kernel-builder add %s '<command>'`", ext, ext)
}

func cmdKernelBuilderAdd(args []string) error {
	// Allow flags to appear before *or* after positional args, which
	// Go's stdlib flag package doesn't do natively.
	flags, rest := extractFlags(args, map[string]bool{"local": true, "from-template": true})
	local := flags["local"]
	fromTpl := flags["from-template"]
	_ = local
	_ = fromTpl
	if len(rest) < 1 {
		return fmt.Errorf("usage: molt kernel-builder add [--local] [--from-template] <ext> [<command>]")
	}
	ext := strings.TrimPrefix(rest[0], ".")

	var command string
	switch {
	case fromTpl:
		tpl, ok := kernelbuilder.SuggestedTemplates[ext]
		if !ok {
			return fmt.Errorf("no suggested template for .%s — provide a command explicitly", ext)
		}
		command = tpl
	case len(rest) >= 2:
		command = rest[1]
	default:
		return fmt.Errorf("usage: molt kernel-builder add [--local] <ext> <command>  (or use --from-template)")
	}

	if local {
		if err := writeKernelBuilderToPyproject(ext, command); err != nil {
			return err
		}
		fmt.Printf("✓ wrote [tool.molt.native_kernel.build.%s] to pyproject.toml\n", ext)
		fmt.Printf("  command: %s\n", command)
		return nil
	}
	if err := kernelbuilder.Add(kernelbuilder.Builder{Ext: ext, Command: command}); err != nil {
		return err
	}
	path, _ := kernelbuilder.GlobalPath()
	fmt.Printf("✓ added builder for .%s → %s\n", ext, path)
	fmt.Printf("  command: %s\n", command)
	return nil
}

func cmdKernelBuilderRemove(args []string) error {
	flags, rest := extractFlags(args, map[string]bool{"local": true})
	local := flags["local"]
	if len(rest) < 1 {
		return fmt.Errorf("usage: molt kernel-builder remove [--local] <ext>")
	}
	ext := strings.TrimPrefix(rest[0], ".")
	if local {
		if err := removeKernelBuilderFromPyproject(ext); err != nil {
			return err
		}
		fmt.Printf("✓ removed [tool.molt.native_kernel.build.%s] from pyproject.toml\n", ext)
		return nil
	}
	if err := kernelbuilder.Remove(ext); err != nil {
		return err
	}
	fmt.Printf("✓ removed builder for .%s\n", ext)
	return nil
}

func cmdKernelBuilderEdit() error {
	path, err := kernelbuilder.GlobalPath()
	if err != nil {
		return err
	}
	// Ensure the file exists (Load auto-seeds on missing).
	if _, err := kernelbuilder.Load(); err != nil {
		return err
	}
	editorBin := os.Getenv("EDITOR")
	if editorBin == "" {
		editorBin = "vi"
	}
	cmd := exec.Command(editorBin, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// extractFlags pulls boolean flags out of args from any position and
// returns the resulting flag map + the remaining positional args. Each
// recognised flag is `--name` (no value); unknown flags pass through
// as positional, which is intentional so a typo doesn't get silently
// dropped.
func extractFlags(args []string, recognised map[string]bool) (map[string]bool, []string) {
	flags := map[string]bool{}
	var rest []string
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			name := strings.TrimPrefix(a, "--")
			if recognised[name] {
				flags[name] = true
				continue
			}
		}
		rest = append(rest, a)
	}
	return flags, rest
}

// writeKernelBuilderToPyproject appends or updates the
// [tool.molt.native_kernel.build.<ext>] block in the current dir's
// pyproject.toml. Conservative line-based editor — preserves the rest
// of the file's formatting and comments.
func writeKernelBuilderToPyproject(ext, command string) error {
	pp, err := os.ReadFile("pyproject.toml")
	if err != nil {
		return fmt.Errorf("read pyproject.toml: %w", err)
	}
	header := fmt.Sprintf("[tool.molt.native_kernel.build.%s]", ext)
	commandLine := fmt.Sprintf("command = %q", command)

	lines := strings.Split(string(pp), "\n")
	found := false
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			found = true
			// Replace the next non-blank line's `command = ...` if
			// present, otherwise insert after the header.
			for j := i + 1; j < len(lines); j++ {
				t := strings.TrimSpace(lines[j])
				if t == "" {
					continue
				}
				if strings.HasPrefix(t, "[") {
					// Section ended without a command line; insert one.
					lines = append(lines[:j], append([]string{commandLine}, lines[j:]...)...)
					break
				}
				if strings.HasPrefix(t, "command") {
					lines[j] = commandLine
					break
				}
				// Some unrelated key — leave it alone, just insert ours
				// at the top of the section.
				lines = append(lines[:i+1], append([]string{commandLine}, lines[i+1:]...)...)
				break
			}
			break
		}
	}
	if !found {
		// Append a new block at end of file.
		out := strings.TrimRight(string(pp), "\n") + "\n\n" + header + "\n" + commandLine + "\n"
		return os.WriteFile("pyproject.toml", []byte(out), 0o644)
	}
	return os.WriteFile("pyproject.toml", []byte(strings.Join(lines, "\n")), 0o644)
}

// removeKernelBuilderFromPyproject deletes the
// [tool.molt.native_kernel.build.<ext>] section and any keys directly
// under it (until the next section or EOF).
func removeKernelBuilderFromPyproject(ext string) error {
	pp, err := os.ReadFile("pyproject.toml")
	if err != nil {
		return fmt.Errorf("read pyproject.toml: %w", err)
	}
	header := fmt.Sprintf("[tool.molt.native_kernel.build.%s]", ext)
	lines := strings.Split(string(pp), "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	removed := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == header {
			skipping = true
			removed = true
			continue
		}
		if skipping {
			if strings.HasPrefix(t, "[") {
				skipping = false
			} else {
				continue
			}
		}
		out = append(out, line)
	}
	if !removed {
		return fmt.Errorf("[tool.molt.native_kernel.build.%s] not found in pyproject.toml", ext)
	}
	return os.WriteFile("pyproject.toml", []byte(strings.Join(out, "\n")), 0o644)
}

// ── env: per-project + global env-var registry ──────────────────────────────

func cmdEnv(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: molt env <list|get|set|unset|edit|path> [args]")
		fmt.Println("       --local on set/unset writes to project pyproject.toml instead")
		return nil
	}
	switch args[0] {
	case "list":
		return cmdEnvList()
	case "get":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt env get <NAME>")
		}
		return cmdEnvGet(args[1])
	case "set":
		return cmdEnvSet(args[1:])
	case "unset":
		return cmdEnvUnset(args[1:])
	case "edit":
		return cmdEnvEdit()
	case "path":
		p, err := globalenv.GlobalPath()
		if err != nil {
			return err
		}
		fmt.Println(p)
		return nil
	}
	return fmt.Errorf("unknown env command %q", args[0])
}

func cmdEnvList() error {
	vars, err := globalenv.Load()
	if err != nil {
		return err
	}
	if len(vars) == 0 {
		fmt.Println("no global env vars set (~/.molt/env.yaml)")
		return nil
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%-30s  %s\n", k, vars[k])
	}
	return nil
}

func cmdEnvGet(name string) error {
	v, ok, err := globalenv.Get(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s not set", name)
	}
	fmt.Println(v)
	return nil
}

func cmdEnvSet(args []string) error {
	flags, rest := extractFlags(args, map[string]bool{"local": true})
	local := flags["local"]
	if len(rest) < 2 {
		return fmt.Errorf("usage: molt env set [--local] <NAME> <VALUE>")
	}
	name, value := rest[0], rest[1]
	if globalenv.IsReserved(name) {
		return fmt.Errorf("%s is molt-managed and can't be set in env config", name)
	}
	if local {
		if err := writeEnvVarToPyproject(name, value); err != nil {
			return err
		}
		fmt.Printf("✓ set %s in pyproject.toml [tool.molt.runtime.env]\n", name)
		return nil
	}
	if err := globalenv.Set(name, value); err != nil {
		return err
	}
	path, _ := globalenv.GlobalPath()
	fmt.Printf("✓ set %s in %s\n", name, path)
	return nil
}

func cmdEnvUnset(args []string) error {
	flags, rest := extractFlags(args, map[string]bool{"local": true})
	local := flags["local"]
	if len(rest) < 1 {
		return fmt.Errorf("usage: molt env unset [--local] <NAME>")
	}
	name := rest[0]
	if local {
		if err := removeEnvVarFromPyproject(name); err != nil {
			return err
		}
		fmt.Printf("✓ removed %s from pyproject.toml\n", name)
		return nil
	}
	if err := globalenv.Unset(name); err != nil {
		return err
	}
	fmt.Printf("✓ removed %s from global env\n", name)
	return nil
}

func cmdEnvEdit() error {
	path, err := globalenv.GlobalPath()
	if err != nil {
		return err
	}
	// Make sure file exists (Load returns empty if missing; we want a
	// real file for $EDITOR to open).
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if err := globalenv.Save(map[string]string{}); err != nil {
			return err
		}
	}
	editorBin := os.Getenv("EDITOR")
	if editorBin == "" {
		editorBin = "vi"
	}
	cmd := exec.Command(editorBin, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// writeEnvVarToPyproject inserts or updates KEY = "VALUE" inside the
// [tool.molt.runtime.env] block of the current dir's pyproject.toml.
// Conservative line editor — preserves comments and other formatting.
func writeEnvVarToPyproject(name, value string) error {
	pp, err := os.ReadFile("pyproject.toml")
	if err != nil {
		return fmt.Errorf("read pyproject.toml: %w", err)
	}
	const header = "[tool.molt.runtime.env]"
	keyLine := fmt.Sprintf("%s = %q", name, value)

	lines := strings.Split(string(pp), "\n")
	headerIdx := -1
	keyIdx := -1
	sectionEnd := len(lines)

	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == header {
			headerIdx = i
			continue
		}
		if headerIdx >= 0 && i > headerIdx {
			if strings.HasPrefix(t, "[") {
				sectionEnd = i
				break
			}
			if strings.HasPrefix(t, name+" ") || strings.HasPrefix(t, name+"=") {
				keyIdx = i
			}
		}
	}

	// Trim trailing blank lines inside the section so we don't insert
	// after them (which would leave a stray blank line mid-section).
	for sectionEnd > headerIdx+1 && strings.TrimSpace(lines[sectionEnd-1]) == "" {
		sectionEnd--
	}
	switch {
	case keyIdx >= 0:
		lines[keyIdx] = keyLine
	case headerIdx >= 0:
		// Insert key after the last existing key in the section.
		lines = append(lines[:sectionEnd],
			append([]string{keyLine}, lines[sectionEnd:]...)...)
	default:
		// New section.
		out := strings.TrimRight(string(pp), "\n") + "\n\n" + header + "\n" + keyLine + "\n"
		return os.WriteFile("pyproject.toml", []byte(out), 0o644)
	}
	return os.WriteFile("pyproject.toml", []byte(strings.Join(lines, "\n")), 0o644)
}

// removeEnvVarFromPyproject deletes one KEY=VALUE line under
// [tool.molt.runtime.env]. If the section becomes empty, leaves the
// header in place — pruning is the user's call.
func removeEnvVarFromPyproject(name string) error {
	pp, err := os.ReadFile("pyproject.toml")
	if err != nil {
		return fmt.Errorf("read pyproject.toml: %w", err)
	}
	const header = "[tool.molt.runtime.env]"
	lines := strings.Split(string(pp), "\n")
	inSection := false
	removed := false
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == header {
			inSection = true
			out = append(out, line)
			continue
		}
		if inSection && strings.HasPrefix(t, "[") {
			inSection = false
		}
		if inSection && (strings.HasPrefix(t, name+" ") || strings.HasPrefix(t, name+"=")) {
			removed = true
			continue
		}
		out = append(out, line)
	}
	if !removed {
		return fmt.Errorf("%s not set in pyproject.toml [tool.molt.runtime.env]", name)
	}
	return os.WriteFile("pyproject.toml", []byte(strings.Join(out, "\n")), 0o644)
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

// ── Named environments (molt envs) ───────────────────────────────────────────

func cmdEnvs(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt envs <create|add|remove|sync|list|info|delete|path>")
	}
	switch args[0] {
	case "create":
		return cmdEnvsCreate(args[1:])
	case "add":
		return cmdEnvsAdd(args[1:])
	case "remove":
		return cmdEnvsRemove(args[1:])
	case "sync":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt envs sync <name> [--allow-fresh]")
		}
		allowFresh := hasFlag(args[2:], "--allow-fresh")
		return moltenv.Sync(args[1], allowFresh)
	case "list":
		return cmdEnvsList()
	case "info":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt envs info <name>")
		}
		return cmdEnvsInfo(args[1])
	case "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt envs delete <name>")
		}
		if err := moltenv.Delete(args[1]); err != nil {
			return err
		}
		fmt.Printf("✓ deleted env %q\n", args[1])
		return nil
	case "path":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt envs path <name>")
		}
		d, err := moltenv.Dir(args[1])
		if err != nil {
			return err
		}
		fmt.Println(d)
		return nil
	default:
		return fmt.Errorf("unknown envs subcommand %q\n  usage: molt envs <create|add|remove|sync|list|info|delete|path>", args[0])
	}
}

func cmdEnvsCreate(args []string) error {
	// Use extractFlag so --python and --allow-fresh work regardless of position.
	python, args := extractFlag(args, "--python")
	allowFresh := hasFlag(args, "--allow-fresh")
	// Remaining positional args: first is the name, rest are packages.
	var positional []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
		}
	}
	if len(positional) < 1 {
		return fmt.Errorf("usage: molt envs create <name> [--python 3.12] [--allow-fresh] [package...]")
	}
	name := positional[0]
	packages := positional[1:]
	if err := moltenv.Create(name, python, packages, allowFresh); err != nil {
		return err
	}
	fmt.Printf("✓ env %q ready", name)
	if len(packages) > 0 {
		fmt.Printf(" (%s)", strings.Join(packages, ", "))
	}
	fmt.Println()
	fmt.Printf("  run a script in it: molt run --env %s <script.py>\n", name)
	return nil
}

func cmdEnvsAdd(args []string) error {
	allowFresh := hasFlag(args, "--allow-fresh")
	var positional []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
		}
	}
	if len(positional) < 2 {
		return fmt.Errorf("usage: molt envs add <name> <package...> [--allow-fresh]")
	}
	name := positional[0]
	pkgs := positional[1:]
	return moltenv.AddPackages(name, pkgs, allowFresh)
}

func cmdEnvsRemove(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: molt envs remove <name> <package...>")
	}
	name := args[0]
	pkgs := args[1:]
	return moltenv.RemovePackages(name, pkgs)
}

func cmdEnvsList() error {
	envs, err := moltenv.List()
	if err != nil {
		return err
	}
	if len(envs) == 0 {
		fmt.Println("No named envs. Create one with: molt envs create <name> [packages...]")
		return nil
	}
	fmt.Printf("%-20s %-8s %-6s %s\n", "NAME", "PYTHON", "PKGS", "PACKAGES")
	for _, def := range envs {
		python := def.Python
		if python == "" {
			python = "(global)"
		}
		pkgList := strings.Join(def.Packages, ", ")
		if len(pkgList) > 50 {
			pkgList = pkgList[:47] + "..."
		}
		fmt.Printf("%-20s %-8s %-6d %s\n", def.Name, python, len(def.Packages), pkgList)
	}
	root, _ := moltenv.Root()
	fmt.Printf("\n%d env(s) at %s\n", len(envs), root)
	return nil
}

func cmdEnvsInfo(name string) error {
	info, err := moltenv.Info(name)
	if err != nil {
		return err
	}
	d, _ := moltenv.Dir(name)
	fmt.Printf("Name:     %s\n", info.Def.Name)
	fmt.Printf("Dir:      %s\n", d)
	if info.Def.Python != "" {
		fmt.Printf("Python:   %s (requested)\n", info.Def.Python)
	}
	if info.Synced {
		fmt.Printf("Python:   %s (resolved)\n", info.Python)
		fmt.Println("Synced:   ✓ yes")
	} else {
		fmt.Println("Synced:   ✗ no — run `molt envs sync " + name + "`")
	}
	if len(info.Def.Packages) == 0 {
		fmt.Println("Packages: (none)")
	} else {
		fmt.Println("Packages:")
		for _, p := range info.Def.Packages {
			fmt.Printf("  %s\n", p)
		}
	}
	if len(info.Def.Env) > 0 {
		fmt.Println("Env vars:")
		for k := range info.Def.Env {
			fmt.Printf("  %s\n", k)
		}
	}
	usage, _ := moltenv.DiskUsage(name)
	fmt.Printf("Disk:     %s\n", formatBytes(usage))
	return nil
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
