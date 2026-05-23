package glue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"molt/internal/projstate"
)

// Artifact describes one compiled glue module (server binary + Python client).
type Artifact struct {
	Module    string // Python module name
	ServerBin string // absolute path to the compiled server binary in the glue dir
	PyClient  string // absolute path to the generated .py client
}

// GlueCacheEntry is written as meta.json in ~/.molt/native/<hash>/.
type GlueCacheEntry struct {
	Module     string    `json:"module"`
	Lang       string    `json:"lang"`
	Transport  string    `json:"transport"`
	CompiledAt time.Time `json:"compiled_at"`
}

// BuildAll builds all glue modules declared in cfg and places results in
// projstate.Dir(projectDir)/glue/. Returns the list of built artifacts.
func BuildAll(cfg GlueConfig, projectDir string, verbose bool) ([]Artifact, error) {
	if len(cfg.Modules) == 0 {
		return nil, nil
	}

	glueDir := GlueDir(projectDir)
	if err := os.MkdirAll(glueDir, 0o755); err != nil {
		return nil, fmt.Errorf("create glue dir: %w", err)
	}

	var arts []Artifact
	for _, m := range cfg.Modules {
		art, err := buildModule(m, projectDir, glueDir, verbose)
		if err != nil {
			return arts, fmt.Errorf("glue build %s: %w", m.Module, err)
		}
		arts = append(arts, art)
	}
	return arts, nil
}

// GlueDir returns the per-project glue output directory.
func GlueDir(projectDir string) string {
	return filepath.Join(projstate.Dir(projectDir), "glue")
}

// buildModule builds a single glue module.
func buildModule(m GlueModuleConfig, projectDir, glueDir string, verbose bool) (Artifact, error) {
	// ── 1. Compute cache key ──────────────────────────────────────────────────
	hash, err := hashModule(m, projectDir)
	if err != nil {
		return Artifact{}, fmt.Errorf("hash module: %w", err)
	}

	serverName := m.Module + "_server"
	if runtime.GOOS == "windows" {
		serverName += ".exe"
	}

	// Check cache.
	cacheRoot, err := cacheRoot()
	if err != nil {
		return Artifact{}, err
	}
	cacheEntryDir := filepath.Join(cacheRoot, hash)
	cachedBin := filepath.Join(cacheEntryDir, serverName)

	serverDest := filepath.Join(glueDir, serverName)
	pyDest := filepath.Join(glueDir, m.Module+".py")

	if _, err := os.Stat(cachedBin); err == nil {
		// Cache hit — copy binary into glue dir.
		if verbose {
			fmt.Printf("→ glue/%s: cache hit\n", m.Module)
		}
		if err := copyFile(cachedBin, serverDest); err != nil {
			return Artifact{}, err
		}
	} else {
		// Cache miss — build from source.
		if verbose {
			fmt.Printf("→ glue/%s: building (%s/%s)...\n", m.Module, m.Lang, m.EffectiveTransport())
		}
		if err := compileModule(m, projectDir, glueDir, cacheEntryDir, serverDest, hash, verbose); err != nil {
			return Artifact{}, err
		}
	}

	// Always (re)generate Python client — it's fast and ensures it's up to date.
	if err := GeneratePythonClient(m, glueDir); err != nil {
		return Artifact{}, fmt.Errorf("generate Python client: %w", err)
	}

	return Artifact{
		Module:    m.Module,
		ServerBin: serverDest,
		PyClient:  pyDest,
	}, nil
}

// compileModule handles the full build cycle: prepare build dir → generate
// server source → run lib_setup_cmd if needed → run build_cmd → cache result.
func compileModule(m GlueModuleConfig, projectDir, glueDir, cacheDir, serverDest, hash string, verbose bool) error {
	driver, ok, err := Find(m.Lang)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no glue driver registered for lang %q\n"+
			"(install one with: molt glue-driver add %s --build \"<cmd>\")", m.Lang, m.Lang)
	}

	// Create a temporary build directory.
	buildDir, err := os.MkdirTemp("", "molt-glue-"+m.Module+"-")
	if err != nil {
		return fmt.Errorf("create build dir: %w", err)
	}
	defer os.RemoveAll(buildDir)

	serverName := m.Module + "_server"
	if runtime.GOOS == "windows" {
		serverName += ".exe"
	}
	outputPath := serverDest

	switch strings.ToLower(m.Lang) {
	case "go":
		if err := buildGoModule(m, projectDir, buildDir, outputPath, driver, verbose); err != nil {
			return err
		}
	case "rust":
		if err := buildRustModule(m, projectDir, buildDir, outputPath, driver, verbose); err != nil {
			return err
		}
	default:
		if err := buildCustomModule(m, projectDir, buildDir, outputPath, driver, verbose); err != nil {
			return err
		}
	}

	// Cache the compiled binary.
	if err := os.MkdirAll(cacheDir, 0o755); err == nil {
		cachedBin := filepath.Join(cacheDir, serverName)
		_ = copyFile(outputPath, cachedBin)
		meta := GlueCacheEntry{
			Module:     m.Module,
			Lang:       m.Lang,
			Transport:  m.EffectiveTransport(),
			CompiledAt: time.Now(),
		}
		if data, err := json.MarshalIndent(meta, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(cacheDir, "meta.json"), data, 0o644)
		}
	}

	return nil
}

