package builder

import (
	"archive/tar"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"molt/pkg/types"
)

type Builder struct{ cfg types.BuildConfig }

func New(cfg types.BuildConfig) *Builder {
	if cfg.TargetOS == "" { cfg.TargetOS = runtime.GOOS }
	if cfg.TargetArch == "" { cfg.TargetArch = runtime.GOARCH }
	return &Builder{cfg: cfg}
}

func (b *Builder) Build() error {
	isCross := b.cfg.TargetOS != runtime.GOOS || b.cfg.TargetArch != runtime.GOARCH
	if isCross && b.cfg.CrossBuildMode != types.CrossBuildBestEffort {
		return fmt.Errorf("cross-build (%s/%s -> %s/%s): use --best-effort for testing",
			runtime.GOOS, runtime.GOARCH, b.cfg.TargetOS, b.cfg.TargetArch)
	}
	fmt.Printf("Building %s v%s (%s/%s)...\n", b.cfg.Name, b.cfg.Version, b.cfg.TargetOS, b.cfg.TargetArch)
	launcherPath, err := compileLauncher(b.cfg.TargetOS, b.cfg.TargetArch)
	if err != nil { return fmt.Errorf("compile launcher: %w", err) }
	defer os.Remove(launcherPath)
	m := &types.Manifest{
		AppName: b.cfg.Name, Version: b.cfg.Version,
		MainModule: b.cfg.Name + ".main", Profile: b.cfg.Profile,
		TargetOS: b.cfg.TargetOS, TargetArch: b.cfg.TargetArch,
		BuildTime: time.Now().UTC().Format(time.RFC3339),
	}
	archivePath, err := createArchive(m, b.cfg.ProjectPath, b.cfg.EmbedFiles)
	if err != nil { return fmt.Errorf("create archive: %w", err) }
	defer os.Remove(archivePath)
	suffix := ""; if b.cfg.TargetOS == "windows" { suffix = ".exe" }
	outputPath := b.cfg.OutputPath + suffix
	if err := assembleBinary(launcherPath, archivePath, outputPath); err != nil {
		return fmt.Errorf("assemble binary: %w", err)
	}
	info, _ := os.Stat(outputPath)
	sizeMB := float64(0); if info != nil { sizeMB = float64(info.Size()) / 1_000_000 }
	fmt.Printf("  Created: %s (%.1fMB)\n", outputPath, sizeMB)
	fmt.Printf("  Install: molt_INSTALL_BASE=/opt ./%s install\n", filepath.Base(outputPath))
	return nil
}

func Capture(cfg types.CaptureConfig) error {
	if cfg.TargetOS == "" { cfg.TargetOS = runtime.GOOS }
	if cfg.TargetArch == "" { cfg.TargetArch = runtime.GOARCH }
	fmt.Printf("Capturing environment (%s/%s)...\n", cfg.TargetOS, cfg.TargetArch)
	m := &types.Manifest{AppName: "__placeholder__", Version: "__placeholder__",
		MainModule: "__placeholder__", TargetOS: cfg.TargetOS, TargetArch: cfg.TargetArch,
		BuildTime: time.Now().UTC().Format(time.RFC3339),
	}
	outPath := cfg.OutputPath
	if outPath == "" { outPath = fmt.Sprintf("%s-%s.manifest.json", runtime.GOOS, runtime.GOARCH) }
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil { return err }
	if err := os.WriteFile(outPath, data, 0o644); err != nil { return err }
	fmt.Printf("  Manifest: %s\n", outPath)
	return nil
}

type Assembler struct{ cfg types.AssembleConfig }

func NewAssembler(cfg types.AssembleConfig) *Assembler {
	if cfg.TargetOS == "" { cfg.TargetOS = runtime.GOOS }
	if cfg.TargetArch == "" { cfg.TargetArch = runtime.GOARCH }
	return &Assembler{cfg: cfg}
}

