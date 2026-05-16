// Package moltenv manages named, project-independent Python environments.
//
// A "named env" is a reusable package set stored at ~/.molt/envs/<name>/.
// It behaves exactly like a molt project but has no source directory —
// it's just a set of packages (and optionally a Python version) that
// can be used from any directory via `molt run --env <name> script.py`
// or by tools installed with `molt tool install --env <name>`.
//
// Layout:
//
//	~/.molt/envs/<name>/
//	    env.toml          ← user-owned definition (name, python, packages, env vars)
//	    pyproject.toml    ← auto-generated from env.toml; syncplan.Sync reads this
//	    uv.lock           ← written by uv; committed alongside env.toml for reproducibility
//
// The compiled syspath lives in the normal per-project state dir, keyed by
// the hash of ~/.molt/envs/<name>/ — exactly like any other project:
//
//	~/.molt/projects/<name>-<hash>/syspath.json
//
// This means syspath.Load / BuildEnv / the entire existing pipeline
// work without any changes.
package moltenv

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"molt/internal/projstate"
	"molt/internal/syncplan"
	"molt/internal/syspath"
)

// Root returns ~/.molt/envs/.
func Root() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".molt", "envs"), nil
}

// Dir returns ~/.molt/envs/<name>/.
func Dir(name string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

// Exists reports whether a named env with this name exists on disk.
func Exists(name string) bool {
	d, err := Dir(name)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(d, "env.toml"))
	return err == nil
}

// EnvDef is the in-memory representation of env.toml.
type EnvDef struct {
	Name          string
	Python        string            // optional; empty → use global ~/.molt/.python-version
	Packages      []string          // PEP 508 dependency specifiers
	Env           map[string]string // extra env vars layered when using this env
	MinPackageAge string            // e.g. "1w", "3d", "24h"; empty → default (1w)
}

// Load reads the env.toml for the named env.
func Load(name string) (EnvDef, error) {
	d, err := Dir(name)
	if err != nil {
		return EnvDef{}, err
	}
	data, err := os.ReadFile(filepath.Join(d, "env.toml"))
	if err != nil {
		if os.IsNotExist(err) {
			return EnvDef{}, fmt.Errorf("env %q not found — run `molt envs create %s` first", name, name)
		}
		return EnvDef{}, err
	}
	return parseEnvToml(name, data)
}

// LoadSpec loads the compiled syspath spec for the named env.
// Returns an error if the env has never been synced.
func LoadSpec(name string) (*syspath.Spec, error) {
	d, err := Dir(name)
	if err != nil {
		return nil, err
	}
	spec, err := syspath.Load(d)
	if err != nil {
		return nil, fmt.Errorf("env %q not synced — run `molt envs sync %s` first", name, name)
	}
	return spec, nil
}

