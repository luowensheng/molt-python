// Package scaffold creates project structures and individual files.
package scaffold

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"molt/pkg/types"
)

// PackageOptions controls subpackage creation.
type PackageOptions struct {
	Under          string
	WithConfig     bool
	WithModels     bool
	WithExceptions bool
	WithCLI        bool
}

// ModuleOptions controls module creation.
type ModuleOptions struct {
	In        string
	ClassName string
	Async     bool
	Dataclass bool
	NoTest    bool
}

// Creator handles project and file scaffolding.
type Creator struct {
	ProjectDir string
}

// New creates a Creator for the given project directory.
func New(projectDir string) *Creator {
	return &Creator{ProjectDir: projectDir}
}

// NewProject creates a full project structure.
// internal/scaffold/scaffold.go

func (c *Creator) NewProject(cfg types.ScaffoldConfig) error {
	dir := cfg.OutputDir
	if dir == "" {
		dir = cfg.Name
	}
	// 🛠️ Sanitize project name: handle "." by deriving from current directory
	if cfg.Name == "." {
		if abs, err := filepath.Abs(c.ProjectDir); err == nil {
			cfg.Name = filepath.Base(abs)
		}
		if cfg.Name == "" || cfg.Name == "." || cfg.Name == "/" {
			cfg.Name = "myproject"
		}
		if dir == "" {
			dir = "."
		}
	}
	if cfg.Name == "" {
		return fmt.Errorf("project name is required")
	}

	fmt.Printf("Creating %s project '%s'...\n", cfg.Type, cfg.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	pkg := strings.ReplaceAll(cfg.Name, "-", "_")
	// Ensure valid Python identifier
	pkg = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, pkg)
	if len(pkg) > 0 && pkg[0] >= '0' && pkg[0] <= '9' {
		pkg = "_" + pkg
	}

	if err := writeFile(filepath.Join(dir, "pyproject.toml"),
		pyprojectTOML(cfg.Name, pkg, cfg.Python, cfg.Type)); err != nil {
		return err
	}

	pyVer := cfg.Python
	if pyVer == "" {
		pyVer = "3.12"
	}
	if err := writeFile(filepath.Join(dir, ".python-version"), pyVer+"\n"); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, ".env.example"), envExample(cfg.Name)); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, ".gitignore"), gitignore()); err != nil {
		return err
	}
	if !cfg.Minimal {
		if err := writeFile(filepath.Join(dir, "README.md"), readme(cfg.Name, cfg.Description)); err != nil {
			return err
		}
	}

	pkgDir := filepath.Join(dir, pkg)
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return err
	}
	writeFile(filepath.Join(pkgDir, "__init__.py"), initPy(cfg.Name, "0.1.0"))
	writeFile(filepath.Join(pkgDir, "__main__.py"), mainPy(pkg))
	writeFile(filepath.Join(pkgDir, "main.py"), mainModule(pkg, cfg.Type))

	if !cfg.Minimal {
		writeFile(filepath.Join(pkgDir, "config.py"), configPy())
		writeFile(filepath.Join(pkgDir, "logging.py"), loggingPy(pkg))
		writeFile(filepath.Join(pkgDir, "exceptions.py"), exceptionsPy(cfg.Name))
	}

	switch cfg.Type {
	case types.ProjectCLI:
		writeFile(filepath.Join(pkgDir, "cli.py"), cliPy(cfg.Name, pkg))
		addEntryPoint(filepath.Join(dir, "pyproject.toml"), cfg.Name, pkg+".cli:main")
	case types.ProjectAPI:
		writeFile(filepath.Join(pkgDir, "app.py"), appPy(pkg))
		os.MkdirAll(filepath.Join(pkgDir, "routers"), 0o755)
		writeFile(filepath.Join(pkgDir, "routers", "__init__.py"), "")
		writeFile(filepath.Join(pkgDir, "routers", "health.py"), healthRouter())
	case types.ProjectWorker:
		writeFile(filepath.Join(pkgDir, "worker.py"), workerPy(cfg.Name, pkg))
	case types.ProjectPlugin:
		os.MkdirAll(filepath.Join(pkgDir, "plugins"), 0o755)
		writeFile(filepath.Join(pkgDir, "plugins", "__init__.py"), pluginSystem(cfg.Name))
	}

	if !cfg.NoTests {
		testDir := filepath.Join(dir, "tests")
		os.MkdirAll(testDir, 0o755)
		writeFile(filepath.Join(testDir, "__init__.py"), "")
		writeFile(filepath.Join(testDir, "conftest.py"), conftest(pkg))
		writeFile(filepath.Join(testDir, "test_main.py"), testMain(pkg))
	}
	if !cfg.Minimal {
		scriptsDir := filepath.Join(dir, "scripts")
		os.MkdirAll(scriptsDir, 0o755)
		writeFile(filepath.Join(scriptsDir, "seed.py"), seedScript(pkg))
	}

	if !cfg.NoGit {
		exec.Command("git", "init", dir).Run()
	}
	if _, err := exec.LookPath("uv"); err == nil {
		fmt.Println("  Running uv lock...")
		cmd := exec.Command("uv", "lock")
		cmd.Dir = dir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
	}

	fmt.Printf("\n✓ Project '%s' created in ./%s\n", cfg.Name, dir)
	fmt.Println("Next steps:")
	fmt.Printf("  cd %s\n", dir)
	fmt.Println("  molt sync")
	fmt.Println("  molt run dev")
	fmt.Println("  molt run test")
	return nil
}

