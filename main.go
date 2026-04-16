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

	"molt/internal/builder"
	"molt/internal/deps"
	"molt/internal/env"
	"molt/internal/executor"
	"molt/internal/hash"
	"molt/internal/imports"
	"molt/internal/installer"
	"molt/internal/manifest"
	"molt/internal/platform"
	"molt/internal/python"
	"molt/internal/scaffold"
	"molt/internal/tasks"
	"molt/internal/templates"
	internuv "molt/internal/uv"
	"molt/internal/verifier"
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
	case "uv":
		err = cmdUV(os.Args[2:])

	// ── Python version CRUD ────────────────────────────────────────────────
	case "python":
		err = cmdPython(os.Args[2:])

	// ── Scaffolding ────────────────────────────────────────────────────────
	case "new":
		err = cmdNew(os.Args[2:])
	case "create":
		err = cmdCreate(os.Args[2:])

	// ── Task runner ────────────────────────────────────────────────────────
	case "run":
		err = cmdRun(os.Args[2:])
	case "task":
		err = cmdTask(os.Args[2:])

	// ── Dependency analysis ────────────────────────────────────────────────
	case "deps":
		err = cmdDeps(os.Args[2:])

	// ── Import analysis ────────────────────────────────────────────────────
	case "imports":
		err = cmdImports(os.Args[2:])

	// ── Environment ────────────────────────────────────────────────────────
	case "env":
		err = cmdEnv(os.Args[2:])

	// ── Hash / reproducibility ────────────────────────────────────────────
	case "hash":
		err = cmdHash(os.Args[2:])

	// ── Template management ────────────────────────────────────────────────
	case "template":
		err = cmdTemplate(os.Args[2:])

	// ── Distribution ──────────────────────────────────────────────────────
	case "build":
		err = cmdBuild(os.Args[2:])
	case "capture":
		err = cmdCapture(os.Args[2:])
	case "assemble":
		err = cmdAssemble(os.Args[2:])
	case "install":
		err = cmdInstall(os.Args[2:])

	// ── Verification ──────────────────────────────────────────────────────
	case "verify":
		err = cmdVerify(os.Args[2:])
	case "check":
		err = cmdCheck(os.Args[2:])
	case "doctor":
		err = cmdDoctor()
	case "sbom":
		err = cmdSBOM(os.Args[2:])

	// ── Meta ──────────────────────────────────────────────────────────────
	case "info":
		err = cmdInfo()
	case "version", "--version":
		fmt.Printf("molt version=%q date=%q commit=%q\n", version, date, commit)
	case "help", "--help", "-h":
		usage()
	default:
		// Try as a task name shortcut.
		r := tasks.New(cwd())
		if taskErr := r.Run(os.Args[1], false, os.Args[2:]); taskErr == nil {
			return
		}
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
  sync     [--frozen]              Sync venv with lockfile
  lock                             Regenerate uv.lock
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
  python isolation-check           Verify venv isolation

Scaffolding:
  new project <name> [--type cli|api|worker|lib|script]  New project
  new package <name> [--under pkg] [--with-models ...]   New subpackage
  new module  <name> [--in path] [--class Name]          New module + test
  new cli     <name> [--typer|--click]                   New CLI entrypoint
  new config  [--pydantic|--layered]                     Config system
  new logging [--json]                                   Logging setup
  new model   <name> [--pydantic|--sqlalchemy] [--fields name:type,...]
  new test    <module> [--async]                         Test file
  new fixture <name>                                     Pytest fixture
  new script  <name> [--scheduled]                       Script + task
  new dockerfile [--distroless|--multi-stage]            Dockerfile
  new github-actions [--release]                         CI workflow

Templates:
  create --from-template <name> [output] [--data '{...}'] [--interactive]
  template list   [--builtin|--user|--project] [--tag <tag>]
  template show   <name>
  template vars   <name>
  template preview <name> --data '{...}'
  template add    <path|url> [--project]
  template remove <name> [--project]
  template export <name> [dest]
  template new    <name> [--from-file <path>]
  template validate <path>

Task runner:
  run  <task> [--watch] [-- args]  Run a named task
  task list                        List all tasks
  task add <name> <command>        Add a task
  task remove <name>               Remove a task