// Create initialises a new named env, writes env.toml + pyproject.toml,
// and runs syncplan.Sync to install packages and write syspath.json.
// When allowFresh is false the resolved packages are checked against PyPI
// and an error is returned if any were uploaded less than MinPackageAge ago
// (default: 1 week).
func Create(name, python string, packages []string, allowFresh bool) error {
	if name == "" {
		return fmt.Errorf("env name required")
	}
	if !isValidName(name) {
		return fmt.Errorf("invalid env name %q: use letters, digits, hyphens, underscores only", name)
	}
	d, err := Dir(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(d, "env.toml")); err == nil {
		return fmt.Errorf("env %q already exists; use `molt envs add` to add packages", name)
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	def := EnvDef{Name: name, Python: python, Packages: packages, Env: map[string]string{}}
	if err := writeEnvToml(d, def); err != nil {
		return err
	}
	if err := writePyprojectToml(d, def); err != nil {
		return err
	}
	if python != "" {
		if err := os.WriteFile(filepath.Join(d, ".python-version"), []byte(python+"\n"), 0o644); err != nil {
			return err
		}
	}
	if err := syncplan.Sync(d, syncplan.Options{Verbose: true}); err != nil {
		return err
	}
	return checkAgesAfterSync(d, def, allowFresh)
}

// AddPackages adds packages to the named env and re-syncs.
// allowFresh skips the minimum-age safety check when true.
func AddPackages(name string, pkgs []string, allowFresh bool) error {
	def, err := Load(name)
	if err != nil {
		return err
	}
	// Merge: skip duplicates by base name.
	existing := map[string]bool{}
	for _, p := range def.Packages {
		existing[baseName(p)] = true
	}
	for _, p := range pkgs {
		if !existing[baseName(p)] {
			def.Packages = append(def.Packages, p)
		}
	}
	return saveAndSync(name, def, allowFresh)
}

// RemovePackages removes packages from the named env by name and re-syncs.
func RemovePackages(name string, pkgs []string) error {
	def, err := Load(name)
	if err != nil {
		return err
	}
	remove := map[string]bool{}
	for _, p := range pkgs {
		remove[strings.ToLower(baseName(p))] = true
	}
	kept := def.Packages[:0]
	for _, p := range def.Packages {
		if !remove[strings.ToLower(baseName(p))] {
			kept = append(kept, p)
		}
	}
	def.Packages = kept
	// Removing packages can't introduce fresh packages — skip age check.
	return saveAndSync(name, def, true)
}

// Sync re-runs syncplan.Sync on the env dir. Call after manual env.toml edits.
// allowFresh skips the minimum-age safety check when true.
func Sync(name string, allowFresh bool) error {
	def, err := Load(name)
	if err != nil {
		return err
	}
	d, err := Dir(name)
	if err != nil {
		return err
	}
	// Re-generate pyproject.toml in case env.toml was edited by hand.
	if err := writePyprojectToml(d, def); err != nil {
		return err
	}
	if err := syncplan.Sync(d, syncplan.Options{Verbose: true}); err != nil {
		return err
	}
	return checkAgesAfterSync(d, def, allowFresh)
}

// Delete removes the env dir and its compiled state.
func Delete(name string) error {
	d, err := Dir(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(d); os.IsNotExist(err) {
		return fmt.Errorf("env %q not found", name)
	}
	// Purge the projstate (syspath.json, bin/, etc.) for this env.
	stateDir := projstate.Dir(d)
	_ = os.RemoveAll(stateDir)
	return os.RemoveAll(d)
}

// List returns all named envs sorted by name.
func List() ([]EnvDef, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []EnvDef
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		def, err := Load(e.Name())
		if err != nil {
			continue // skip malformed entries
		}
		out = append(out, def)
	}
	return out, nil
}

// DiskUsage returns the approximate on-disk size of the env's state in bytes.
func DiskUsage(name string) (int64, error) {
	d, err := Dir(name)
	if err != nil {
		return 0, err
	}
	stateDir := projstate.Dir(d)
	var total int64
	for _, dir := range []string{d, stateDir} {
		_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				total += info.Size()
			}
			return nil
		})
	}
	return total, nil
}

// Metadata for `molt envs list` — wraps DiskUsage + Spec for display.
type EnvInfo struct {
	Def    EnvDef
	Synced bool
	Python string // resolved python version from syspath
}

// Info returns display-friendly metadata for a named env.
func Info(name string) (EnvInfo, error) {
	def, err := Load(name)
	if err != nil {
		return EnvInfo{}, err
	}
	info := EnvInfo{Def: def}
	if spec, err := LoadSpec(name); err == nil {
		info.Synced = true
		info.Python = spec.Version
	}
	return info, nil
}

// --- internal helpers ---

func saveAndSync(name string, def EnvDef, allowFresh bool) error {
	d, err := Dir(name)
	if err != nil {
		return err
	}
	if err := writeEnvToml(d, def); err != nil {
		return err
	}
	if err := writePyprojectToml(d, def); err != nil {
		return err
	}
	if err := syncplan.Sync(d, syncplan.Options{Verbose: true}); err != nil {
		return err
	}
	return checkAgesAfterSync(d, def, allowFresh)
}

// checkAgesAfterSync loads the syspath.json written by Sync and runs the
// package-age check unless allowFresh is true.
func checkAgesAfterSync(envDir string, def EnvDef, allowFresh bool) error {
	if allowFresh {
		return nil
	}
	ageStr := def.MinPackageAge
	if ageStr == "" {
		ageStr = "1w" // default: one week
	}
	minAge, err := parseMinAge(ageStr)
	if err != nil {
		return fmt.Errorf("invalid min_package_age in env.toml: %w", err)
	}
	if minAge <= 0 {
		return nil
	}
	spec, err := syspath.Load(envDir)
	if err != nil {
		// Sync didn't produce a syspath — nothing to check.
		return nil
	}
	return CheckPackageAges(spec, minAge)
}