// addEntryPoint safely injects a script entry into pyproject.toml.
func addEntryPoint(tomlPath, name, target string) error {
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return err
	}
	content := string(data)
	if !strings.Contains(content, "[project.scripts]") {
		// Sanitize name to guarantee a valid TOML bare key
		key := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				return r
			}
			return '_'
		}, name)
		if key == "" {
			key = "app"
		}
		content += fmt.Sprintf("\n[project.scripts]\n%s = \"%s\"\n", key, target)
		return os.WriteFile(tomlPath, []byte(content), 0o644)
	}
	return nil
}

// NewPackage creates a subpackage.
func (c *Creator) NewPackage(name string, opts PackageOptions) error {
	pkg := strings.ReplaceAll(name, "-", "_")
	mainPkg := c.detectMainPackage()

	var pkgDir string
	if opts.Under != "" {
		pkgDir = filepath.Join(c.ProjectDir, opts.Under, pkg)
	} else {
		pkgDir = filepath.Join(c.ProjectDir, mainPkg, pkg)
	}

	os.MkdirAll(pkgDir, 0o755)

	importPrefix := mainPkg + "." + pkg
	writeFile(filepath.Join(pkgDir, "__init__.py"), initPy(name, ""))
	writeFile(filepath.Join(pkgDir, "__main__.py"), mainPy(importPrefix))
	writeFile(filepath.Join(pkgDir, "core.py"), corePy(name, mainPkg))

	if opts.WithConfig {
		writeFile(filepath.Join(pkgDir, "config.py"), configPy())
	}
	if opts.WithModels {
		writeFile(filepath.Join(pkgDir, "models.py"), modelsPy(name))
	}
	if opts.WithExceptions {
		writeFile(filepath.Join(pkgDir, "exceptions.py"), exceptionsPy(name))
	}
	if opts.WithCLI {
		writeFile(filepath.Join(pkgDir, "cli.py"), cliPy(name, importPrefix))
	}

	testPkgDir := filepath.Join(c.ProjectDir, "tests", pkg)
	os.MkdirAll(testPkgDir, 0o755)
	writeFile(filepath.Join(testPkgDir, "__init__.py"), "")
	writeFile(filepath.Join(testPkgDir, "conftest.py"), subpkgConftest(importPrefix))
	writeFile(filepath.Join(testPkgDir, "test_core.py"), testModuleFile(name+"Core", importPrefix+".core"))

	fmt.Printf("✓ Package '%s' created\n", name)
	printCreated(pkgDir, c.ProjectDir)
	return nil
}

