// Package imports performs static import analysis on Python source code.
package imports

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"molt/pkg/types"
)

// stdlib packages bundled with Python (subset of most common ones).
var stdlib = map[string]bool{
	"os": true, "sys": true, "re": true, "json": true, "math": true,
	"time": true, "datetime": true, "pathlib": true, "io": true,
	"collections": true, "itertools": true, "functools": true,
	"typing": true, "abc": true, "enum": true, "dataclasses": true,
	"contextlib": true, "copy": true, "random": true, "string": true,
	"struct": true, "hashlib": true, "hmac": true, "secrets": true,
	"base64": true, "urllib": true, "http": true, "email": true,
	"html": true, "xml": true, "csv": true, "configparser": true,
	"argparse": true, "logging": true, "unittest": true, "traceback": true,
	"threading": true, "multiprocessing": true, "asyncio": true,
	"concurrent": true, "subprocess": true, "socket": true,
	"ssl": true, "select": true, "signal": true, "shutil": true,
	"tempfile": true, "glob": true, "fnmatch": true, "stat": true,
	"zipfile": true, "tarfile": true, "gzip": true, "pickle": true,
	"sqlite3": true, "pprint": true, "textwrap": true, "inspect": true,
	"ast": true, "dis": true, "gc": true, "weakref": true,
	"importlib": true, "pkgutil": true, "platform": true, "builtins": true,
	"warnings": true, "linecache": true, "tokenize": true, "token": true,
	"numbers": true, "decimal": true, "fractions": true, "statistics": true,
}

// Analyser performs static import analysis.
type Analyser struct {
	ProjectDir string
}

// New creates an Analyser.
func New(projectDir string) *Analyser {
	return &Analyser{ProjectDir: projectDir}
}

// BuildGraph constructs the full import graph.
func (a *Analyser) BuildGraph() (*types.ImportGraph, error) {
	g := &types.ImportGraph{}
	nodeID := map[string]string{} // module → id

	sourceFiles := a.collectSourceFiles()

	// First pass: create nodes.
	for _, path := range sourceFiles {
		rel, _ := filepath.Rel(a.ProjectDir, path)
		mod := pathToModule(rel)
		id := fmt.Sprintf("n%d", len(g.Nodes))
		nodeID[mod] = id
		g.Nodes = append(g.Nodes, types.ImportNode{
			ID:     id,
			Module: mod,
			File:   filepath.ToSlash(rel),
			Kind:   "source",
			Used:   true,
		})
	}

	// Collect installed packages for classification.
	installedPkgs := a.installedPackages()

	// Second pass: extract imports and create edges.
	for _, path := range sourceFiles {
		rel, _ := filepath.Rel(a.ProjectDir, path)
		fromMod := pathToModule(rel)
		fromID := nodeID[fromMod]

		imports := extractImportsFromFile(path)
		for _, imp := range imports {
			topLevel := strings.SplitN(imp, ".", 2)[0]

			// Determine kind.
			kind := "unknown"
			pkg := ""
			if stdlib[topLevel] {
				kind = "stdlib"
			} else if installedPkgs[topLevel] != "" || installedPkgs[strings.ReplaceAll(topLevel, "_", "-")] != "" {
				kind = "third-party"
				pkg = topLevel
			} else {
				// Could be internal.
				kind = "source"
			}

			// Ensure node exists.
			if _, ok := nodeID[imp]; !ok {
				id := fmt.Sprintf("n%d", len(g.Nodes))
				nodeID[imp] = id
				g.Nodes = append(g.Nodes, types.ImportNode{
					ID:      id,
					Module:  imp,
					Kind:    kind,
					Package: pkg,
					Used:    true,
				})
			}

			g.Edges = append(g.Edges, types.ImportEdge{
				From: fromID,
				To:   nodeID[imp],
			})
		}
	}

	return g, nil
}