func writeEnvToml(dir string, def EnvDef) error {
	var b strings.Builder
	b.WriteString("# molt named environment — edit packages here, then run `molt envs sync <name>`\n")
	b.WriteString(fmt.Sprintf("name = %q\n", def.Name))
	if def.Python != "" {
		b.WriteString(fmt.Sprintf("python = %q\n", def.Python))
	}
	if def.MinPackageAge != "" {
		b.WriteString(fmt.Sprintf("min_package_age = %q\n", def.MinPackageAge))
	}
	b.WriteString("packages = [")
	for i, p := range def.Packages {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(fmt.Sprintf("%q", p))
	}
	b.WriteString("]\n")
	if len(def.Env) > 0 {
		b.WriteString("\n[env]\n")
		// Sort keys for determinism.
		keys := make([]string, 0, len(def.Env))
		for k := range def.Env {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			b.WriteString(fmt.Sprintf("%s = %q\n", k, def.Env[k]))
		}
	}
	return os.WriteFile(filepath.Join(dir, "env.toml"), []byte(b.String()), 0o644)
}

func writePyprojectToml(dir string, def EnvDef) error {
	var b strings.Builder
	b.WriteString("# Auto-generated by molt from env.toml — do not edit directly.\n")
	b.WriteString("[project]\n")
	b.WriteString(fmt.Sprintf("name = %q\n", def.Name))
	b.WriteString("version = \"0.1.0\"\n")
	if def.Python != "" {
		b.WriteString(fmt.Sprintf("requires-python = \">=%s\"\n", def.Python))
	}
	b.WriteString("dependencies = [")
	for i, p := range def.Packages {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(fmt.Sprintf("%q", p))
	}
	b.WriteString("]\n")
	return os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(b.String()), 0o644)
}

// parseEnvToml is a minimal TOML parser for env.toml.
// env.toml is simple enough that a full TOML library isn't worth pulling in.
func parseEnvToml(name string, data []byte) (EnvDef, error) {
	def := EnvDef{Name: name, Env: map[string]string{}}
	inEnvSection := false

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if line == "[env]" {
			inEnvSection = true
			continue
		}
		if strings.HasPrefix(line, "[") {
			inEnvSection = false
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		if inEnvSection {
			def.Env[key] = unquote(val)
			continue
		}
		switch key {
		case "name":
			def.Name = unquote(val)
		case "python":
			def.Python = unquote(val)
		case "min_package_age":
			def.MinPackageAge = unquote(val)
		case "packages":
			def.Packages = parseTOMLStringArray(val)
		}
	}
	return def, nil
}

func parseTOMLStringArray(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") {
		return nil
	}
	// Strip the outer brackets, then split on commas, unquoting each element.
	inner := s[1:]
	if idx := strings.LastIndexByte(inner, ']'); idx >= 0 {
		inner = inner[:idx]
	}
	parts := strings.Split(inner, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = unquote(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// unquote strips surrounding " or ' from a TOML string value.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

// baseName extracts the distribution name from a PEP 508 specifier,
// e.g. "pandas>=2.0" → "pandas", "numpy" → "numpy".
func baseName(spec string) string {
	for _, sep := range []string{">=", "<=", "!=", "==", "~=", ">", "<", "[", ";"} {
		if i := strings.Index(spec, sep); i >= 0 {
			spec = spec[:i]
		}
	}
	return strings.TrimSpace(spec)
}

func isValidName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j] < ss[j-1]; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}

// EnvInfoJSON is the JSON representation used by `molt envs list --json`.
type EnvInfoJSON struct {
	Name     string   `json:"name"`
	Python   string   `json:"python,omitempty"`
	Packages []string `json:"packages"`
	Synced   bool     `json:"synced"`
}

// MarshalJSON marshals an EnvInfo to JSON.
func (i EnvInfo) MarshalJSON() ([]byte, error) {
	return json.Marshal(EnvInfoJSON{
		Name:     i.Def.Name,
		Python:   i.Python,
		Packages: i.Def.Packages,
		Synced:   i.Synced,
	})
}

// --- Package age safety ---

// parseMinAge parses strings like "1w", "7d", "24h" into a Duration.
// Supported suffixes: h (hours), d (days), w (weeks). No suffix → days.
func parseMinAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	unit := byte('d')
	num := s
	last := s[len(s)-1]
	if last == 'h' || last == 'd' || last == 'w' {
		unit = last
		num = s[:len(s)-1]
	}
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid min_package_age %q: expected e.g. \"1w\", \"7d\", \"24h\"", s)
	}
	switch unit {
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("unknown unit in %q", s)
}

// ageCachePath returns ~/.molt/pkg-age-cache.json.
func ageCachePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".molt", "pkg-age-cache.json")
}

// loadAgeCache reads the upload-time cache from disk.
// Returns an empty map on any error — the cache is advisory only.
func loadAgeCache() map[string]string {
	data, err := os.ReadFile(ageCachePath())
	if err != nil {
		return map[string]string{}
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]string{}
	}
	return m
}

