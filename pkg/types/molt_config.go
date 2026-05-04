package types

import "time"

// ── molt.yaml schema ──────────────────────────────────────────────────────────
//
// MoltConfig is what a molt.yaml file deserializes to. It describes the
// deployment artefact: what ships in the binary, how to run it, runtime env,
// assets, integrity policy. It is intentionally orthogonal to pyproject.toml,
// which continues to describe the Python package (name/version/deps).
//
// Everything below the Project block is optional. When molt.yaml is absent
// entirely, molt falls back to legacy pyproject.toml-driven behaviour.

type MoltConfig struct {
	Version   int                    `yaml:"version"`
	Project   MoltProject            `yaml:"project"`
	Deps      *MoltDeps              `yaml:"deps,omitempty"`
	Include   []string               `yaml:"include,omitempty"`
	Exclude   []string               `yaml:"exclude,omitempty"`
	Assets    *MoltAssets            `yaml:"assets,omitempty"`
	Commands  map[string]MoltCommand `yaml:"commands,omitempty"`
	Env       map[string]string      `yaml:"env,omitempty"`
	Hooks     MoltHooks              `yaml:"hooks,omitempty"`
	Integrity *MoltIntegrity         `yaml:"integrity,omitempty"`

	// DefaultCommand is taken from either `commands.default:` shorthand or
	// resolved from convention. Populated by the loader, not parsed directly.
	DefaultCommand string `yaml:"-"`

	// SourcePath is the absolute path to the molt.yaml this was loaded from.
	// Empty when the config was synthesized (e.g. from pyproject.toml fallback).
	SourcePath string `yaml:"-"`
}

// MoltProject identifies the deployable app. Name and Version are required
// whenever molt.yaml exists.
type MoltProject struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Python      string `yaml:"python,omitempty"`
	Description string `yaml:"description,omitempty"`
}

// MoltDeps tells molt how to install Python dependencies. The strategy is
// explicit — molt never guesses between requirements.txt and pyproject.toml.
type MoltDeps struct {
	// Strategy: "requirements" | "pyproject" | "poetry" | "pipenv" | "none"
	Strategy  MoltDepsStrategy `yaml:"strategy"`
	Files     []string         `yaml:"files,omitempty"`
	ExtraArgs []string         `yaml:"extra_args,omitempty"`
}

// MoltDepsStrategy is a typed enum of the accepted deps.strategy values.
// It's a string alias so YAML unmarshalling, string comparison, and
// switch statements all behave naturally.
type MoltDepsStrategy string

// Dependency-install strategies. Declared as constants so the rest of the
// codebase never type-checks against string literals.
const (
	DepsStrategyRequirements MoltDepsStrategy = "requirements"
	DepsStrategyPyProject    MoltDepsStrategy = "pyproject"
	DepsStrategyPoetry       MoltDepsStrategy = "poetry"
	DepsStrategyPipenv       MoltDepsStrategy = "pipenv"
	DepsStrategyNone         MoltDepsStrategy = "none"
)

// MoltConfigSchemaVersion is the currently-supported top-level `version:`
// value in molt.yaml. Kept here (not in moltcfg) so adopt and other
// scaffolders can reach it without importing moltcfg.
const MoltConfigSchemaVersion = 1

// MoltAssets lists non-code files that must ship with the binary. Unlike
// Include globs which are permissive, assets are explicit and can be marked
// Required, meaning the build fails if they're missing at build time.
type MoltAssets struct {
	Files []MoltAssetFile `yaml:"files,omitempty"`

	// MaxFileSizeMB warns (does not fail) when any single asset exceeds
	// this size. Defaults to 100MB in applyDefaults. 0 disables the check.
	MaxFileSizeMB int `yaml:"max_file_size_mb,omitempty"`

	// MaxTotalSizeMB fails the build if the combined packaged size exceeds
	// this. 0 disables the check. Intended to catch a data/ directory
	// accidentally slurping up gigabytes.
	MaxTotalSizeMB int `yaml:"max_total_size_mb,omitempty"`
}

// MoltAssetFile is a single declared asset. Path may be a glob.
type MoltAssetFile struct {
	Path        string `yaml:"path"`
	Required    bool   `yaml:"required,omitempty"`
	Description string `yaml:"description,omitempty"`
}

// MoltCommand is a named runnable. Exec and Script are mutually exclusive:
// Exec is argv-style (no shell, no injection surface); Script is a shell
// one-liner, used when you genuinely need pipes or redirection.
type MoltCommand struct {
	Exec        []string          `yaml:"exec,omitempty"`
	Script      string            `yaml:"script,omitempty"`
	Description string            `yaml:"description,omitempty"`
	Env         map[string]string `yaml:"env,omitempty"`
	Dir         string            `yaml:"dir,omitempty"`
}

// MoltHooks define shell commands run at lifecycle points. They execute in
// the installed hermetic environment, not the build host.
type MoltHooks struct {
	PreInstall  []string `yaml:"pre_install,omitempty"`
	PostInstall []string `yaml:"post_install,omitempty"`
}

