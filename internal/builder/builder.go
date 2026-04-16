package builder

import (
	"archive/tar"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"molt/embedder"
	"molt/pkg/types"
)

type Builder struct {
	cfg types.BuildConfig
}

func New(cfg types.BuildConfig) *Builder {
	if cfg.TargetOS == "" {
		cfg.TargetOS = runtime.GOOS
	}
	if cfg.TargetArch == "" {
		cfg.TargetArch = runtime.GOARCH
	}
	if cfg.EmbedIgnoreFile == "" {
		cfg.EmbedIgnoreFile = ".moltignore"
	}
	// Strict mode enabled by default for security
	if !cfg.EmbedStrict {
		cfg.EmbedStrict = true
	}
	return &Builder{cfg: cfg}
}

func (b *Builder) Build() error {
	isCross := b.cfg.TargetOS != runtime.GOOS || b.cfg.TargetArch != runtime.GOARCH
	if isCross && b.cfg.CrossBuildMode != types.CrossBuildBestEffort {
		return fmt.Errorf("cross-build (%s/%s -> %s/%s): use --best-effort for testing",
			runtime.GOOS, runtime.GOARCH, b.cfg.TargetOS, b.cfg.TargetArch)
	}

	fmt.Printf("Building %s v%s (%s/%s)...\n", b.cfg.Name, b.cfg.Version, b.cfg.TargetOS, b.cfg.TargetArch)

	// Step 1: Compile the launcher binary for target platform
	launcherPath, err := compileLauncher(b.cfg.TargetOS, b.cfg.TargetArch)
	if err != nil {
		return fmt.Errorf("compile launcher: %w", err)
	}
	defer os.Remove(launcherPath)

	// Step 2: Build manifest
	m := &types.Manifest{
		AppName:    b.cfg.Name,
		Version:    b.cfg.Version,
		MainModule: b.cfg.Name + ".main",
		Profile:    b.cfg.Profile,
		TargetOS:   b.cfg.TargetOS,
		TargetArch: b.cfg.TargetArch,
		BuildTime:  time.Now().UTC().Format(time.RFC3339),
	}

	// Step 3: Create strict payload using embedder
	payloadPath, err := b.createStrictPayload(m)
	if err != nil {
		return fmt.Errorf("create payload: %w", err)
	}
	defer os.Remove(payloadPath)

	// Step 4: Assemble final binary
	suffix := ""
	if b.cfg.TargetOS == "windows" {
		suffix = ".exe"
	}
	outputPath := b.cfg.OutputPath + suffix
	if err := assembleBinary(launcherPath, payloadPath, outputPath); err != nil {
		return fmt.Errorf("assemble binary: %w", err)
	}

	info, _ := os.Stat(outputPath)
	sizeMB := float64(0)
	if info != nil {
		sizeMB = float64(info.Size()) / 1_000_000
	}
	fmt.Printf("  Created: %s (%.1fMB)\n", outputPath, sizeMB)
	fmt.Printf("  Install: molt_INSTALL_BASE=/opt ./%s install\n", filepath.Base(outputPath))
	return nil
}

// createStrictPayload uses the embedder package to create a filtered tar.gz payload.
func (b *Builder) createStrictPayload(m *types.Manifest) (string, error) {
	tmp, err := os.CreateTemp("", "molt-payload-*.tar.gz")
	if err != nil {
		return "", err
	}
	tmp.Close()
	payloadPath := tmp.Name()

	// First, write the manifest to a temp file so embedder can include it
	manifestDir := filepath.Join(b.cfg.ProjectPath, ".molt")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		return "", err
	}
	manifestPath := filepath.Join(manifestDir, "manifest.json")
	manifestData, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(manifestPath, manifestData, 0o644); err != nil {
		return "", err
	}
	defer os.Remove(manifestPath) // clean up temp manifest

	cfg := embedder.Config{
		RootDir:         b.cfg.ProjectPath,
		OutputFile:      payloadPath,
		PrefixInTar:     "src/", // matches launcher extraction logic
		IgnoreFile:      b.cfg.EmbedIgnoreFile,
		FailOnSensitive: b.cfg.EmbedStrict,
	}

	fmt.Printf("  → Embedding files (strict=%v)...\n", b.cfg.EmbedStrict)
	if err := embedder.Run(cfg); err != nil {
		return "", fmt.Errorf("embed payload: %w", err)
	}

	// Verify payload isn't empty
	info, err := os.Stat(payloadPath)
	if err != nil || info.Size() == 0 {
		return "", fmt.Errorf("payload is empty — check .moltignore rules")
	}
	fmt.Printf("  ✓ Payload: %.1f KB\n", float64(info.Size())/1024)
	return payloadPath, nil
}