// buildGoModule generates and compiles a Go glue server.
func buildGoModule(m GlueModuleConfig, projectDir, buildDir, outputPath string, driver GlueDriver, verbose bool) error {
	var importPath string
	var libSetupNeeded bool

	if m.SrcIsLibrary() {
		// ── Library import (stdlib or third-party) ────────────────────────────
		importPath = m.Src
		libSetupNeeded = isThirdPartyGoPath(m.Src)

		// For stdlib, no require is needed.
		// For third-party, we write a clean go.mod and let `go get` add the version.
		goMod := fmt.Sprintf("module molt-glue-%s\n\ngo 1.21\n", m.Module)
		if err := os.WriteFile(filepath.Join(buildDir, "go.mod"), []byte(goMod), 0o644); err != nil {
			return err
		}
	} else {
		// ── Local package ─────────────────────────────────────────────────────
		srcAbs := m.Src
		if !filepath.IsAbs(srcAbs) {
			srcAbs = filepath.Join(projectDir, srcAbs)
		}

		// Determine module path.
		modPath := m.PkgModule
		if modPath == "" {
			modPath = "user/" + m.Module
		}
		importPath = modPath

		// Check if src already has a go.mod; if not, generate one.
		srcGoMod := filepath.Join(srcAbs, "go.mod")
		if _, err := os.Stat(srcGoMod); os.IsNotExist(err) {
			generated := fmt.Sprintf("module %s\n\ngo 1.21\n", modPath)
			if err := os.WriteFile(srcGoMod, []byte(generated), 0o644); err != nil {
				return fmt.Errorf("write go.mod for user package: %w", err)
			}
		}

		// Generate build dir go.mod with replace directive.
		goMod := fmt.Sprintf(
			"module molt-glue-%s\n\ngo 1.21\n\nrequire %s v0.0.0\n\nreplace %s => %s\n",
			m.Module, modPath, modPath, filepath.ToSlash(srcAbs),
		)
		if err := os.WriteFile(filepath.Join(buildDir, "go.mod"), []byte(goMod), 0o644); err != nil {
			return err
		}
	}

	// Generate server.go.
	if err := GenerateGoServer(m, buildDir, importPath); err != nil {
		return fmt.Errorf("generate Go server: %w", err)
	}

	// Run lib_setup_cmd if needed (for third-party modules).
	if libSetupNeeded && driver.LibSetupCmd != "" {
		tokens := map[string]string{
			"import_path": m.Src,
			"module":      m.Module,
		}
		cmd := ExpandTokens(driver.LibSetupCmd, tokens)
		if err := runInDir(buildDir, cmd, verbose); err != nil {
			return fmt.Errorf("go get %s: %w", m.Src, err)
		}
	}

	// Run build_cmd.
	tokens := map[string]string{
		"server_dir":  buildDir,
		"server_file": filepath.Join(buildDir, "server.go"),
		"output":      outputPath,
		"import_path": importPath,
		"module":      m.Module,
	}
	buildCmd := ExpandTokens(driver.BuildCmd, tokens)
	if err := runInDir(buildDir, buildCmd, verbose); err != nil {
		return fmt.Errorf("go build: %w", err)
	}
	return nil
}

// buildRustModule generates and compiles a Rust glue server.
func buildRustModule(m GlueModuleConfig, projectDir, buildDir, outputPath string, driver GlueDriver, verbose bool) error {
	var srcAbsPath string // for include!-style single-file glue

	if !m.SrcIsLibrary() && strings.HasSuffix(m.Src, ".rs") {
		// Single .rs file — use include! in generated main.rs.
		srcAbsPath = m.Src
		if !filepath.IsAbs(srcAbsPath) {
			srcAbsPath = filepath.Join(projectDir, srcAbsPath)
		}
	}

	// Generate src/main.rs.
	if err := GenerateRustServer(m, buildDir, srcAbsPath); err != nil {
		return fmt.Errorf("generate Rust server: %w", err)
	}

	// Generate Cargo.toml.
	cargoToml := fmt.Sprintf(`[package]
name = "molt-glue-%s"
version = "0.1.0"
edition = "2021"

[dependencies]
serde = { version = "1", features = ["derive"] }
serde_json = "1"
base64 = "0.22"
`, m.Module)

	// Add user crates. TOML config uses single quotes; Cargo.toml needs double quotes.
	for _, c := range m.Crates {
		cargoToml += strings.ReplaceAll(c, "'", "\"") + "\n"
	}

	// If src is a directory with Cargo.toml, add as a path dependency.
	if !strings.HasSuffix(m.Src, ".rs") && !m.SrcIsLibrary() {
		srcAbs := m.Src
		if !filepath.IsAbs(srcAbs) {
			srcAbs = filepath.Join(projectDir, srcAbs)
		}
		cargoToml += fmt.Sprintf("%s = { path = \"%s\" }\n", m.Module, filepath.ToSlash(srcAbs))
	}

	cargoTomlPath := filepath.Join(buildDir, "Cargo.toml")
	if err := os.WriteFile(cargoTomlPath, []byte(cargoToml), 0o644); err != nil {
		return err
	}

	// Determine release binary path.
	releaseBin := filepath.Join(buildDir, "target", "release", "molt-glue-"+m.Module)
	if runtime.GOOS == "windows" {
		releaseBin += ".exe"
	}

	// Run build_cmd.
	tokens := map[string]string{
		"server_dir":  buildDir,
		"server_file": filepath.Join(buildDir, "src", "main.rs"),
		"cargo_toml":  cargoTomlPath,
		"release_bin": releaseBin,
		"output":      outputPath,
		"module":      m.Module,
	}
	buildCmd := ExpandTokens(driver.BuildCmd, tokens)
	if err := runInDir(buildDir, buildCmd, verbose); err != nil {
		return fmt.Errorf("cargo build: %w", err)
	}
	return nil
}

