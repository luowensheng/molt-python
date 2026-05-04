// internal/deps_strategy/deps_strategy.go
//
// Package deps_strategy implements the five ways molt can install Python
// dependencies on the target machine. The strategy is declared in molt.yaml
// and chosen at build time, not at install time — a binary commits to ONE
// way of getting its deps in place.
//
// Each strategy is an Install(ctx) method with a uniform signature so the
// launcher can drive whichever strategy the manifest pinned without
// caring about the mechanics.
//
// Strategies:
//
//   requirements  — `uv pip install -r <file>` for each listed file
//   pyproject     — `uv sync --frozen` (or `uv sync` if no lockfile)
//   poetry        — `poetry export` piped into `uv pip install -r -`
//   pipenv        — `pipenv requirements` piped into `uv pip install -r -`
//   none          — do nothing (deps are already vendored)
//
// All strategies run uv via uvbin.Find/Ensure so molt's pinned uv is used,
// not whatever happens to be on the host PATH.

package deps_strategy

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"molt/internal/uvbin"
	"molt/pkg/types"
)

// Strategy installs Python dependencies into installDir/.venv.
type Strategy interface {
	// Name returns the YAML-level strategy name ("requirements", "poetry", etc.).
	Name() string

	// Install installs deps into the venv rooted at installDir/.venv.
	// It is responsible for respecting the `files` and `extra_args` fields
	// of MoltDeps. stdout/stderr should be streamed to the provided writer
	// so the launcher can surface progress in real time.
	Install(installDir string, out io.Writer) error
}