// saveAgeCache writes the upload-time cache to disk.
func saveAgeCache(m map[string]string) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(ageCachePath(), data, 0o644)
}

// fetchUploadTime queries the PyPI JSON API for the earliest upload time
// of the given package version. Returns a zero Time for packages not on PyPI
// (private / VCS deps), so callers can skip them.
func fetchUploadTime(name, version string) (time.Time, error) {
	url := fmt.Sprintf("https://pypi.org/pypi/%s/%s/json", name, version)
	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		return time.Time{}, fmt.Errorf("PyPI lookup for %s==%s: %w", name, version, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		// Not on PyPI — private package or VCS dep; silently skip.
		return time.Time{}, nil
	}
	if resp.StatusCode != 200 {
		return time.Time{}, fmt.Errorf("PyPI returned HTTP %d for %s==%s", resp.StatusCode, name, version)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return time.Time{}, err
	}

	var result struct {
		URLs []struct {
			UploadTime string `json:"upload_time_iso_8601"`
		} `json:"urls"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return time.Time{}, fmt.Errorf("parse PyPI response for %s==%s: %w", name, version, err)
	}

	var earliest time.Time
	for _, u := range result.URLs {
		// PyPI uses "2006-01-02T15:04:05.999999Z" (ISO 8601 with fractional seconds and Z).
		t, err := time.Parse("2006-01-02T15:04:05.999999Z", u.UploadTime)
		if err != nil {
			t, err = time.Parse(time.RFC3339, u.UploadTime)
			if err != nil {
				continue
			}
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest, nil
}

// pkgFromStorePath extracts name and version from a store path.
// Store layout: <storeRoot>/<name>/<version>/<tags>/
// Returns ok=false for paths outside the store or with unexpected structure.
func pkgFromStorePath(p, storeRoot string) (name, version string, ok bool) {
	rel, err := filepath.Rel(storeRoot, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", "", false
	}
	// rel = "name/version/tags" or "name/version/tags/subdir"
	parts := strings.SplitN(rel, string(filepath.Separator), 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// pkgStoreRoot returns ~/.molt/pkg.
func pkgStoreRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".molt", "pkg")
}

// CheckPackageAges verifies that every package in spec is at least minAge old
// on PyPI. Returns a descriptive error listing packages that are too fresh.
// Packages not found on PyPI (private / VCS deps) are silently skipped.
func CheckPackageAges(spec *syspath.Spec, minAge time.Duration) error {
	if minAge <= 0 {
		return nil
	}
	root := pkgStoreRoot()
	cache := loadAgeCache()
	cacheDirty := false
	now := time.Now().UTC()

	type result struct{ name, version string; age time.Duration }
	var tooFresh []result

	seen := map[string]bool{}
	for _, p := range spec.Syspath {
		pkgName, pkgVersion, ok := pkgFromStorePath(p, root)
		if !ok {
			continue
		}
		key := pkgName + "/" + pkgVersion
		if seen[key] {
			continue
		}
		seen[key] = true

		// Check cache first.
		var uploadTime time.Time
		if cached, ok := cache[key]; ok {
			uploadTime, _ = time.Parse(time.RFC3339, cached)
		}
		if uploadTime.IsZero() {
			var err error
			uploadTime, err = fetchUploadTime(pkgName, pkgVersion)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: age check skipped for %s==%s: %v\n",
					pkgName, pkgVersion, err)
				continue
			}
			if !uploadTime.IsZero() {
				cache[key] = uploadTime.UTC().Format(time.RFC3339)
				cacheDirty = true
			}
		}

		if uploadTime.IsZero() {
			continue // not on PyPI — skip
		}
		age := now.Sub(uploadTime)
		if age < minAge {
			tooFresh = append(tooFresh, result{pkgName, pkgVersion, age})
		}
	}

	if cacheDirty {
		saveAgeCache(cache)
	}
	if len(tooFresh) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("package age check failed (minimum: %s):\n", fmtAge(minAge)))
	for _, f := range tooFresh {
		b.WriteString(fmt.Sprintf("  %s==%s  uploaded %s ago (too fresh)\n",
			f.name, f.version, fmtAge(f.age)))
	}
	b.WriteString("Re-run with --allow-fresh to bypass this check.")
	return fmt.Errorf("%s", b.String())
}

// fmtAge renders a duration in human-readable form matching the parseMinAge syntax.
func fmtAge(d time.Duration) string {
	if d >= 7*24*time.Hour {
		return fmt.Sprintf("%dw", int(d/(7*24*time.Hour)))
	}
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	if d >= time.Hour {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dm", int(d/time.Minute))
}