// buildCustomModule handles user-added drivers (Nim, Zig, etc.).
func buildCustomModule(m GlueModuleConfig, projectDir, buildDir, outputPath string, driver GlueDriver, verbose bool) error {
	if driver.ServerTemplate == "" {
		return fmt.Errorf("driver for lang %q has no server_template; cannot generate server source", m.Lang)
	}

	// For custom drivers, the server template renders to a file named server.<ext>.
	// We don't know the ext, so we use the template filename extension.
	ext := filepath.Ext(driver.ServerTemplate)
	if ext == ".tmpl" {
		ext = filepath.Ext(strings.TrimSuffix(driver.ServerTemplate, ".tmpl"))
	}
	serverFile := filepath.Join(buildDir, "server"+ext)

	// Run lib_setup_cmd if needed.
	if m.SrcIsLibrary() && driver.LibSetupCmd != "" {
		tokens := map[string]string{"import_path": m.Src, "module": m.Module}
		if err := runInDir(buildDir, ExpandTokens(driver.LibSetupCmd, tokens), verbose); err != nil {
			return err
		}
	}

	tokens := map[string]string{
		"server_dir":  buildDir,
		"server_file": serverFile,
		"output":      outputPath,
		"import_path": m.Src,
		"module":      m.Module,
	}
	buildCmd := ExpandTokens(driver.BuildCmd, tokens)
	return runInDir(buildDir, buildCmd, verbose)
}

// runInDir runs a shell command in a directory, printing it when verbose.
func runInDir(dir, cmdStr string, verbose bool) error {
	if verbose {
		fmt.Printf("  $ %s\n", cmdStr)
	}
	var c *exec.Cmd
	if runtime.GOOS == "windows" {
		c = exec.Command("cmd", "/c", cmdStr)
	} else {
		c = exec.Command("sh", "-c", cmdStr)
	}
	c.Dir = dir
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// hashModule produces a short content hash for the module config + source files.
func hashModule(m GlueModuleConfig, projectDir string) (string, error) {
	h := sha256.New()
	// Hash config fields.
	fmt.Fprintf(h, "%s|%s|%s|%s|", m.Module, m.Lang, m.Src, m.EffectiveTransport())
	for _, fn := range m.Fns {
		fmt.Fprintf(h, "%s(%v)%s|", fn.Name, fn.Args, fn.Returns)
	}

	// Hash source file contents (if local).
	if !m.SrcIsLibrary() {
		srcAbs := m.Src
		if !filepath.IsAbs(srcAbs) {
			srcAbs = filepath.Join(projectDir, srcAbs)
		}
		if info, err := os.Stat(srcAbs); err == nil {
			if info.IsDir() {
				// Hash all .go/.rs files in the directory.
				_ = filepath.WalkDir(srcAbs, func(p string, d os.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return nil
					}
					ext := filepath.Ext(p)
					if ext == ".go" || ext == ".rs" {
						if data, err := os.ReadFile(p); err == nil {
							h.Write(data)
						}
					}
					return nil
				})
			} else {
				if data, err := os.ReadFile(srcAbs); err == nil {
					h.Write(data)
				}
			}
		}
	}

	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// cacheRoot returns ~/.molt/native (shared with the native module cache).
func cacheRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".molt", "native"), nil
}

// copyFile copies src → dst, creating dst's parent dirs.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// isThirdPartyGoPath returns true when the Go import path requires `go get`
// (i.e. it's not a stdlib path). Third-party paths contain a domain in their
// first path segment.
func isThirdPartyGoPath(importPath string) bool {
	parts := strings.SplitN(importPath, "/", 2)
	if len(parts) == 0 {
		return false
	}
	return strings.Contains(parts[0], ".")
}

// goModuleOf returns the top-level module path for a Go import path.
// e.g. "golang.org/x/crypto/sha3" → "golang.org/x/crypto"
func goModuleOf(importPath string) string {
	parts := strings.Split(importPath, "/")
	if len(parts) < 3 {
		return importPath
	}
	// Standard module layout: host/org/repo
	return strings.Join(parts[:3], "/")
}