// PrintGraph prints the import graph.
func (a *Analyser) PrintGraph() error {
	g, err := a.BuildGraph()
	if err != nil {
		return err
	}

	// Group by kind.
	sourceNodes := []types.ImportNode{}
	stdlibNodes := []types.ImportNode{}
	thirdPartyNodes := []types.ImportNode{}

	seen := map[string]bool{}
	for _, n := range g.Nodes {
		if seen[n.Module] {
			continue
		}
		seen[n.Module] = true
		switch n.Kind {
		case "source":
			sourceNodes = append(sourceNodes, n)
		case "stdlib":
			stdlibNodes = append(stdlibNodes, n)
		case "third-party":
			thirdPartyNodes = append(thirdPartyNodes, n)
		}
	}

	fmt.Println("Import Graph")
	fmt.Println("============")
	fmt.Println()

	fmt.Printf("Source modules (%d):\n", len(sourceNodes))
	for _, n := range sourceNodes {
		if n.File != "" {
			fmt.Printf("  %s (%s)\n", n.Module, n.File)
		}
	}
	fmt.Println()

	fmt.Printf("Third-party imports (%d):\n", len(thirdPartyNodes))
	seen2 := map[string]bool{}
	for _, n := range thirdPartyNodes {
		if !seen2[n.Module] {
			seen2[n.Module] = true
			fmt.Printf("  %s\n", n.Module)
		}
	}
	fmt.Println()

	fmt.Printf("Stdlib imports (%d):\n", len(stdlibNodes))
	seen3 := map[string]bool{}
	for _, n := range stdlibNodes {
		if !seen3[n.Module] {
			seen3[n.Module] = true
			fmt.Printf("  %s\n", n.Module)
		}
	}
	fmt.Println()

	// Print edges (imports between source files).
	fmt.Println("Internal import relationships:")
	nodeByID := map[string]types.ImportNode{}
	for _, n := range g.Nodes {
		nodeByID[n.ID] = n
	}
	for _, e := range g.Edges {
		from := nodeByID[e.From]
		to := nodeByID[e.To]
		if from.Kind == "source" && to.Kind == "source" {
			fmt.Printf("  %s → %s\n", from.Module, to.Module)
		}
	}

	return nil
}

// PrintJSON prints the import graph as JSON.
func (a *Analyser) PrintJSON() error {
	g, err := a.BuildGraph()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(g)
}

// Unused finds packages that are installed but never imported.
func (a *Analyser) Unused() error {
	fmt.Println("Checking for unused dependencies...")
	fmt.Println()

	// Collect all imports from source.
	allImports := map[string]bool{}
	for _, path := range a.collectSourceFiles() {
		for _, imp := range extractImportsFromFile(path) {
			topLevel := strings.SplitN(imp, ".", 2)[0]
			allImports[strings.ToLower(topLevel)] = true
			allImports[strings.ToLower(strings.ReplaceAll(topLevel, "_", "-"))] = true
		}
	}

	// Get direct deps from pyproject.toml.
	directDeps := a.directDeps()
	installed := a.installedPackages()

	unused := []string{}
	for dep := range directDeps {
		depNorm := strings.ToLower(strings.ReplaceAll(dep, "-", "_"))
		depNorm2 := strings.ToLower(dep)
		if !allImports[depNorm] && !allImports[depNorm2] {
			unused = append(unused, dep)
		}
	}

	if len(unused) == 0 {
		fmt.Println("✓ All direct dependencies are imported by source code")
		return nil
	}

	fmt.Printf("Packages in pyproject.toml that are NOT imported by source:\n\n")
	for _, dep := range unused {
		ver := installed[strings.ToLower(dep)]
		fmt.Printf("  %-30s %s\n", dep, ver)
	}
	fmt.Println()
	fmt.Println("These may be:")
	fmt.Println("  - Transitive dependencies that became direct (safe to remove)")
	fmt.Println("  - Runtime deps not visible to static analysis (plugins, etc.)")
	fmt.Println("  - Genuinely unused (can be removed)")
	fmt.Println()
	fmt.Println("Use 'molt deps why <package>' to investigate each one.")
	return nil
}

