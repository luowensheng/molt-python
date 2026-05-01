package types

// This file preserves the legacy type set that the rest of the codebase
// (builder, installer, launcher-manifest) was built around. The newer
// molt.yaml / integrity types live in molt_config.go alongside. Nothing
// here references the new types; the builder bridges between them.

// ── Build profiles ────────────────────────────────────────────────────────────

type BuildProfile string

const (
	ProfileMinimal  BuildProfile = "minimal"
	ProfileStandard BuildProfile = "standard"
	ProfileExtended BuildProfile = "extended"
	ProfileFull     BuildProfile = "full"
)

type InstallMode string

const (
	ModeMinimal    InstallMode = "minimal"
	ModeStandalone InstallMode = "standalone"
	ModeExact      InstallMode = "exact"
)

type CrossBuildMode string

const (
	CrossBuildDeny       CrossBuildMode = "deny"
	CrossBuildBestEffort CrossBuildMode = "best-effort"
)

// ── Legacy Manifest ───────────────────────────────────────────────────────────
//
// Manifest is the pre-existing .molt/manifest.json shape. It's written by
// the builder into the payload and read by the launcher at install/run
// time. Distinct from IntegrityManifest (which is for authentication/audit).
type Manifest struct {
	AppName    string       `json:"app_name"`
	Version    string       `json:"version"`
	MainModule string       `json:"main_module"`
	Python     PythonSpec   `json:"python"`
	SystemDeps []SystemDep  `json:"system_deps"`
	PyPackages []PyPackage  `json:"py_packages"`
	Profile    BuildProfile `json:"profile"`
	TargetOS   string       `json:"target_os"`
	TargetArch string       `json:"target_arch"`
	BuildTime  string       `json:"build_time"`

	// MoltConfigSnapshot embeds the resolved molt.yaml so the launcher can
	// execute commands, apply env, and run hooks without needing the
	// original file. Optional — nil for legacy pyproject-only builds.
	MoltConfigSnapshot *MoltConfig `json:"molt_config,omitempty"`
}

type PythonSpec struct {
	Version  string `json:"version"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Embedded bool   `json:"embedded"`
}

type SystemDep struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	URL         string `json:"url,omitempty"`
	PackageName string `json:"package_name,omitempty"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	Embedded    bool   `json:"embedded"`
}

type PyPackage struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Embedded bool   `json:"embedded"`
}

// ── Config structs passed to builder / installer / etc. ──────────────────────

type BuildConfig struct {
	Profile        BuildProfile
	Name           string
	Version        string
	ProjectPath    string
	OutputPath     string
	TargetOS       string
	TargetArch     string
	Offline        bool
	SignKeyPath    string
	EmbedFiles     []string
	MaxSizeMB      int
	CrossBuildMode CrossBuildMode

	EmbedStrict      bool
	EmbedIgnoreFile  string
	EmbedExtraIgnore []string
	EmbedIncludeOnly []string
}

type InstallConfig struct {
	Mode       InstallMode
	TargetDir  string
	CacheDir   string
	Offline    bool
	NoDownload bool
	Parallel   int
	Verbose    bool
	DryRun     bool
	AuditLog   string
}

type ExecutionConfig struct {
	UseNamespace bool
	NoNetwork    bool
	ReadOnly     bool
	Mounts       []string
	Debug        bool
}

type AssembleConfig struct {
	ManifestPath string
	ProjectPath  string
	OutputPath   string
	TargetOS     string
	TargetArch   string
	Profile      BuildProfile
	EmbedFiles   []string
}

type CaptureConfig struct {
	ProjectPath string
	OutputPath  string
	TargetOS    string
	TargetArch  string
}

// ── Installation metadata ────────────────────────────────────────────────────

type Installation struct {
	AppName       string
	Version       string
	Path          string
	PythonBin     string
	MainModule    string
	PythonVersion string
}

// SBOM is the software bill of materials returned by verifier.SBOM.
type SBOM struct {
	AppName      string
	Version      string
	Installation string
	GeneratedAt  string
	Python       PythonSpec
	SystemDeps   []SystemDep
	PyPackages   []PyPackage
}