// MoltIntegrity controls the transparency manifest and launch-time checks.
// Enabled is a pointer so callers can distinguish "default" (nil → on)
// from "explicitly disabled" (false).
type MoltIntegrity struct {
	Enabled         *bool  `yaml:"enabled,omitempty"`
	Output          string `yaml:"output,omitempty"`          // defaults to "{name}-v{version}.manifest.json"
	Algorithm       string `yaml:"algorithm,omitempty"`       // "sha256" (default) | "sha512"
	VerifyOnLaunch  bool   `yaml:"verify_on_launch,omitempty"`
	VerifyOnInstall bool   `yaml:"verify_on_install,omitempty"`
}

// IntegrityEnabled is the nil-safe accessor.
func (m *MoltIntegrity) IntegrityEnabled() bool {
	if m == nil || m.Enabled == nil {
		return true
	}
	return *m.Enabled
}

// ── Integrity manifest ────────────────────────────────────────────────────────
//
// IntegrityManifest serves two purposes:
//   1. Transparency — a human/machine-readable list of every packaged file
//      with its size and hash. Audit-friendly, diff-friendly, SBOM-friendly.
//   2. Authentication — its RootHash is embedded in the binary trailer. The
//      launcher can recompute the hash over the extracted payload and
//      compare, refusing to run on mismatch.
//
// Both uses come from the same data. The external .manifest.json file is
// written alongside the binary; the root hash is copied into the trailer.

type IntegrityManifest struct {
	Schema    string          `json:"schema"`
	App       ManifestApp     `json:"app"`
	BuiltAt   string          `json:"built_at"`
	BuildHost ManifestHost    `json:"build_host"`
	Algorithm string          `json:"algorithm"`
	RootHash  string          `json:"root_hash"`
	Payload   ManifestPayload `json:"payload"`
	Python    *ManifestPython `json:"python,omitempty"`
	Deps      *ManifestDeps   `json:"deps,omitempty"`
	Assets    []ManifestAsset `json:"assets,omitempty"`
}

type ManifestApp struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ManifestHost struct {
	OS    string `json:"os"`
	Arch  string `json:"arch"`
	Glibc string `json:"glibc,omitempty"`
}

type ManifestPayload struct {
	TotalFiles int64          `json:"total_files"`
	TotalBytes int64          `json:"total_bytes"`
	Files      []PackagedFile `json:"files"`
}

type ManifestPython struct {
	Version string `json:"version,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

type ManifestDeps struct {
	Strategy string           `json:"strategy"`
	Files    []string         `json:"files,omitempty"`
	Packages []ManifestDepPkg `json:"packages,omitempty"`
}

type ManifestDepPkg struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	SHA256  string `json:"sha256,omitempty"`
}

type ManifestAsset struct {
	Path        string `json:"path"`
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`
	Found       bool   `json:"found"`
}

// IntegrityApp and IntegrityDeps are the simpler shapes the builder passes
// to integrity.BuildManifest (the 3-arg form). They exist as a stable API
// surface independent of MoltConfig's evolution.
type IntegrityApp struct {
	Name    string
	Version string
}

type IntegrityDeps struct {
	Strategy string
	Files    []string
}

// PackagedFile describes one file that landed in the payload. The Source
// field lets us answer "why was this included?" at inspect time without
// re-running the build.
type PackagedFile struct {
	Path        string             `json:"path"`
	Size        int64              `json:"size"`
	SHA256      string             `json:"sha256"`
	Source      PackagedFileSource `json:"source"`
	Description string             `json:"description,omitempty"`
}

// PackagedFileSource is a typed enum of the reasons a file ended up in the
// payload. String-based so JSON output stays human-friendly.
type PackagedFileSource string

// Source values for PackagedFile. Using typed constants keeps call sites
// honest and searchable.
//
//   "include" — matched a user include glob
//   "default" — packaged under legacy denylist scan (no include globs set)
//   "asset"   — declared in molt.yaml assets
//   "auto"    — molt-generated metadata (manifest.json, receipt.json)
//   "hook"    — produced by a pre_install hook at build time
const (
	SourceInclude PackagedFileSource = "include"
	SourceDefault PackagedFileSource = "default"
	SourceAsset   PackagedFileSource = "asset"
	SourceAuto    PackagedFileSource = "auto"
	SourceHook    PackagedFileSource = "hook"
)

// ── Binary trailer ────────────────────────────────────────────────────────────
//
// TrailerV1 is 48 bytes at the end of the binary:
//   [payload_offset: 8 bytes LE int64]
//   [root_hash:      32 bytes raw sha256 (not hex)]
//   [magic:          8 bytes "MOLT0001"]
//
// If the magic is missing the launcher falls back to legacy 8-byte trailer
// parsing (offset only, no integrity check). This keeps old binaries working.

const (
	TrailerMagic      = "MOLT0001"
	TrailerV1Size     = 48
	LegacyTrailerSize = 8
	RootHashBytes     = 32 // sha256
)

// BuildInfo is the summary returned after a successful build.
type BuildInfo struct {
	BinaryPath    string
	ManifestPath  string
	RootHash      string
	PayloadBytes  int64
	FilesPackaged int
	BuildDuration time.Duration
}
