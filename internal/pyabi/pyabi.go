// Package pyabi inspects a Python interpreter to discover its compatibility
// tags (PEP 425). The information is used to pick the right wheel from a
// uv.lock entry that lists multiple wheels per package.
package pyabi

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type Info struct {
	Executable string   `json:"executable"`
	Version    string   `json:"version"`     // e.g. "3.12.3"
	PyTag      string   `json:"py_tag"`      // e.g. "cp312"
	AbiTag     string   `json:"abi_tag"`     // e.g. "cp312"
	Platforms  []string `json:"platforms"`   // ordered most-specific first; "any" appended
}

func (i *Info) GetPyTag() string         { return i.PyTag }
func (i *Info) GetAbiTag() string        { return i.AbiTag }
func (i *Info) GetPlatforms() []string   { return i.Platforms }

// detectScript runs inside the target interpreter. It tries packaging.tags
// first (the canonical source); if packaging is unavailable it computes a
// best-effort approximation from sysconfig and sys.implementation.
const detectScript = `
import json, sys, sysconfig
out = {"executable": sys.executable, "version": "%d.%d.%d" % sys.version_info[:3]}
try:
    from packaging import tags
    py_tag = "cp%d%d" % sys.version_info[:2]
    # Best ABI: matching cpython tag if available; else abi3; else 'none'.
    abi_tag = None
    plats = []
    for t in tags.sys_tags():
        if abi_tag is None:
            abi_tag = t.abi
        if py_tag is None:
            py_tag = t.interpreter
        if t.platform not in plats:
            plats.append(t.platform)
    out["py_tag"] = py_tag
    out["abi_tag"] = abi_tag or "none"
    out["platforms"] = plats
except Exception:
    impl = sys.implementation.name
    if impl == "cpython":
        out["py_tag"] = "cp%d%d" % sys.version_info[:2]
        out["abi_tag"] = "cp%d%d" % sys.version_info[:2]
    else:
        out["py_tag"] = impl[:2] + "%d%d" % sys.version_info[:2]
        out["abi_tag"] = "none"
    plat = sysconfig.get_platform().replace("-", "_").replace(".", "_")
    out["platforms"] = [plat]
print(json.dumps(out))
`

// Detect runs the Python interpreter and parses its self-report.
func Detect(pythonExe string) (*Info, error) {
	cmd := exec.Command(pythonExe, "-c", detectScript)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("pyabi.Detect %s: %w", pythonExe, err)
	}
	info := &Info{}
	if err := json.Unmarshal(out, info); err != nil {
		return nil, fmt.Errorf("pyabi.Detect parse: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	// Always allow the universal "any" platform as a fallback.
	hasAny := false
	for _, p := range info.Platforms {
		if p == "any" {
			hasAny = true
			break
		}
	}
	if !hasAny {
		info.Platforms = append(info.Platforms, "any")
	}
	return info, nil
}
