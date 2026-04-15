package types

// BuildProfile defines how much to embed in the binary.
type BuildProfile string

const (
	ProfileMinimal  BuildProfile = "minimal"
	ProfileStandard BuildProfile = "standard"
	ProfileExtended BuildProfile = "extended"
	ProfileFull     BuildProfile = "full"
)

// InstallMode controls isolation level.
type InstallMode string

const (
	ModeMinimal    InstallMode = "minimal"
	ModeStandalone InstallMode = "standalone"
	ModeExact      InstallMode = "exact"
)

// Manifest is the hermetic environment description embedded in each binary.
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
}

// PythonSpec identifies the exact Python build.
type PythonSpec struct {
	Version  string `json:"version"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Embedded bool   `json:"embedded"`
}

// SystemDep is a shared library dependency.
type SystemDep struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	URL         string `json:"url,omitempty"`
	PackageName string `json:"package_name,omitempty"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	Embedded    bool   `json:"embedded"`
}

// PyPackage is a Python package from PyPI or direct URL.
type PyPackage struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Embedded bool   `json:"embedded"`
}

// BuildConfig controls how the binary is produced.
type BuildConfig struct {
	Profile     BuildProfile
	Name        string
	Version     string
	ProjectPath string
	OutputPath  string
	TargetOS    string // empty = current OS
	TargetArch  string // empty = current arch
	Offline     bool
	SignKeyPath  string
	EmbedFiles  []string
	MaxSizeMB      int
	CrossBuildMode CrossBuildMode // deny (default) or best-effort
}

// InstallConfig controls how installation proceeds.
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

// ExecutionConfig controls how Python is invoked.
type ExecutionConfig struct {
	UseNamespace bool
	NoNetwork    bool
	ReadOnly     bool
	Mounts       []string
	Debug        bool
}

// Installation represents an installed application environment.
type Installation struct {
	AppName       string
	Version       string
	Path          string
	PythonBin     string
	MainModule    string
	PythonVersion string
}

// Snapshot is the captured environment at build time.
type Snapshot struct {
	Python     PythonSpec
	SystemDeps []SystemDep
	PyPackages []PyPackage
	Source     []SourceFile
}

// SourceFile is a file to embed in the binary.
type SourceFile struct {
	RelPath string
	Data    []byte
}

// SBOM is a Software Bill of Materials.
type SBOM struct {
	AppName      string
	Version      string
	Installation string
	GeneratedAt  string
	Python       PythonSpec
	SystemDeps   []SystemDep
	PyPackages   []PyPackage
}

// AssembleConfig controls the assemble-only pipeline.
type AssembleConfig struct {
	ManifestPath string
	ProjectPath  string
	OutputPath   string
	TargetOS     string
	TargetArch   string
	Profile      BuildProfile
	EmbedFiles   []string
}

// CaptureConfig controls the capture-only pipeline.
type CaptureConfig struct {
	ProjectPath string
	OutputPath  string // path to write manifest JSON
	TargetOS    string
	TargetArch  string
}

// CrossBuildMode describes how a cross-build is handled.
type CrossBuildMode string

const (
	CrossBuildDeny       CrossBuildMode = "deny"        // error (default)
	CrossBuildBestEffort CrossBuildMode = "best-effort" // warn and continue
)