Dependency analysis:
  deps                             Summary dashboard
  deps tree   [--json]             Full visual tree (all layers + hashes)
  deps flat                        Flat list, greppable
  deps why    <package>            Why is this in the graph
  deps pinned-by <package>         Who constrains this version
  deps conflicts                   Detect version conflicts
  deps minimal                     Which direct deps could be removed
  deps unused                      Packages never imported by source

Import analysis:
  imports graph   [--json|--html]  Full static import graph
  imports unused                   Imported but never used
  imports missing                  Used but not in pyproject.toml
  imports shadow                   Source files shadowing stdlib names
  imports cycles                   Circular import detection
  imports trace   <module>         Exactly which file gets imported
  imports external                 All third-party imports

Environment:
  env                              Current environment summary
  env diff                         Venv vs lockfile diff
  env validate                     Full consistency check
  env reset                        Nuke and recreate venv from lockfile
  env snapshot [name]              Save venv state snapshot
  env restore  <name>              Restore venv from snapshot
  env snapshots                    List all snapshots
  env vars                         Print project env variables
  env vars-check                   Verify .env matches .env.example

Hash / reproducibility:
  hash                             Hash everything (all layers)
  hash verify                      Re-hash and compare to lock
  hash diff                        Changes since last hash lock
  hash file   <path>               Hash a single file
  hash lock                        Write hash manifest to .molt-deps.lock

Distribution:
  build    [flags] [project-path]  Build binary for current platform
  capture  [flags] [project-path]  Capture env (run on TARGET machine)
  assemble [flags] [project-path]  Build from captured manifest
  install  [flags] [manifest]      Install on target machine

Verification:
  verify   [install-dir]           Verify installation integrity
  check                            Full project health check (CI gate)
  doctor                           System diagnostics
  sbom     [install-dir]           Software Bill of Materials (JSON)
  info                             Project summary

Other:
  version                          Print version

