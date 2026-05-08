// Package syspath reads/writes the per-project .molt/syspath.json that
// records the active interpreter and the ordered list of global store
// directories that make up the project's Python environment.
package syspath

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"molt/internal/projstate"
)

// FileName / SiteCustomize are file names inside the per-project state
// directory. They're exported for callers (e.g. syncplan) that compose
// paths via projstate.Dir(...) + these names.
const FileName = "syspath.json"
const SiteCustomize = "sitecustomize.py"
const BinDirName = "bin"

// DirName is retained for backward source-compat but is no longer used:
// state lives at projstate.Dir(projectDir), not at projectDir/.molt.
//
// Deprecated: use projstate.Dir / projstate.Bin / etc.
const DirName = ".molt"

type Spec struct {
	Python     string   `json:"python"`      // absolute path to interpreter
	Version    string   `json:"version"`     // python version string
	PyTag      string   `json:"py_tag"`
	AbiTag     string   `json:"abi_tag"`
	Platform   string   `json:"platform"`    // most-specific tag
	LockHash   string   `json:"lock_hash"`   // sha256 of uv.lock at sync time
	ProjectDir string   `json:"project_dir"` // absolute project root
	Syspath    []string `json:"syspath"`     // store dirs + project src; order matters
}

// projectMoltDir returns the per-project state directory. State lives
// centrally under ~/.molt/projects/<basename>-<hash16>/ so the user's
// project tree stays clean — see internal/projstate.
func projectMoltDir(projectDir string) string { return projstate.Dir(projectDir) }

func Path(projectDir string) string {
	return filepath.Join(projectMoltDir(projectDir), FileName)
}

func Save(s *Spec) error {
	dir := projectMoltDir(s.ProjectDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, FileName), data, 0o644)
}

func Load(projectDir string) (*Spec, error) {
	data, err := os.ReadFile(Path(projectDir))
	if err != nil {
		return nil, err
	}
	s := &Spec{}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	return s, nil
}

// BuildEnv returns parent with PYTHONPATH set, .molt/bin prepended to PATH,
// and the venv-related variables stripped so they can't leak in. The .molt
// directory itself is prepended to PYTHONPATH so sitecustomize.py is imported
// before user code; sitecustomize processes .pth files in each store dir.
func (s *Spec) BuildEnv(parent []string) []string {
	moltDir := projectMoltDir(s.ProjectDir)
	pyPath := append([]string{moltDir}, s.Syspath...)
	binDir := filepath.Join(moltDir, BinDirName)

	out := make([]string, 0, len(parent)+2)
	for _, kv := range parent {
		k := envKey(kv)
		switch k {
		case "PYTHONPATH", "VIRTUAL_ENV", "PYTHONHOME", "PATH":
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "PYTHONPATH="+strings.Join(pyPath, pathListSep()))
	pathVal := binDir
	for _, kv := range parent {
		if envKey(kv) == "PATH" {
			pathVal = binDir + string(os.PathListSeparator) + kv[len("PATH="):]
			break
		}
	}
	out = append(out, "PATH="+pathVal)
	return out
}

// PythonCommand builds an *exec.Cmd that invokes the project's Python
// interpreter directly (s.Python) with args, the project's PYTHONPATH and
// cleaned PATH, stdio inherited, and CWD set to s.ProjectDir.
//
// Use this for any "run python ..." path: structured tasks (module/script),
// `molt run python ...`, and the new `molt python run` subcommand. It does
// NOT need .molt/bin/python to exist — molt resolves directly to the
// uv-managed interpreter, so the user is never required to have any
// system `python` on PATH.
func (s *Spec) PythonCommand(args ...string) *exec.Cmd {
	cmd := exec.Command(s.Python, args...)
	cmd.Env = s.BuildEnv(os.Environ())
	cmd.Dir = s.ProjectDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// StateDir returns the per-project state directory (e.g. ~/.molt/projects/myapp-abc123/).
func (s *Spec) StateDir() string { return projectMoltDir(s.ProjectDir) }

// ResolveCommand searches .molt/bin first for a project-local shim, falling
// back to PATH. Returns absolute path or "" if not found.
func (s *Spec) ResolveCommand(name string) string {
	candidates := []string{filepath.Join(projectMoltDir(s.ProjectDir), BinDirName, name)}
	if runtime.GOOS == "windows" {
		candidates = append(candidates, filepath.Join(projectMoltDir(s.ProjectDir), BinDirName, name+".cmd"))
		candidates = append(candidates, filepath.Join(projectMoltDir(s.ProjectDir), BinDirName, name+".exe"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func envKey(kv string) string {
	if i := strings.IndexByte(kv, '='); i >= 0 {
		return kv[:i]
	}
	return kv
}

func pathListSep() string { return string(os.PathListSeparator) }