// New returns a Strategy matching cfg.Strategy. Returns an error if the
// strategy name is unknown — by now the config was already validated, but
// defending against programmer error in callers is cheap.
func New(cfg *types.MoltDeps) (Strategy, error) {
	if cfg == nil {
		// No deps block: behave like pyproject to stay compatible with
		// existing molt projects that have only pyproject.toml.
		return &pyprojectStrategy{}, nil
	}
	switch cfg.Strategy {
	case types.DepsStrategyRequirements:
		return &requirementsStrategy{files: cfg.Files, extraArgs: cfg.ExtraArgs}, nil
	case types.DepsStrategyPyProject, "":
		return &pyprojectStrategy{extraArgs: cfg.ExtraArgs}, nil
	case types.DepsStrategyPoetry:
		return &poetryStrategy{extraArgs: cfg.ExtraArgs}, nil
	case types.DepsStrategyPipenv:
		return &pipenvStrategy{extraArgs: cfg.ExtraArgs}, nil
	case types.DepsStrategyNone:
		return &noneStrategy{}, nil
	default:
		return nil, fmt.Errorf("unknown deps strategy: %q", cfg.Strategy)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// uvEnv returns the subprocess environment for uv, anchored on the given
// venv path. We strip any inherited VIRTUAL_ENV so uv doesn't get confused
// by an outer shell that happens to be in a different venv.
func uvEnv(venvDir string) []string {
	base := os.Environ()
	clean := make([]string, 0, len(base)+1)
	for _, e := range base {
		if strings.HasPrefix(e, "VIRTUAL_ENV=") {
			continue
		}
		clean = append(clean, e)
	}
	return append(clean, "VIRTUAL_ENV="+venvDir)
}

// runUV invokes the managed uv binary with args inside installDir.
func runUV(installDir string, args []string, out io.Writer) error {
	uv, err := uvbin.Ensure()
	if err != nil {
		return fmt.Errorf("locate uv: %w", err)
	}
	cmd := exec.Command(uv, args...)
	cmd.Dir = installDir
	cmd.Env = uvEnv(filepath.Join(installDir, ".venv"))
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

// ── requirements ──────────────────────────────────────────────────────────────

type requirementsStrategy struct {
	files     []string
	extraArgs []string
}

func (r *requirementsStrategy) Name() string { return "requirements" }

func (r *requirementsStrategy) Install(installDir string, out io.Writer) error {
	// For each requirements file the user listed, invoke `uv pip install -r`.
	// We run them sequentially rather than batching — keeps error messages
	// per-file and matches what developers expect when they see the command
	// name in a log.
	for _, rel := range r.files {
		path := filepath.Join(installDir, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("requirements file %q not found in payload: %w", rel, err)
		}
		args := []string{"pip", "install", "-r", path}
		args = append(args, r.extraArgs...)
		fmt.Fprintf(out, "[molt] uv pip install -r %s\n", rel)
		if err := runUV(installDir, args, out); err != nil {
			return fmt.Errorf("uv pip install -r %s: %w", rel, err)
		}
	}
	return nil
}

// ── pyproject ─────────────────────────────────────────────────────────────────

type pyprojectStrategy struct {
	extraArgs []string
}

func (p *pyprojectStrategy) Name() string { return "pyproject" }

func (p *pyprojectStrategy) Install(installDir string, out io.Writer) error {
	lock := filepath.Join(installDir, "uv.lock")
	args := []string{"sync"}
	if _, err := os.Stat(lock); err == nil {
		// Lockfile present: use it. `--frozen` forbids implicit updates,
		// matching the hermetic-build promise molt makes.
		args = append(args, "--frozen")
	}
	args = append(args, p.extraArgs...)
	fmt.Fprintf(out, "[molt] uv %s\n", strings.Join(args, " "))
	return runUV(installDir, args, out)
}

// ── poetry ────────────────────────────────────────────────────────────────────

type poetryStrategy struct {
	extraArgs []string
}

func (p *poetryStrategy) Name() string { return "poetry" }

func (p *poetryStrategy) Install(installDir string, out io.Writer) error {
	// poetry has no native "install into arbitrary venv" command, so we
	// export to requirements format and pipe that into uv. This keeps the
	// SAME installer (uv) across strategies — one tool, one codepath for
	// resolution and caching.
	poetry, err := exec.LookPath("poetry")
	if err != nil {
		return fmt.Errorf(
			"poetry not found on PATH — deps.strategy: poetry requires poetry to be installed on the target",
		)
	}

	// Step 1: poetry export -f requirements.txt --output /tmp/req.txt
	tmp, err := os.CreateTemp("", "molt-poetry-req-*.txt")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	exportArgs := []string{"export", "-f", "requirements.txt", "--without-hashes", "--output", tmp.Name()}
	fmt.Fprintf(out, "[molt] poetry %s\n", strings.Join(exportArgs, " "))
	cmd := exec.Command(poetry, exportArgs...)
	cmd.Dir = installDir
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("poetry export: %w", err)
	}

	// Step 2: uv pip install -r /tmp/req.txt
	args := []string{"pip", "install", "-r", tmp.Name()}
	args = append(args, p.extraArgs...)
	fmt.Fprintf(out, "[molt] uv pip install -r <poetry-export>\n")
	return runUV(installDir, args, out)
}

// ── pipenv ────────────────────────────────────────────────────────────────────

type pipenvStrategy struct {
	extraArgs []string
}

func (p *pipenvStrategy) Name() string { return "pipenv" }

func (p *pipenvStrategy) Install(installDir string, out io.Writer) error {
	pipenv, err := exec.LookPath("pipenv")
	if err != nil {
		return fmt.Errorf(
			"pipenv not found on PATH — deps.strategy: pipenv requires pipenv to be installed on the target",
		)
	}
	tmp, err := os.CreateTemp("", "molt-pipenv-req-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	// `pipenv requirements` writes requirements-format to stdout. We redirect
	// it into our temp file.
	fmt.Fprintf(out, "[molt] pipenv requirements > (temp file)\n")
	cmd := exec.Command(pipenv, "requirements")
	cmd.Dir = installDir
	cmd.Stdout = tmp
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		tmp.Close()
		return fmt.Errorf("pipenv requirements: %w", err)
	}
	tmp.Close()

	args := []string{"pip", "install", "-r", tmp.Name()}
	args = append(args, p.extraArgs...)
	fmt.Fprintf(out, "[molt] uv pip install -r <pipenv-export>\n")
	return runUV(installDir, args, out)
}

// ── none ──────────────────────────────────────────────────────────────────────

type noneStrategy struct{}

func (noneStrategy) Name() string { return "none" }

func (noneStrategy) Install(installDir string, out io.Writer) error {
	// Explicitly a no-op. Used when the user vendors everything themselves
	// and doesn't want molt running any installer.
	fmt.Fprintln(out, "[molt] deps.strategy: none — skipping install step")
	return nil
}