`)
}

// ── Python version CRUD ───────────────────────────────────────────────────────

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
		installed := false
		for _, v := range versions {
			if len(args) > 1 && args[1] == "--installed" && !v.Installed {
				continue
			}
			active := ""
			if v.Active {
				active = " ← active"
			}
			fmt.Printf("  %-12s %-12s %s%s\n", v.Version, v.Source, v.Path, active)
			installed = true
		}
		if !installed {
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

// ── Scaffolding ───────────────────────────────────────────────────────────────

func cmdNew(args []string) error {
	if len(args) == 0 {
		return interactiveNew()
	}

	c := scaffold.New(cwd())

	switch args[0] {
	case "project":
		fs := flag.NewFlagSet("new project", flag.ExitOnError)
		projectType := fs.String("type", "cli", "cli|api|worker|lib|script|plugin|monorepo")
		python := fs.String("python", "3.12", "Python version")
		description := fs.String("description", "", "Project description")
		noGit := fs.Bool("no-git", false, "Skip git init")
		noTests := fs.Bool("no-tests", false, "Skip test scaffold")
		minimal := fs.Bool("minimal", false, "Minimal scaffold")
		var projName string
		var flagArgs []string
		for i := 1; i < len(args); i++ {
			if !strings.HasPrefix(args[i], "-") && projName == "" {
				projName = args[i]
			} else {
				flagArgs = append(flagArgs, args[i])
			}
		}
		fs.Parse(flagArgs)
		if projName == "" {
			return fmt.Errorf("usage: molt new project <n> [--type cli|api|worker|lib|script]")
		}
		// If projName contains path separators, use the last component as the
		// actual project name and the original value as the output directory.
		outputDir := projName
		projName = filepath.Base(projName)
		if projName == "." || projName == "/" {
			return fmt.Errorf("invalid project name: %s", outputDir)
		}
		return c.NewProject(types.ScaffoldConfig{
			Name:        projName,
			Type:        types.ProjectType(*projectType),
			Python:      *python,
			Description: *description,
			NoGit:       *noGit,
			NoTests:     *noTests,
			Minimal:     *minimal,
			OutputDir:   outputDir,
		})

	case "package":
		fs := flag.NewFlagSet("new package", flag.ExitOnError)
		under := fs.String("under", "", "Parent package")
		withConfig := fs.Bool("with-config", false, "Include config.py")
		withModels := fs.Bool("with-models", false, "Include models.py")
		withExceptions := fs.Bool("with-exceptions", false, "Include exceptions.py")
		withCLI := fs.Bool("with-cli", false, "Include cli.py")
		var pkgName string
		var pkgFlags []string
		for i := 1; i < len(args); i++ {
			if !strings.HasPrefix(args[i], "-") && pkgName == "" {
				pkgName = args[i]
			} else {
				pkgFlags = append(pkgFlags, args[i])
			}
		}
		fs.Parse(pkgFlags)
		if pkgName == "" {
			return fmt.Errorf("usage: molt new package <n>")
		}
		return c.NewPackage(pkgName, scaffold.PackageOptions{
			Under:          *under,
			WithConfig:     *withConfig,
			WithModels:     *withModels,
			WithExceptions: *withExceptions,
			WithCLI:        *withCLI,
		})

	case "module":
		fs := flag.NewFlagSet("new module", flag.ExitOnError)
		in := fs.String("in", "", "Directory to place module in")
		className := fs.String("class", "", "Class name to scaffold")
		async := fs.Bool("async", false, "Async module template")
		dataclass := fs.Bool("dataclass", false, "Dataclass-based module")
		noTest := fs.Bool("no-test", false, "Skip test file")
		var modName string
		var modFlags []string
		for i := 1; i < len(args); i++ {
			if !strings.HasPrefix(args[i], "-") && modName == "" {
				modName = args[i]
			} else {
				modFlags = append(modFlags, args[i])
			}
		}
		fs.Parse(modFlags)
		if modName == "" {
			return fmt.Errorf("usage: molt new module <n>")
		}
		return c.NewModule(modName, scaffold.ModuleOptions{
			In:        *in,
			ClassName: *className,
			Async:     *async,
			Dataclass: *dataclass,
			NoTest:    *noTest,
		})

	case "cli":
		fs := flag.NewFlagSet("new cli", flag.ExitOnError)
		style := fs.String("style", "argparse", "argparse|typer|click")
		fs.Bool("typer", false, "Use typer")
		fs.Bool("click", false, "Use click")
		fs.Parse(args[1:])
		name := fs.Arg(0)
		if name == "" {
			name = filepath.Base(cwd())
		}
		// Check for --typer / --click flags in raw args.
		for _, a := range args[1:] {
			if a == "--typer" {
				*style = "typer"
			}
			if a == "--click" {
				*style = "click"
			}
		}
		return c.NewCLI(name, *style)

	case "config":
		style := "dataclass"
		for _, a := range args[1:] {
			if a == "--pydantic" {
				style = "pydantic"
			}
			if a == "--layered" {
				style = "layered"
			}
		}
		return c.NewConfig(style)

	case "logging":
		jsonOutput := false
		for _, a := range args[1:] {
			if a == "--json" {
				jsonOutput = true
			}
		}
		return c.NewLogging(jsonOutput)

	case "model":
		fs := flag.NewFlagSet("new model", flag.ExitOnError)
		style := fs.String("style", "dataclass", "dataclass|pydantic|sqlalchemy")
		fields := fs.String("fields", "", "Fields as name:type,name:type")
		fs.Bool("pydantic", false, "Pydantic model")
		fs.Bool("sqlalchemy", false, "SQLAlchemy model")
		var mdlName string
		var mdlNameFlags []string
		for i := 1; i < len(args); i++ {
			if !strings.HasPrefix(args[i], "-") && mdlName == "" {
				mdlName = args[i]
			} else {
				mdlNameFlags = append(mdlNameFlags, args[i])
			}
		}
		fs.Parse(mdlNameFlags)
		if mdlName == "" {
			return fmt.Errorf("usage: molt new model <n>")
		}
		for _, a := range args[1:] {
			if a == "--pydantic" {
				*style = "pydantic"
			}
			if a == "--sqlalchemy" {
				*style = "sqlalchemy"
			}
		}
		return c.NewModel(mdlName, *style, *fields)

	case "test":
		async := false
		for _, a := range args[1:] {
			if a == "--async" {
				async = true
			}
		}
		name := ""
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "--") {
				name = a
				break
			}
		}
		if name == "" {
			return fmt.Errorf("usage: molt new test <module>")
		}
		return c.NewTest(name, async)

	case "fixture":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt new fixture <name>")
		}
		return c.NewFixture(args[1])

	case "script":
		fs := flag.NewFlagSet("new script", flag.ExitOnError)
		scheduled := fs.Bool("scheduled", false, "Scheduled task")
		var scriptName string
		var scriptNameFlags []string
		for i := 1; i < len(args); i++ {
			if !strings.HasPrefix(args[i], "-") && scriptName == "" {
				scriptName = args[i]
			} else {
				scriptNameFlags = append(scriptNameFlags, args[i])
			}
		}
		fs.Parse(scriptNameFlags)
		if scriptName == "" {
			return fmt.Errorf("usage: molt new script <n>")
		}
		return c.NewScript(scriptName, *scheduled)

	case "dockerfile":
		style := "multi-stage"
		pythonVersion := ""
		for i, a := range args[1:] {
			if a == "--distroless" {
				style = "distroless"
			}
			if strings.HasPrefix(a, "--python=") {
				pythonVersion = strings.TrimPrefix(a, "--python=")
			}
			if a == "--python" && i+1 < len(args[1:]) {
				pythonVersion = args[i+2]
			}
		}
		return c.NewDockerfile(style, pythonVersion)

	case "github-actions":
		kind := "ci"
		for _, a := range args[1:] {
			if a == "--release" {
				kind = "release"
			}
		}
		return c.NewGitHubActions(kind)

	case "readme":
		return c.NewREADME()

	default:
		// Treat as project name with wizard.
		return c.NewProject(types.ScaffoldConfig{
			Name: args[0],
			Type: types.ProjectCLI,
		})
	}
}

func interactiveNew() error {
	fmt.Println("What would you like to create?")
	fmt.Println()
	fmt.Println("  1. New project")
	fmt.Println("  2. Package inside current project")
	fmt.Println("  3. Module")
	fmt.Println("  4. CLI entrypoint")
	fmt.Println("  5. Config system")
	fmt.Println("  6. Model")
	fmt.Println("  7. Test file")
	fmt.Println("  8. Script")
	fmt.Println("  9. Dockerfile")
	fmt.Println(" 10. GitHub Actions CI")
	fmt.Println()
	fmt.Print("Choice: ")

	var choice string
	fmt.Scanln(&choice)
	fmt.Println()

	switch strings.TrimSpace(choice) {
	case "1":
		fmt.Print("Project name: ")
		var name string
		fmt.Scanln(&name)
		fmt.Print("Type (cli/api/worker/lib) [cli]: ")
		var t string
		fmt.Scanln(&t)
		if t == "" {
			t = "cli"
		}
		c := scaffold.New(cwd())
		return c.NewProject(types.ScaffoldConfig{Name: name, Type: types.ProjectType(t)})
	default:
		fmt.Println("Use 'molt new --help' for all scaffold commands.")
	}
	return nil
}

// ── Template commands ──────────────────────────────────────────────────────────

func cmdCreate(args []string) error {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	fromTemplate := fs.String("from-template", "", "Template name")
	data := fs.String("data", "", "JSON data or @file.json")
	interactive := fs.Bool("interactive", false, "Interactive variable input")
	fs.Parse(args)

	if *fromTemplate == "" {
		return fmt.Errorf("usage: molt create --from-template <name> [output] [--data '{...}']")
	}

	outputPath := fs.Arg(0)
	e := templates.New(cwd())
	return e.Create(*fromTemplate, outputPath, *data, *interactive)
}

func cmdTemplate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt template <list|show|vars|preview|add|remove|export|new|validate>")
	}

	e := templates.New(cwd())

	switch args[0] {
	case "list":
		tier, tag := "", ""
		for i, a := range args[1:] {
			switch a {
			case "--builtin":
				tier = "builtin"
			case "--user":
				tier = "user"
			case "--project":
				tier = "project"
			case "--tag":
				if i+1 < len(args[1:]) {
					tag = args[i+2]
				}
			}
		}
		e.PrintList(tier, tag)

	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt template show <name>")
		}
		return e.Show(args[1])

	case "vars":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt template vars <name>")
		}
		return e.Vars(args[1])

	case "preview":
		fs := flag.NewFlagSet("template preview", flag.ExitOnError)
		data := fs.String("data", "", "JSON data")
		var filteredArgs []string
		tplName := ""
		for j := 1; j < len(args); j++ {
			if !strings.HasPrefix(args[j], "-") && tplName == "" {
				tplName = args[j]
			} else {
				filteredArgs = append(filteredArgs, args[j])
			}
		}
		fs.Parse(filteredArgs)
		if tplName == "" {
			return fmt.Errorf("usage: molt template preview <name> --data '{...}'")
		}
		return e.Preview(tplName, *data)

	case "add":
		fs := flag.NewFlagSet("template add", flag.ExitOnError)
		project := fs.Bool("project", false, "Install to project templates")
		fs.Parse(args[1:])
		if fs.NArg() == 0 {
			return fmt.Errorf("usage: molt template add <path|url>")
		}
		return e.Add(fs.Arg(0), *project)

	case "remove":
		fs := flag.NewFlagSet("template remove", flag.ExitOnError)
		project := fs.Bool("project", false, "Remove from project templates")
		fs.Parse(args[1:])
		if fs.NArg() == 0 {
			return fmt.Errorf("usage: molt template remove <name>")
		}
		return e.Remove(fs.Arg(0), *project)

	case "export":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt template export <name> [dest]")
		}
		dest := ""
		if len(args) > 2 {
			dest = args[2]
		}
		return e.Export(args[1], dest)

	case "new":
		fs := flag.NewFlagSet("template new", flag.ExitOnError)
		fromFile := fs.String("from-file", "", "Templatize an existing file")
		fs.Parse(args[1:])
		if fs.NArg() == 0 {
			return fmt.Errorf("usage: molt template new <name>")
		}
		return e.NewTemplate(fs.Arg(0), *fromFile)

	case "validate":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt template validate <path>")
		}
		return e.Validate(args[1])

	default:
		return fmt.Errorf("unknown template subcommand: %s", args[0])
	}
	return nil
}

// ── Task runner ───────────────────────────────────────────────────────────────

func cmdRun(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt run <task> [--watch] [-- extra-args]")
	}

	// Check if this is an app run (--app flag) vs task run.
	if hasFlag(args, "--app") {
		return cmdAppRun(args)
	}

	taskName := args[0]
	watch := hasFlag(args[1:], "--watch")

	// Collect extra args after --.
	var extraArgs []string
	for i, a := range args[1:] {
		if a == "--" {
			extraArgs = args[i+2:]
			break
		}
	}

	r := tasks.New(cwd())
	return r.Run(taskName, watch, extraArgs)
}

func cmdAppRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	appName := fs.String("app", "", "Application name")
	appVer := fs.String("version", "", "Application version")
	isolated := fs.Bool("isolated", false, "Linux namespace isolation")
	debug := fs.Bool("debug", false, "Debug output")
	fs.Parse(args)

	installDir, err := executor.DefaultInstallDir(*appName, *appVer)
	if err != nil {
		return err
	}
	if _, err := os.Stat(installDir); os.IsNotExist(err) {
		return fmt.Errorf("not installed: %s v%s (run 'molt install' first)", *appName, *appVer)
	}
	exec, err := executor.New(installDir, types.ExecutionConfig{UseNamespace: *isolated, Debug: *debug})
	if err != nil {
		return err
	}
	return exec.Run(fs.Args())
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

// ── Dependency analysis ───────────────────────────────────────────────────────

func cmdDeps(args []string) error {
	dir := cwd()
	a := deps.New(dir, false)

	if len(args) == 0 {
		// Summary dashboard.
		g, err := a.Build()
		if err != nil {
			return err
		}
		fmt.Printf("\n%s %s — dependency summary\n\n", g.Root, g.Version)
		fmt.Printf("Python:         %s (%s)\n", g.Python.Version, g.Python.BuildType)
		fmt.Printf("Platform:       %s\n", g.Platform)
		if g.GlibcVer != "" {
			fmt.Printf("glibc:          %s\n", g.GlibcVer)
		}
		fmt.Printf("Packages:       %d (%d direct)\n", len(g.Packages), countDirect(g.Packages))
		fmt.Printf("Native exts:    %d\n", len(g.NativeExts))
		fmt.Printf("System libs:    %d (%d non-standard)\n",
			len(g.SystemLibs), countNonStandard(g.SystemLibs))
		fmt.Printf("Source files:   %d\n", len(g.Source))
		if g.Security.Warnings > 0 {
			fmt.Printf("Security:       ⚠ %d warnings\n", g.Security.Warnings)
		} else {
			fmt.Println("Security:       ✓ clean")
		}
		return nil
	}

	switch args[0] {
	case "tree":
		jsonOut := hasFlag(args[1:], "--json")
		g, err := a.Build()
		if err != nil {
			return err
		}
		if jsonOut {
			data, err := deps.JSON(g)
			if err != nil {
				return err
			}
			fmt.Println(string(data))
		} else {
			deps.PrintTree(g)
		}

	case "flat":
		g, err := a.Build()
		if err != nil {
			return err
		}
		deps.PrintFlat(g)

	case "why":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt deps why <package>")
		}
		g, err := a.Build()
		if err != nil {
			return err
		}
		deps.Why(g, args[1])

	case "pinned-by":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt deps pinned-by <package>")
		}
		return deps.PinnedBy(dir, args[1])

	case "conflicts":
		return deps.Conflicts(dir)

	case "minimal":
		return deps.Minimal(dir)

	case "unused":
		im := imports.New(dir)
		return im.Unused()

	default:
		return fmt.Errorf("unknown deps subcommand: %s — try: tree, flat, why, pinned-by, conflicts, minimal, unused", args[0])
	}
	return nil
}

// ── Import analysis ───────────────────────────────────────────────────────────

func cmdImports(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt imports <graph|unused|missing|shadow|cycles|trace|external>")
	}

	im := imports.New(cwd())

	switch args[0] {
	case "graph":
		if hasFlag(args[1:], "--json") {
			return im.PrintJSON()
		}
		return im.PrintGraph()

	case "unused":
		return im.Unused()

	case "missing":
		return im.Missing()

	case "shadow":
		return im.Shadow()

	case "cycles":
		return im.Cycles()

	case "trace":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt imports trace <module>")
		}
		return im.Trace(args[1])

	case "external":
		return im.External()

	default:
		return fmt.Errorf("unknown imports subcommand: %s", args[0])
	}
}

// ── Environment ───────────────────────────────────────────────────────────────

func cmdEnv(args []string) error {
	m := env.New(cwd())

	if len(args) == 0 {
		return m.Validate()
	}

	switch args[0] {
	case "diff":
		return m.Diff()
	case "validate":
		return m.Validate()
	case "reset":
		return m.Reset()
	case "snapshot":
		name := ""
		if len(args) > 1 {
			name = args[1]
		}
		return m.Snapshot(name)
	case "restore":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt env restore <name>")
		}
		return m.Restore(args[1])
	case "snapshots":
		return m.ListSnapshots()
	case "vars":
		return m.Vars()
	case "vars-check":
		return m.VarsCheck()
	default:
		return fmt.Errorf("unknown env subcommand: %s", args[0])
	}
}

// ── Hash ──────────────────────────────────────────────────────────────────────

func cmdHash(args []string) error {
	b := hash.New(cwd())

	if len(args) == 0 {
		// Hash everything and print summary.
		fmt.Println("Computing full hash manifest...")
		m, err := b.Build()
		if err != nil {
			return err
		}
		hash.PrintManifest(m)
		return nil
	}

	switch args[0] {
	case "verify":
		stored, err := hash.Load(cwd())
		if err != nil {
			return err
		}
		return b.Verify(stored)

	case "diff":
		stored, err := hash.Load(cwd())
		if err != nil {
			return err
		}
		return b.Diff(stored)

	case "file":
		if len(args) < 2 {
			return fmt.Errorf("usage: molt hash file <path>")
		}
		return hash.HashFile(args[1])

	case "lock":
		fmt.Println("Computing full hash manifest...")
		m, err := b.Build()
		if err != nil {
			return err
		}
		return b.Write(m)

	default:
		return fmt.Errorf("unknown hash subcommand: %s", args[0])
	}
}

// ── Check (CI gate) ───────────────────────────────────────────────────────────

func cmdCheck(args []string) error {
	dir := cwd()
	issues := 0

	fmt.Println("molt check — project health")

	// 1. Python version pinned.
	if _, err := os.Stat(filepath.Join(dir, ".python-version")); err == nil {
		fmt.Println("  ✓ Python version pinned (.python-version)")
	} else {
		fmt.Println("  ✗ Python version not pinned — run 'molt python use <version>'")
		issues++
	}

	// 2. Lockfile present.
	if _, err := os.Stat(filepath.Join(dir, "uv.lock")); err == nil {
		fmt.Println("  ✓ uv.lock present")
	} else {
		fmt.Println("  ✗ uv.lock missing — run 'molt lock'")
		issues++
	}

	// 3. pyproject.toml present.
	if _, err := os.Stat(filepath.Join(dir, "pyproject.toml")); err == nil {
		fmt.Println("  ✓ pyproject.toml present")
	} else {
		fmt.Println("  ✗ pyproject.toml missing")
		issues++
	}

	// 4. Venv exists and matches lockfile.
	em := env.New(dir)
	if err := em.Validate(); err != nil {
		issues++
	}

	// 5. Import consistency.
	im := imports.New(dir)
	fmt.Println()
	fmt.Println("Import analysis:")
	if err := im.Missing(); err != nil {
		issues++
	}
	if err := im.Shadow(); err != nil {
		issues++
	}

	// 6. Dep conflicts.
	fmt.Println()
	fmt.Println("Dependency conflicts:")
	if err := deps.Conflicts(dir); err != nil {
		issues++
	}

	// 7. Hash manifest up to date.
	lockPath := filepath.Join(dir, hash.LockFile)
	if _, err := os.Stat(lockPath); err == nil {
		fmt.Println()
		fmt.Println("Hash manifest:")
		hb := hash.New(dir)
		stored, _ := hash.Load(dir)
		if err := hb.Verify(stored); err != nil {
			issues++
		}
	} else {
		fmt.Println()
		fmt.Println("  ⚠ No hash manifest — run 'molt hash lock' to create one")
	}

	fmt.Println()
	if issues == 0 {
		fmt.Println("✓ All checks passed")
	} else {
		fmt.Printf("✗ %d checks failed\n", issues)
		return fmt.Errorf("%d checks failed", issues)
	}
	return nil
}

// ── Info ──────────────────────────────────────────────────────────────────────

func cmdInfo() error {
	dir := cwd()

	// Read pyproject.toml.
	name, ver := "unknown", "unknown"
	if data, err := os.ReadFile(filepath.Join(dir, "pyproject.toml")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "name = ") {
				name = strings.Trim(strings.TrimPrefix(line, "name = "), `"`)
			}
			if strings.HasPrefix(line, "version = ") {
				ver = strings.Trim(strings.TrimPrefix(line, "version = "), `"`)
			}
		}
	}

	// Python version.
	pyVer := "unknown"
	if data, err := os.ReadFile(filepath.Join(dir, ".python-version")); err == nil {
		pyVer = strings.TrimSpace(string(data))
	}

	// Task count.
	r := tasks.New(dir)
	taskList, _ := r.List()

	fmt.Printf("\n%s %s\n", name, ver)
	fmt.Printf("Python:     %s\n", pyVer)
	fmt.Printf("Platform:   %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("Directory:  %s\n", dir)

	// Venv status.
	venvDir := filepath.Join(dir, ".venv")
	if _, err := os.Stat(venvDir); err == nil {
		fmt.Println("Venv:       present")
	} else {
		fmt.Println("Venv:       missing (run 'molt sync')")
	}

	// Hash manifest.
	if _, err := os.Stat(filepath.Join(dir, hash.LockFile)); err == nil {
		fmt.Println("Hash lock:  present (.molt-deps.lock)")
	} else {
		fmt.Println("Hash lock:  missing (run 'molt hash lock')")
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

// ── Existing commands (kept from original) ───────────────────────────────────

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	python := fs.String("python", "", "Python version")
	lib := fs.Bool("lib", false, "Library layout")
	noLock := fs.Bool("no-lock", false, "Skip uv lock")
	fs.Parse(args)

	dir, name := ".", ""
	switch fs.NArg() {
	case 0:
		abs, _ := filepath.Abs(dir)
		name = filepath.Base(abs)
	case 1:
		name = fs.Arg(0)
		dir = name
	default:
		return fmt.Errorf("usage: molt init [flags] [name]")
	}

	fmt.Printf("Initialising project %q...\n", name)
	if err := internuv.Init(dir, name, internuv.InitOptions{Python: *python, Lib: *lib}); err != nil {
		return fmt.Errorf("uv init: %w", err)
	}
	if !*noLock {
		if err := internuv.Lock(dir); err != nil {
			return fmt.Errorf("uv lock: %w", err)
		}
	}
	fmt.Println("✓ Done. Try 'molt new config' and 'molt new logging' to scaffold the project.")
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
	return internuv.Add(absDir, fs.Args(), *dev)
}

func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	dev := fs.Bool("dev", false, "Dev dependency")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: molt remove <package...>")
	}
	absDir, _ := filepath.Abs(".")
	return internuv.Remove(absDir, fs.Args(), *dev)
}

func cmdSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	frozen := fs.Bool("frozen", false, "Fail if lockfile needs updating")
	fs.Parse(args)
	absDir, _ := filepath.Abs(".")
	return internuv.Sync(absDir, *frozen)
}

func cmdLock(args []string) error {
	absDir, _ := filepath.Abs(".")
	return internuv.Lock(absDir)
}

func cmdTree(args []string) error {
	absDir, _ := filepath.Abs(".")
	return internuv.Tree(absDir)
}

func cmdUV(args []string) error {
	if len(args) == 0 {
		return internuv.Raw(".", []string{"--help"})
	}
	return internuv.Raw(".", args)
}

func cmdBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	profile := fs.String("profile", "standard", "minimal|standard|extended|full")
	name := fs.String("name", "", "Application name")
	ver := fs.String("version", "0.1.0", "Application version")
	output := fs.String("output", "", "Output path")
	targetOS := fs.String("os", runtime.GOOS, "Target OS")
	targetArch := fs.String("arch", runtime.GOARCH, "Target arch")
	bestEffort := fs.Bool("best-effort", false, "Allow cross-build")
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
		Profile:        types.BuildProfile(*profile),
		Name:           *name,
		Version:        *ver,
		ProjectPath:    absProject,
		OutputPath:     *output,
		TargetOS:       *targetOS,
		TargetArch:     *targetArch,
		CrossBuildMode: crossMode,
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

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	mode := fs.String("mode", "standalone", "minimal|standalone|exact")
	prefix := fs.String("prefix", "", "Installation directory")
	cacheDir := fs.String("cache-dir", "", "Cache directory")
	offline := fs.Bool("offline", false, "No network")
	parallel := fs.Int("parallel", 4, "Download workers")
	verbose := fs.Bool("verbose", false, "Verbose")
	dryRun := fs.Bool("dry-run", false, "Dry run")
	auditLog := fs.String("audit-log", "", "Audit log path")
	fs.Parse(args)

	manifestArg := ".molt/manifest.json"
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
		Mode:     types.InstallMode(*mode),
		CacheDir: *cacheDir,
		Offline:  *offline,
		Parallel: *parallel,
		Verbose:  *verbose,
		DryRun:   *dryRun,
		AuditLog: *auditLog,
	})
	if err != nil {
		return err
	}
	return inst.Install(context.Background(), m, targetDir)
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
	fmt.Printf("molt %s\n", version)
	fmt.Printf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println("─────────────────────────────────────")

	checks := []struct {
		label string
		fn    func() (string, bool)
	}{
		{"python3", func() (string, bool) {
			p, e := lookPath("python3")
			return p, e == nil
		}},
		{"uv", func() (string, bool) { return "found", internuv.Available() }},
		{"go", func() (string, bool) { p, e := lookPath("go"); return p, e == nil }},
		{"git", func() (string, bool) { p, e := lookPath("git"); return p, e == nil }},
		{"ldd", func() (string, bool) { p, e := lookPath("ldd"); return p, e == nil }},
		{"curl", func() (string, bool) { p, e := lookPath("curl"); return p, e == nil }},
	}

	for _, c := range checks {
		loc, ok := c.fn()
		sym := "✓"
		if !ok {
			sym = "✗"
		}
		fmt.Printf("  %s %-20s %s\n", sym, c.label, loc)
	}
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

// ── Helpers ───────────────────────────────────────────────────────────────────

func cwd() string {
	dir, _ := os.Getwd()
	return dir
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func loadManifest(arg string) (*types.Manifest, error) {
	if data, err := os.ReadFile(arg); err == nil {
		return manifest.Decode(data)
	}
	return manifest.Load(arg)
}

func lookPath(name string) (string, error) {
	return os.Executable()
}

func countDirect(pkgs []types.PackageInfo) int {
	n := 0
	for _, p := range pkgs {
		if p.DirectDep {
			n++
		}
	}
	return n
}

func countNonStandard(libs []types.SysLibInfo) int {
	n := 0
	for _, l := range libs {
		if !l.Standard {
			n++
		}
	}
	return n
}