// NewModule creates a single module + test.
func (c *Creator) NewModule(name string, opts ModuleOptions) error {
	pkg := strings.ReplaceAll(name, "-", "_")
	mainPkg := c.detectMainPackage()

	moduleDir := c.ProjectDir
	if opts.In != "" {
		moduleDir = filepath.Join(c.ProjectDir, opts.In)
	} else if mainPkg != "" {
		moduleDir = filepath.Join(c.ProjectDir, mainPkg)
	}

	modulePath := filepath.Join(moduleDir, pkg+".py")
	writeFile(modulePath, moduleFile(name, mainPkg, opts))

	if !opts.NoTest {
		testPkg := mainPkg + "." + pkg
		if opts.ClassName != "" {
			// Has an explicit class — test that class.
			writeFile(filepath.Join(c.ProjectDir, "tests", "test_"+pkg+".py"),
				testModuleFile(opts.ClassName, testPkg))
		} else if opts.Async {
			// Async module — generate async test.
			writeFile(filepath.Join(c.ProjectDir, "tests", "test_"+pkg+".py"),
				testAsync(pkg, mainPkg))
		} else {
			// Plain module — test a function or let it be a stub.
			writeFile(filepath.Join(c.ProjectDir, "tests", "test_"+pkg+".py"),
				testFunctionModule(pkg, testPkg))
		}
	}

	rel, _ := filepath.Rel(c.ProjectDir, modulePath)
	fmt.Printf("✓ Module '%s' created\n  %s\n", name, rel)
	if !opts.NoTest {
		fmt.Printf("  tests/test_%s.py\n", pkg)
	}
	return nil
}

// NewCLI creates a CLI entrypoint.
func (c *Creator) NewCLI(name, style string) error {
	pkg := c.detectMainPackage()
	cliPath := filepath.Join(c.ProjectDir, pkg, "cli.py")

	var content string
	switch style {
	case "typer":
		content = cliTyper(name, pkg)
	case "click":
		content = cliClick(name, pkg)
	default:
		content = cliPy(name, pkg)
	}

	writeFile(cliPath, content)
	addEntryPoint(filepath.Join(c.ProjectDir, "pyproject.toml"), name, pkg+".cli:main")
	fmt.Printf("✓ CLI created at %s/cli.py\n  Entry point: %s = \"%s.cli:main\"\n", pkg, name, pkg)
	return nil
}

// NewTest creates a test file.
func (c *Creator) NewTest(moduleName string, async bool) error {
	pkg := c.detectMainPackage()
	mod := strings.ReplaceAll(moduleName, "-", "_")
	testPath := filepath.Join(c.ProjectDir, "tests", "test_"+mod+".py")

	// Determine whether moduleName refers to a subpackage or a module file.
	// This determines the correct import path.
	subpkgPath := filepath.Join(c.ProjectDir, pkg, mod)
	modulePath := filepath.Join(c.ProjectDir, pkg, mod+".py")
	var importPath string
	if _, err := os.Stat(subpkgPath); err == nil {
		// It's a subpackage — import from the package directly.
		importPath = pkg + "." + mod
	} else if _, err := os.Stat(modulePath); err == nil {
		// It's a single module file — import using pkg.mod.
		importPath = pkg + "." + mod
	} else {
		// Unknown — fall back to pkg.mod pattern.
		importPath = pkg + "." + mod
	}

	var content string
	if async {
		content = testAsync(mod, pkg)
	} else {
		content = testModuleFile(mod, importPath)
	}

	writeFile(testPath, content)
	fmt.Printf("✓ Test file created: tests/test_%s.py\n", mod)
	return nil
}

// NewConfig creates the config system.
func (c *Creator) NewConfig(style string) error {
	pkg := c.detectMainPackage()
	configPath := filepath.Join(c.ProjectDir, pkg, "config.py")

	var content string
	switch style {
	case "pydantic":
		content = configPydantic()
	case "layered":
		content = configLayered()
	default:
		content = configPy()
	}

	writeFile(configPath, content)
	writeFile(filepath.Join(c.ProjectDir, ".env.example"), envExample(c.detectProjectName()))
	writeFile(filepath.Join(c.ProjectDir, ".env"), "# Local overrides — DO NOT COMMIT\n")
	addToGitignore(c.ProjectDir, ".env")
	fmt.Printf("✓ Config created at %s/config.py\n", pkg)
	return nil
}

// NewLogging creates the logging setup.
func (c *Creator) NewLogging(jsonOutput bool) error {
	pkg := c.detectMainPackage()
	logPath := filepath.Join(c.ProjectDir, pkg, "logging.py")
	if jsonOutput {
		writeFile(logPath, loggingJSON(pkg))
	} else {
		writeFile(logPath, loggingPy(pkg))
	}
	fmt.Printf("✓ Logging created at %s/logging.py\n", pkg)
	return nil
}

