package uv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrNotFound = fmt.Errorf("uv not found")

type InitOptions struct {
	Python   string
	Lib      bool
	NoReadme bool
	NoPin    bool
}

func Find() (string, error) {
	if p, err := exec.LookPath("uv"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".cargo", "bin", "uv"),
		"/usr/local/bin/uv",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", ErrNotFound
}

func Available() bool {
	_, err := Find()
	return err == nil
}

func Version() (string, error) {
	uv, err := Find()
	if err != nil {
		return "", err
	}
	out, err := exec.Command(uv, "version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func run(dir string, args ...string) error {
	uv, err := Find()
	if err != nil {
		return err
	}
	cmd := exec.Command(uv, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func Init(dir, name string, opts InitOptions) error {
	args := []string{"init"}
	if opts.Python != "" {
		args = append(args, "--python", opts.Python)
	}
	if opts.Lib {
		args = append(args, "--lib")
	}
	if name != "" {
		args = append(args, name)
	}
	return run(dir, args...)
}

func Add(dir string, packages []string, dev bool) error {
	if len(packages) == 0 {
		return fmt.Errorf("add: at least one package required")
	}
	args := []string{"add"}
	if dev {
		args = append(args, "--dev")
	}
	return run(dir, append(args, packages...)...)
}

func Remove(dir string, packages []string, dev bool) error {
	if len(packages) == 0 {
		return fmt.Errorf("remove: at least one package required")
	}
	args := []string{"remove"}
	if dev {
		args = append(args, "--dev")
	}
	return run(dir, append(args, packages...)...)
}

func Lock(dir string) error            { return run(dir, "lock") }
func Sync(dir string, frozen bool) error {
	args := []string{"sync"}
	if frozen {
		args = append(args, "--frozen")
	}
	return run(dir, args...)
}
func Tree(dir string) error            { return run(dir, "tree") }

func LockIfStale(dir string) (bool, error) {
	return true, Lock(dir)
}

func Raw(dir string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no args")
	}
	return run(dir, args...)
}