func Capture(cfg types.CaptureConfig) error {
	if cfg.TargetOS == "" {
		cfg.TargetOS = runtime.GOOS
	}
	if cfg.TargetArch == "" {
		cfg.TargetArch = runtime.GOARCH
	}
	fmt.Printf("Capturing environment (%s/%s)...\n", cfg.TargetOS, cfg.TargetArch)
	m := &types.Manifest{
		AppName: "__placeholder__", Version: "__placeholder__",
		MainModule: "__placeholder__", TargetOS: cfg.TargetOS, TargetArch: cfg.TargetArch,
		BuildTime: time.Now().UTC().Format(time.RFC3339),
	}
	outPath := cfg.OutputPath
	if outPath == "" {
		outPath = fmt.Sprintf("%s-%s.manifest.json", runtime.GOOS, runtime.GOARCH)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("  Manifest: %s\n", outPath)
	return nil
}

type Assembler struct{ cfg types.AssembleConfig }

func NewAssembler(cfg types.AssembleConfig) *Assembler {
	if cfg.TargetOS == "" {
		cfg.TargetOS = runtime.GOOS
	}
	if cfg.TargetArch == "" {
		cfg.TargetArch = runtime.GOARCH
	}
	return &Assembler{cfg: cfg}
}

func (a *Assembler) Assemble(name, version string) error {
	manifestData, err := os.ReadFile(a.cfg.ManifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var m types.Manifest
	if err := json.Unmarshal(manifestData, &m); err != nil {
		return err
	}
	m.AppName, m.Version, m.MainModule, m.Profile = name, version, name+".main", a.cfg.Profile

	launcherPath, err := compileLauncher(a.cfg.TargetOS, a.cfg.TargetArch)
	if err != nil {
		return err
	}
	defer os.Remove(launcherPath)

	// For assembler, we assume the manifest already contains the payload info
	// or we re-embed from the captured project path
	payloadPath, err := a.createPayload(&m)
	if err != nil {
		return err
	}
	defer os.Remove(payloadPath)

	suffix := ""
	if a.cfg.TargetOS == "windows" {
		suffix = ".exe"
	}
	return assembleBinary(launcherPath, payloadPath, a.cfg.OutputPath+suffix)
}

func (a *Assembler) createPayload(m *types.Manifest) (string, error) {
	tmp, err := os.CreateTemp("", "molt-payload-*.tar.gz")
	if err != nil {
		return "", err
	}
	tmp.Close()

	// Write manifest into payload
	manifestData, _ := json.MarshalIndent(m, "", "  ")
	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)
	_ = addToTar(tw, ".molt/manifest.json", manifestData)
	tw.Close()
	gz.Close()
	tmp.Close()

	return tmp.Name(), nil
}

func compileLauncher(targetOS, targetArch string) (string, error) {
	if err := installGOIfNotInstalled(); err != nil {
		return "", fmt.Errorf("bootstrap go: %w", err)
	}

	suffix := ""
	if targetOS == "windows" {
		suffix = ".exe"
	}
	tmp, err := os.CreateTemp("", "molt-launcher-*"+suffix)
	if err != nil {
		return "", err
	}
	tmp.Close()

	goBin := filepath.Join(getGOOutputDir(), "bin", "go")
	if runtime.GOOS == "windows" {
		goBin += ".exe"
	}

	home, _ := os.UserHomeDir()
	if home == "" {
		home = os.TempDir()
	}
	goCache := filepath.Join(home, ".cache", "go-build")
	goPath := filepath.Join(home, "go")
	os.MkdirAll(goCache, 0o755)
	os.MkdirAll(goPath, 0o755)

	cmd := exec.Command(goBin, "build", "-o", tmp.Name(), "molt/internal/launcher")
	cmd.Env = []string{
		"GOOS=" + targetOS,
		"GOARCH=" + targetArch,
		"CGO_ENABLED=0",
		"HOME=" + home,
		"GOCACHE=" + goCache,
		"GOPATH=" + goPath,
		"GO111MODULE=on",
	}
	for _, key := range []string{"PATH", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "TMPDIR", "USER", "LOGNAME"} {
		if v := os.Getenv(key); v != "" {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go build launcher: %w\n%s", err, out)
	}
	fmt.Printf("  Compiled launcher (%s/%s)\n", targetOS, targetArch)
	return tmp.Name(), nil
}

func assembleBinary(launcherPath, archivePath, outputPath string) error {
	launcher, err := os.ReadFile(launcherPath)
	if err != nil {
		return err
	}
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		return err
	}
	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := out.Write(launcher); err != nil {
		return err
	}
	archiveOffset := int64(len(launcher))
	if _, err := out.Write(archive); err != nil {
		return err
	}
	if err := binary.Write(out, binary.LittleEndian, archiveOffset); err != nil {
		return err
	}
	return out.Chmod(0o755)
}

func addToTar(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(data)),
	}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

const goVersion = "1.24.1"

func getGOOutputDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".molt", "go", goVersion)
}

func installGOIfNotInstalled() error {
	goRoot := getGOOutputDir()
	binGo := filepath.Join(goRoot, "bin", "go")
	if runtime.GOOS == "windows" {
		binGo += ".exe"
	}
	if _, err := os.Stat(binGo); err == nil {
		return nil
	}
	fmt.Printf("  Bootstrapping Go %s...\n", goVersion)
	return downloadAndExtractGo(goVersion, goRoot, runtime.GOOS, runtime.GOARCH)
}

func downloadAndExtractGo(version, destDir, hostOS, hostArch string) error {
	platform := fmt.Sprintf("%s-%s", hostOS, hostArch)
	url := fmt.Sprintf("https://go.dev/dl/go%s.%s.tar.gz", version, platform)

	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(hdr.Name, "go/")
		if rel == "" {
			continue
		}
		outPath := filepath.Join(destDir, rel)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(outPath, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
				return err
			}
			os.Remove(outPath)
			if err := os.Symlink(hdr.Linkname, outPath); err != nil {
				return err
			}
		}
	}
	return nil
}