// Missing finds packages that are imported but not in pyproject.toml.
func (a *Analyser) Missing() error {
	fmt.Println("Checking for missing dependencies...")
	fmt.Println()

	directDeps := a.directDeps()
	installedPkgs := a.installedPackages()

	// Walk source and find third-party imports not in direct deps.
	missingMap := map[string][]string{} // import → files that use it

	for _, path := range a.collectSourceFiles() {
		rel, _ := filepath.Rel(a.ProjectDir, path)
		for _, imp := range extractImportsFromFile(path) {
			topLevel := strings.ToLower(strings.SplitN(imp, ".", 2)[0])
			if stdlib[topLevel] {
				continue
			}
			// Is it a local module?
			if isLocalModule(imp, a.ProjectDir) {
				continue
			}
			// Is it an installed package?
			if installedPkgs[topLevel] == "" {
				continue
			}
			// Is it a direct dep?
			if !directDeps[topLevel] && !directDeps[strings.ReplaceAll(topLevel, "_", "-")] {
				missingMap[imp] = append(missingMap[imp], rel)
			}
		}
	}

	if len(missingMap) == 0 {
		fmt.Println("✓ All imports are accounted for in pyproject.toml")
		return nil
	}

	fmt.Println("Packages imported but NOT in pyproject.toml (only available as transitive deps):")
	fmt.Println()
	for imp, files := range missingMap {
		fmt.Printf("  %-30s used in: %s\n", imp, strings.Join(files[:min(3, len(files))], ", "))
	}
	fmt.Println()
	fmt.Println("⚠ These will break if the package that pulls them in changes.")
	fmt.Println("  Add them explicitly: molt add <package>")
	return nil
}

// Shadow finds your source files that shadow stdlib or package names.
func (a *Analyser) Shadow() error {
	fmt.Println("Checking for import shadowing...")
	fmt.Println()

	issues := 0
	for _, path := range a.collectSourceFiles() {
		rel, _ := filepath.Rel(a.ProjectDir, path)
		base := strings.TrimSuffix(filepath.Base(rel), ".py")
		if stdlib[base] {
			fmt.Printf("  ⚠ %s shadows stdlib module '%s'\n", rel, base)
			issues++
		}
	}

	if issues == 0 {
		fmt.Println("✓ No shadowing detected")
	}
	return nil
}

// Cycles finds circular imports.
func (a *Analyser) Cycles() error {
	g, err := a.BuildGraph()
	if err != nil {
		return err
	}

	// Build adjacency for source nodes only.
	nodeByID := map[string]types.ImportNode{}
	for _, n := range g.Nodes {
		nodeByID[n.ID] = n
	}

	adj := map[string][]string{}
	for _, e := range g.Edges {
		from := nodeByID[e.From]
		to := nodeByID[e.To]
		if from.Kind == "source" && to.Kind == "source" {
			adj[from.ID] = append(adj[from.ID], to.ID)
		}
	}

	// DFS cycle detection.
	visited := map[string]bool{}
	inStack := map[string]bool{}
	var cycle []string
	var hasCycle bool

	var dfs func(id string) bool
	dfs = func(id string) bool {
		visited[id] = true
		inStack[id] = true
		for _, next := range adj[id] {
			if !visited[next] {
				if dfs(next) {
					cycle = append([]string{nodeByID[next].Module}, cycle...)
					return true
				}
			} else if inStack[next] {
				cycle = []string{nodeByID[next].Module}
				hasCycle = true
				return true
			}
		}
		inStack[id] = false
		return false
	}

	for _, n := range g.Nodes {
		if !visited[n.ID] && n.Kind == "source" {
			if dfs(n.ID) {
				break
			}
		}
	}

	if hasCycle {
		fmt.Println("⚠ Circular imports detected:")
		fmt.Printf("  %s\n", strings.Join(cycle, " → "))
	} else {
		fmt.Println("✓ No circular imports detected")
	}
	return nil
}