func (a *Assembler) Assemble(name, version string) error {
	manifestData, err := os.ReadFile(a.cfg.ManifestPath)
	if err != nil { return fmt.Errorf("read manifest: %w", err) }
	var m types.Manifest
	if err := json.Unmarshal(manifestData, &m); err != nil { return err }
	m.AppName, m.Version, m.MainModule, m.Profile = name, version, name+".main", a.cfg.Profile
	launcherPath, err := compileLauncher(a.cfg.TargetOS, a.cfg.TargetArch)
	if err != nil { return err }
	defer os.Remove(launcherPath)
	archivePath, err := createArchive(&m, a.cfg.ProjectPath, a.cfg.EmbedFiles)
	if err != nil { return err }
	defer os.Remove(archivePath)
	suffix := ""; if a.cfg.TargetOS == "windows" { suffix = ".exe" }
	return assembleBinary(launcherPath, archivePath, a.cfg.OutputPath+suffix)
}

func compileLauncher(targetOS, targetArch string) (string, error) {
	suffix := ""; if targetOS == "windows" { suffix = ".exe" }
	tmp, err := os.CreateTemp("", "molt-launcher-*"+suffix)
	if err != nil { return "", err }
	tmp.Close()
	if _, err := exec.LookPath("go"); err != nil {
		os.WriteFile(tmp.Name(), []byte("#!/bin/sh\necho stub\nexit 1\n"), 0o755)
		return tmp.Name(), nil
	}
	cmd := exec.Command("go", "build", "-o", tmp.Name(), "molt/internal/launcher")
	cmd.Env = append(os.Environ(), "GOOS="+targetOS, "GOARCH="+targetArch, "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil { return "", fmt.Errorf("go build launcher: %w\n%s", err, out) }
	fmt.Printf("  Compiled launcher (%s/%s)\n", targetOS, targetArch)
	return tmp.Name(), nil
}

func createArchive(m *types.Manifest, projectPath string, embedFiles []string) (string, error) {
	tmp, err := os.CreateTemp("", "molt-archive-*.tar.gz")
	if err != nil { return "", err }
	defer tmp.Close()
	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)
	manifestData, _ := json.MarshalIndent(m, "", "  ")
	addToTar(tw, ".molt/manifest.json", manifestData)
	if projectPath != "" { addSourceFiles(tw, projectPath) }
	for _, ef := range embedFiles {
		if data, err := os.ReadFile(ef); err == nil {
			addToTar(tw, "extras/"+filepath.Base(ef), data)
		}
	}
	tw.Close(); gz.Close()
	return tmp.Name(), nil
}

func addSourceFiles(tw *tar.Writer, projectPath string) error {
	allowed := map[string]bool{".py":true,".toml":true,".lock":true,".json":true,".cfg":true,".ini":true,".txt":true}
	skip := map[string]bool{".venv":true,"__pycache__":true,".git":true,"dist":true,"build":true}
	return filepath.WalkDir(projectPath, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() { return nil }
		rel, _ := filepath.Rel(projectPath, path)
		parts := filepath.SplitList(rel)
		for _, p := range parts { if skip[p] { return filepath.SkipDir } }
		if !allowed[filepath.Ext(path)] { return nil }
		data, err := os.ReadFile(path)
		if err != nil { return nil }
		return addToTar(tw, "src/"+filepath.ToSlash(rel), data)
	})
}

func assembleBinary(launcherPath, archivePath, outputPath string) error {
	launcher, err := os.ReadFile(launcherPath); if err != nil { return err }
	archive, err := os.ReadFile(archivePath); if err != nil { return err }
	out, err := os.Create(outputPath); if err != nil { return err }
	defer out.Close()
	out.Write(launcher)
	archiveOffset := int64(len(launcher))
	out.Write(archive)
	binary.Write(out, binary.LittleEndian, archiveOffset)
	return out.Chmod(0o755)
}

func addToTar(tw *tar.Writer, name string, data []byte) error {
	tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))})
	_, err := tw.Write(data)
	return err
}