// NewModel creates a model.
func (c *Creator) NewModel(name, style, fields string) error {
	pkg := c.detectMainPackage()
	modelName := strings.ReplaceAll(name, "-", "_")
	modelsPath := filepath.Join(c.ProjectDir, pkg, "models.py")
	exists := fileExists(modelsPath)

	var content string
	switch style {
	case "pydantic":
		content = modelPydantic(name, fields)
	case "sqlalchemy":
		content = modelSQLAlchemy(name, fields)
	default:
		content = modelDataclass(name, fields)
	}

	if exists {
		// Merge: add any new import lines to top of existing file,
		// then append the class body only — avoiding E402 errors.
		existing, err := os.ReadFile(modelsPath)
		if err != nil {
			return err
		}
		merged := mergeModelImports(string(existing), content)
		if err := os.WriteFile(modelsPath, []byte(merged), 0o644); err != nil {
			return err
		}
		fmt.Printf("✓ Model '%s' appended to %s/models.py\n", name, pkg)
	} else {
		writeFile(modelsPath, "from __future__ import annotations\n\n"+content)
		fmt.Printf("✓ Model '%s' created at %s/models.py\n", name, pkg)
	}
	writeFile(filepath.Join(c.ProjectDir, "tests", "test_"+modelName+".py"),
		testModuleFile(modelName, pkg+".models"))
	return nil
}

// NewFixture adds a fixture to conftest.py.
func (c *Creator) NewFixture(name string) error {
	confPath := filepath.Join(c.ProjectDir, "tests", "conftest.py")
	fixture := fmt.Sprintf("\n\n@pytest.fixture\ndef %s():\n    \"\"\"Fixture for %s.\"\"\"\n    yield None\n", name, name)

	if !fileExists(confPath) {
		writeFile(confPath, "import pytest\n"+fixture)
	} else {
		f, err := os.OpenFile(confPath, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		fmt.Fprint(f, fixture)
		f.Close()
	}
	fmt.Printf("✓ Fixture '%s' added to tests/conftest.py\n", name)
	return nil
}

// NewScript creates a standalone script.
func (c *Creator) NewScript(name string, scheduled bool) error {
	pkg := c.detectMainPackage()
	scriptName := strings.ReplaceAll(name, "-", "_")
	scriptPath := filepath.Join(c.ProjectDir, "scripts", scriptName+".py")
	os.MkdirAll(filepath.Join(c.ProjectDir, "scripts"), 0o755)
	writeFile(scriptPath, scriptFile(name, pkg, scheduled))
	addTask(filepath.Join(c.ProjectDir, "pyproject.toml"), name, "python scripts/"+scriptName+".py")
	fmt.Printf("✓ Script created: scripts/%s.py\n  Run: molt run %s\n", scriptName, name)
	return nil
}

// NewDockerfile creates a Dockerfile.
func (c *Creator) NewDockerfile(style, pythonVersion string) error {
	if pythonVersion == "" {
		pythonVersion = c.detectPythonVersion()
	}
	pkg := c.detectMainPackage()
	var content string
	if style == "distroless" {
		content = dockerfileDistroless(pkg, pythonVersion)
	} else {
		content = dockerfileMultiStage(pkg, pythonVersion)
	}
	writeFile(filepath.Join(c.ProjectDir, "Dockerfile"), content)
	writeFile(filepath.Join(c.ProjectDir, ".dockerignore"), dockerignore())
	fmt.Println("✓ Dockerfile created")
	return nil
}

// NewGitHubActions creates CI/CD workflows.
func (c *Creator) NewGitHubActions(kind string) error {
	workflowDir := filepath.Join(c.ProjectDir, ".github", "workflows")
	os.MkdirAll(workflowDir, 0o755)
	pythonVersion := c.detectPythonVersion()
	var filename, content string
	if kind == "release" {
		filename, content = "release.yml", githubActionsRelease()
	} else {
		filename, content = "ci.yml", githubActionsCI(pythonVersion)
	}
	writeFile(filepath.Join(workflowDir, filename), content)
	fmt.Printf("✓ GitHub Actions workflow: .github/workflows/%s\n", filename)
	return nil
}

// NewREADME creates a README.
func (c *Creator) NewREADME() error {
	writeFile(filepath.Join(c.ProjectDir, "README.md"), readme(c.detectProjectName(), ""))
	fmt.Println("✓ README.md created")
	return nil
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (c *Creator) detectMainPackage() string {
	data, err := os.ReadFile(filepath.Join(c.ProjectDir, "pyproject.toml"))
	if err != nil {
		return "app"
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "name = ") {
			name := strings.Trim(strings.TrimPrefix(line, "name = "), `"`)
			return strings.ReplaceAll(name, "-", "_")
		}
	}
	return "app"
}

func (c *Creator) detectProjectName() string {
	data, err := os.ReadFile(filepath.Join(c.ProjectDir, "pyproject.toml"))
	if err != nil {
		return "myproject"
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "name = ") {
			return strings.Trim(strings.TrimPrefix(line, "name = "), `"`)
		}
	}
	return "myproject"
}

