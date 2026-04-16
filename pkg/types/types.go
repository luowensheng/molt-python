package types

import "time"

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

// ── Manifest ──────────────────────────────────────────────────────────────────

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

// ── Config structs ────────────────────────────────────────────────────────────

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

	EmbedStrict      bool     // Enable strict mode (fail on sensitive files)
	EmbedIgnoreFile  string   // Path to .moltignore (default: ".moltignore")
	EmbedExtraIgnore []string // Additional patterns to exclude
	EmbedIncludeOnly []string // If set, ONLY these patterns are included (allowlist mode)
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

type Installation struct {
	AppName       string
	Version       string
	Path          string
	PythonBin     string
	MainModule    string
	PythonVersion string
}

type Snapshot struct {
	Python     PythonSpec
	SystemDeps []SystemDep
	PyPackages []PyPackage
	Source     []SourceFile
}

type SourceFile struct {
	RelPath string
	Data    []byte
}

type SBOM struct {
	AppName      string
	Version      string
	Installation string
	GeneratedAt  string
	Python       PythonSpec
	SystemDeps   []SystemDep
	PyPackages   []PyPackage
}

// ── Python version management ─────────────────────────────────────────────────

type PythonVersion struct {
	Version   string `json:"version"`
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
	Path      string `json:"path,omitempty"`
	Source    string `json:"source,omitempty"` // "standalone", "system", "pyenv"
}

// ── Dependency graph ──────────────────────────────────────────────────────────

type DepGraph struct {
	Root        string          `json:"root"`
	Version     string          `json:"version"`
	GeneratedAt time.Time       `json:"generated_at"`
	Platform    string          `json:"platform"`
	GlibcVer    string          `json:"glibc_version,omitempty"`
	Python      PythonInfo      `json:"python"`
	Source      []SourceInfo    `json:"source"`
	Packages    []PackageInfo   `json:"packages"`
	NativeExts  []NativeExt     `json:"native_extensions"`
	SystemLibs  []SysLibInfo    `json:"system_libs"`
	BuildEnv    BuildEnvInfo    `json:"build_env"`
	Security    SecuritySummary `json:"security"`
}

type PythonInfo struct {
	Version      string `json:"version"`
	SHA256       string `json:"sha256"`
	Path         string `json:"path"`
	BuildType    string `json:"build_type"` // standalone, system
	OpenSSL      string `json:"openssl,omitempty"`
	CompileFlags string `json:"compile_flags,omitempty"`
}

