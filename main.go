package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"molt/internal/adopt"
	"molt/internal/builder"
	"molt/internal/integrity"
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

	case "uv":
		err = cmdUV(os.Args[2:])
	case "doctor":
		err = cmdDoctor()

	case "version", "--version":
		fmt.Printf("molt version=%q date=%q commit=%q\n", version, date, commit)
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

Build:
  build    [flags] [project-path]   Build self-contained binary
  capture  [flags] [project-path]   Capture environment manifest
  assemble [flags]                  Assemble binary from a manifest

Adoption:
  adopt    [dir] [--non-interactive] [--force]   Scaffold molt.yaml

Integrity:
  verify-binary <binary> [--deep]   Verify binary integrity
  inspect <binary> [--files] [--json]   Show embedded manifest
  diff <a> <b>                      Compare two builds/manifests

uv:
  uv path                           Print resolved uv binary path
  uv version                        Print uv version

Diagnostics:
  doctor                            System/tool diagnostics
  version                           Print molt version

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

// ── Adopt + integrity (merged from new_commands.go) ──────────────────────────

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

// ── uv ────────────────────────────────────────────────────────────────────────

func cmdUV(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: molt uv <path|version>")
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
		return fmt.Errorf("uv subcommand %q not available in this build", args[0])
	}
}

// ── doctor ────────────────────────────────────────────────────────────────────

func cmdDoctor() error {
	fmt.Printf("molt %s\n", version)
	fmt.Printf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println("─────────────────────────────────────")

	uvPath, uvErr := uvbin.Find()
	if uvErr == nil {
		uvVer, _ := uvbin.Version()
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