func (c *Creator) detectPythonVersion() string {
	if data, err := os.ReadFile(filepath.Join(c.ProjectDir, ".python-version")); err == nil {
		return strings.TrimSpace(string(data))
	}
	return "3.12"
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func printCreated(dir, projectDir string) {
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(projectDir, path)
		if d.IsDir() {
			fmt.Printf("  %s/\n", rel)
		} else {
			fmt.Printf("  %s\n", rel)
		}
		return nil
	})
}


func addTask(tomlPath, name, command string) error {
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return err
	}
	content := string(data)
	task := fmt.Sprintf("%s = \"%s\"\n", name, command)
	if strings.Contains(content, "[tool.molt.tasks]") {
		content = strings.Replace(content, "[tool.molt.tasks]\n",
			"[tool.molt.tasks]\n"+task, 1)
	} else {
		content += "\n[tool.molt.tasks]\n" + task
	}
	return os.WriteFile(tomlPath, []byte(content), 0o644)
}

func addToGitignore(projectDir, entry string) {
	path := filepath.Join(projectDir, ".gitignore")
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), entry) {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\n", entry)
}

// mergeModelImports takes existing models.py content and a new model definition,
// hoists any new import lines to the top import block of the existing file,
// then appends just the class body. This avoids ruff E402 errors.
func mergeModelImports(existing, newModel string) string {
	// Split new model into import lines and body lines.
	var newImports []string
	var newBody []string
	inBody := false
	for _, line := range strings.Split(newModel, "\n") {
		trimmed := strings.TrimSpace(line)
		if !inBody && (strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from ")) {
			newImports = append(newImports, line)
		} else {
			if trimmed != "" {
				inBody = true
			}
			if inBody || trimmed != "" {
				newBody = append(newBody, line)
			}
		}
	}

	// Collect imports already present in the existing file.
	existingImports := map[string]bool{}
	for _, line := range strings.Split(existing, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "import ") || strings.HasPrefix(t, "from ") {
			existingImports[strings.TrimSpace(line)] = true
		}
	}

	// Find the last import line in the existing file.
	existingLines := strings.Split(existing, "\n")
	lastImportIdx := -1
	for i, line := range existingLines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "import ") || strings.HasPrefix(t, "from ") {
			lastImportIdx = i
		}
	}

	// Insert missing imports right after the last existing import line.
	var toInsert []string
	for _, imp := range newImports {
		if !existingImports[strings.TrimSpace(imp)] {
			toInsert = append(toInsert, imp)
		}
	}

	var result []string
	if lastImportIdx >= 0 && len(toInsert) > 0 {
		result = append(result, existingLines[:lastImportIdx+1]...)
		result = append(result, toInsert...)
		result = append(result, existingLines[lastImportIdx+1:]...)
	} else {
		result = existingLines
	}

	// Trim trailing newlines from existing content, then append body.
	combined := strings.TrimRight(strings.Join(result, "\n"), "\n")
	body := strings.TrimRight(strings.Join(newBody, "\n"), "\n")
	if body != "" {
		combined += "\n\n\n" + body + "\n"
	}
	return combined
}

// stripModelImports removes top-level import lines from a model definition.
// Kept for reference but mergeModelImports is preferred.
func stripModelImports(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	inBody := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBody && (strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from ")) {
			continue
		}
		if trimmed != "" {
			inBody = true
		}
		if inBody {
			out = append(out, line)
		}
	}
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	return strings.Join(out, "\n")
}