type SourceInfo struct {
	RelPath string `json:"rel_path"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Kind    string `json:"kind"` // source, config, data
}

type PackageInfo struct {
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	WheelSHA256 string       `json:"wheel_sha256,omitempty"`
	Pure        bool         `json:"pure"`
	ABI         string       `json:"abi,omitempty"`
	License     string       `json:"license,omitempty"`
	LastRelease string       `json:"last_release,omitempty"`
	Maintainers int          `json:"maintainers,omitempty"`
	DirectDep   bool         `json:"direct_dep"`
	RequiredBy  []string     `json:"required_by,omitempty"`
	Requires    []string     `json:"requires,omitempty"`
	Files       []RecordFile `json:"files,omitempty"`
	CVEs        []CVEInfo    `json:"cves,omitempty"`
	SigstoreOK  bool         `json:"sigstore_verified"`
}

type RecordFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type NativeExt struct {
	Path        string        `json:"path"`
	SHA256      string        `json:"sha256"`
	Package     string        `json:"package"`
	Hardening   HardeningInfo `json:"hardening"`
	ImportsFrom []string      `json:"imports_from"`
	RPATH       string        `json:"rpath,omitempty"`
}

type HardeningInfo struct {
	StackCanary bool   `json:"stack_canary"`
	RELRO       string `json:"relro"` // none, partial, full
	NX          bool   `json:"nx"`
	PIE         bool   `json:"pie"`
	Fortify     bool   `json:"fortify"`
}

type SysLibInfo struct {
	Name        string    `json:"name"`
	SHA256      string    `json:"sha256"`
	Path        string    `json:"path"`
	SONAME      string    `json:"soname,omitempty"`
	OSPackage   string    `json:"os_package,omitempty"`
	Standard    bool      `json:"standard"`
	MinRequired string    `json:"min_required,omitempty"`
	RequiredBy  []string  `json:"required_by"`
	CVEs        []CVEInfo `json:"cves,omitempty"`
}

type BuildEnvInfo struct {
	OS     string `json:"os"`
	Kernel string `json:"kernel"`
	Glibc  string `json:"glibc,omitempty"`
	GCC    string `json:"gcc,omitempty"`
	Molt   string `json:"molt"`
}

type SecuritySummary struct {
	Warnings     int      `json:"warnings"`
	Issues       []string `json:"issues,omitempty"`
	SecretsFound bool     `json:"secrets_found"`
	CVECount     int      `json:"cve_count"`
}

type CVEInfo struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Summary  string `json:"summary,omitempty"`
}

// ── Hash manifest ─────────────────────────────────────────────────────────────

type HashManifest struct {
	GeneratedAt string            `json:"generated_at"`
	Molt        string            `json:"molt"`
	Platform    string            `json:"platform"`
	GlibcVer    string            `json:"glibc_version,omitempty"`
	Python      HashEntry         `json:"python"`
	Source      []HashEntry       `json:"source"`
	Packages    []HashEntry       `json:"packages"`
	NativeExts  []HashEntry       `json:"native_extensions"`
	SystemLibs  []HashEntry       `json:"system_libs"`
	BuildEnv    map[string]string `json:"build_env"`
}

type HashEntry struct {
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Kind     string `json:"kind,omitempty"`
	Standard bool   `json:"standard,omitempty"`
}

// ── Env snapshot ──────────────────────────────────────────────────────────────

type EnvSnapshot struct {
	Name        string      `json:"name"`
	CreatedAt   time.Time   `json:"created_at"`
	ProjectPath string      `json:"project_path"`
	Python      string      `json:"python"`
	Packages    []HashEntry `json:"packages"`
	Files       []HashEntry `json:"files"`
}

// ── Import graph ──────────────────────────────────────────────────────────────

type ImportGraph struct {
	Nodes []ImportNode `json:"nodes"`
	Edges []ImportEdge `json:"edges"`
}

type ImportNode struct {
	ID      string `json:"id"`
	Module  string `json:"module"`
	File    string `json:"file"`
	Kind    string `json:"kind"` // source, stdlib, third-party
	Package string `json:"package,omitempty"`
	Used    bool   `json:"used"`
}

type ImportEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ── Templates ─────────────────────────────────────────────────────────────────

type TemplateSource string

const (
	TemplateBuiltin TemplateSource = "builtin"
	TemplateUser    TemplateSource = "user"
	TemplateProject TemplateSource = "project"
)

type TemplateMeta struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Tags        []string       `json:"tags"`
	Requires    []string       `json:"requires"` // pip packages needed
	Version     string         `json:"version"`
	Author      string         `json:"author,omitempty"`
	MultiFile   bool           `json:"multi_file"`
	Source      TemplateSource `json:"source"`
	Path        string         `json:"path"`
	Vars        []TemplateVar  `json:"vars"`
	Files       []TemplateFile `json:"files,omitempty"`
	Hooks       []TemplateHook `json:"hooks,omitempty"`
}

type TemplateVar struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
}

type TemplateFile struct {
	Src  string `json:"src"`
	Dst  string `json:"dst"`
	When string `json:"when,omitempty"`
}

type TemplateHook struct {
	Command string `json:"command"`
	When    string `json:"when,omitempty"`
}

// ── Scaffold / new ────────────────────────────────────────────────────────────

type ProjectType string

const (
	ProjectCLI      ProjectType = "cli"
	ProjectAPI      ProjectType = "api"
	ProjectWorker   ProjectType = "worker"
	ProjectLib      ProjectType = "lib"
	ProjectScript   ProjectType = "script"
	ProjectPlugin   ProjectType = "plugin"
	ProjectMonorepo ProjectType = "monorepo"
)

type ScaffoldConfig struct {
	Name        string
	Type        ProjectType
	Python      string
	Description string
	Author      string
	NoGit       bool
	NoTests     bool
	Minimal     bool
	OutputDir   string
}

// ── Tasks ─────────────────────────────────────────────────────────────────────

type Task struct {
	Name        string   `toml:"name" json:"name"`
	Command     string   `toml:"command" json:"command"`
	Description string   `toml:"description,omitempty" json:"description,omitempty"`
	Env         []string `toml:"env,omitempty" json:"env,omitempty"`
	Dir         string   `toml:"dir,omitempty" json:"dir,omitempty"`
}

// ── Dep analysis ─────────────────────────────────────────────────────────────

type DepConflict struct {
	Package     string
	Version     string
	Constraints []DepConstraint
}

type DepConstraint struct {
	RequiredBy string
	Constraint string
}

type DepRisk struct {
	Package     string
	Version     string
	Score       int // 0-100, higher = riskier
	Reasons     []string
	LastRelease string
	Maintainers int
	Dependents  int // how many of your deps need this
}