// Trace shows exactly which file gets imported for a module name.
func (a *Analyser) Trace(moduleName string) error {
	fmt.Printf("Tracing import resolution for '%s'...\n\n", moduleName)

	venvPython := filepath.Join(a.ProjectDir, ".venv", "bin", "python3")

	script := fmt.Sprintf(`
import importlib.util, sys
spec = importlib.util.find_spec(%q)
if spec:
    print("Found:", spec.origin or spec.submodule_search_locations)
    print("Loader:", type(spec.loader).__name__)
else:
    print("NOT FOUND in sys.path")

print("sys.path search order:")
for i, p in enumerate(sys.path):
    print(f"  [{i}] {p}")
`, moduleName)

	cmd := exec.Command(venvPython, "-c", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// External lists all third-party imports mapped to packages.
func (a *Analyser) External() error {
	installedPkgs := a.installedPackages()
	imports := map[string]bool{}

	for _, path := range a.collectSourceFiles() {
		for _, imp := range extractImportsFromFile(path) {
			topLevel := strings.ToLower(strings.SplitN(imp, ".", 2)[0])
			if !stdlib[topLevel] && installedPkgs[topLevel] != "" {
				imports[topLevel] = true
			}
		}
	}

	fmt.Println("External (third-party) imports used by your source:")
	for imp := range imports {
		pkg := imp
		ver := installedPkgs[imp]
		fmt.Printf("  %-30s %s\n", pkg, ver)
	}
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (a *Analyser) collectSourceFiles() []string {
	var files []string
	skip := map[string]bool{
		".venv": true, "__pycache__": true, ".git": true,
		"dist": true, "build": true,
	}
	filepath.WalkDir(a.ProjectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(a.ProjectDir, path)
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if skip[part] {
				return filepath.SkipDir
			}
		}
		if strings.HasSuffix(path, ".py") {
			files = append(files, path)
		}
		return nil
	})
	return files
}

func (a *Analyser) installedPackages() map[string]string {
	pip := filepath.Join(a.ProjectDir, ".venv", "bin", "pip")
	out, err := exec.Command(pip, "list", "--format=json").Output()
	if err != nil {
		return map[string]string{}
	}
	var pkgs []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	json.Unmarshal(out, &pkgs)
	result := map[string]string{}
	for _, p := range pkgs {
		name := strings.ToLower(p.Name)
		result[name] = p.Version
		result[strings.ReplaceAll(name, "-", "_")] = p.Version
	}
	return result
}

func (a *Analyser) directDeps() map[string]bool {
	data, _ := os.ReadFile(filepath.Join(a.ProjectDir, "pyproject.toml"))
	deps := map[string]bool{}
	inDeps := false
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "dependencies") && strings.Contains(line, "[") {
			inDeps = true
			continue
		}
		if inDeps {
			if line == "]" {
				break
			}
			name := strings.Trim(line, `"', `)
			for _, sep := range []string{">=", "<=", "!=", "==", ">", "<"} {
				if idx := strings.Index(name, sep); idx > 0 {
					name = name[:idx]
				}
			}
			if name != "" {
				deps[strings.ToLower(name)] = true
				deps[strings.ToLower(strings.ReplaceAll(name, "-", "_"))] = true
			}
		}
	}
	return deps
}

func extractImportsFromFile(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var imports []string
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip comments and strings.
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "import ") {
			rest := strings.TrimPrefix(line, "import ")
			for _, part := range strings.Split(rest, ",") {
				pkg := strings.TrimSpace(strings.SplitN(part, " as ", 2)[0])
				pkg = strings.TrimSpace(pkg)
				if pkg != "" {
					imports = append(imports, pkg)
				}
			}
		}
		if strings.HasPrefix(line, "from ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				pkg := parts[1]
				if pkg != "." && !strings.HasPrefix(pkg, ".") {
					imports = append(imports, pkg)
				}
			}
		}
	}
	return imports
}

func pathToModule(rel string) string {
	rel = filepath.ToSlash(rel)
	rel = strings.TrimSuffix(rel, ".py")
	rel = strings.ReplaceAll(rel, "/", ".")
	rel = strings.TrimSuffix(rel, ".__init__")
	return rel
}

func isLocalModule(imp, projectDir string) bool {
	// Check if this is a local package.
	parts := strings.Split(imp, ".")
	if len(parts) == 0 {
		return false
	}
	// Check for __init__.py.
	pkgPath := filepath.Join(projectDir, parts[0])
	if _, err := os.Stat(pkgPath); err == nil {
		return true
	}
	pyPath := filepath.Join(projectDir, parts[0]+".py")
	_, err := os.Stat(pyPath)
	return err == nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
