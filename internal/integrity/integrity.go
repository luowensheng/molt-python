// Package integrity builds and verifies the transparency manifest.
//
// A single data structure serves two purposes:
//   1. Transparency — the written .manifest.json lists every packaged file
//      with size and hash. Human auditable; machine-diffable between builds.
//   2. Authentication — the RootHash field is embedded in the binary trailer
//      so the launcher can detect tampering with the payload.
//
// Both derive from the same []PackagedFile — no drift possible between what
// was hashed for verification and what the manifest claims was packaged.
//
// ── Root hash algorithm ───────────────────────────────────────────────────
//
// Goal: a single 32-byte digest that changes iff any file path, file
// contents, or the set of included files changes. Independent of ordering
// (sorting is applied), reimplementable in any language.
//
//   1. Sort PackagedFiles by Path (byte-wise, locale-independent).
//   2. For each file, compute inner = sha256(path || 0x00 || file_sha256).
//   3. Feed each inner digest (32 bytes) to a running outer SHA-256.
//   4. RootHash = hex(outer.Sum()).
//
// The inner hash domain-separates path and content and keeps the outer
// hash's input a stream of fixed-size 32-byte chunks. Simpler than a Merkle
// tree because we don't need partial verification.
package integrity

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"molt/pkg/types"
)

// SchemaID identifies this manifest format. Bump if the structure changes
// in a way older readers couldn't handle.
const SchemaID = "molt-integrity/v1"

// EmbeddedManifestPath is the path inside the payload tar where the
// integrity manifest is stored. Used for both writing (by the builder) and
// reading (by VerifyBinary).
const EmbeddedManifestPath = ".molt/integrity.json"

// ── Root hash ─────────────────────────────────────────────────────────────────

// ComputeRootHash returns the hex-encoded SHA-256 root hash over the file
// set. Input is copied and sorted internally; the caller's slice is not
// modified.
func ComputeRootHash(files []types.PackagedFile) string {
	sorted := make([]types.PackagedFile, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	h := sha256.New()
	for _, f := range sorted {
		inner := sha256.New()
		inner.Write([]byte(f.Path))
		inner.Write([]byte{0x00})
		raw, err := hex.DecodeString(f.SHA256)
		if err != nil || len(raw) != sha256.Size {
			// Should never happen — embedder always produces a valid hex
			// digest. If it does we incorporate the raw bytes so we fail
			// loudly rather than silently skip.
			inner.Write([]byte("INVALID:" + f.SHA256))
		} else {
			inner.Write(raw)
		}
		h.Write(inner.Sum(nil))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ComputeRootHashBytes is the raw 32-byte form for writing to the binary
// trailer.
func ComputeRootHashBytes(files []types.PackagedFile) [types.RootHashBytes]byte {
	hexStr := ComputeRootHash(files)
	var out [types.RootHashBytes]byte
	raw, _ := hex.DecodeString(hexStr)
	copy(out[:], raw)
	return out
}

// ── Manifest construction & I/O ───────────────────────────────────────────────

// BuildManifestFromConfig assembles a full IntegrityManifest using a
// MoltConfig as the source of truth. RootHash is computed internally;
// callers should not pre-fill it.
//
// IMPORTANT: the `files` passed in must already be the FINAL set that will
// ship in the payload. The builder should call BuildManifestFromConfig,
// write the manifest externally, AND also include it in the payload under
// EmbeddedManifestPath — but since that embedding would change the file
// list and thus the root hash, we break the cycle like this:
//
//   - The packaged tar contains EmbeddedManifestPath as a file.
//   - BuildManifestFromConfig's `files` argument should NOT include the
//     manifest entry — the manifest describes only the "content" files.
//   - VerifyBinary, when extracting the embedded manifest for comparison,
//     knows to exclude the manifest path from the set it rehashes.
//
// This way the root hash is stable.
func BuildManifestFromConfig(
	cfg *types.MoltConfig,
	files []types.PackagedFile,
	pythonVersion string,
	depPackages []types.ManifestDepPkg,
	glibcVersion string,
) *types.IntegrityManifest {
	sortedFiles := make([]types.PackagedFile, len(files))
	copy(sortedFiles, files)
	sort.Slice(sortedFiles, func(i, j int) bool { return sortedFiles[i].Path < sortedFiles[j].Path })

	var totalBytes int64
	for _, f := range sortedFiles {
		totalBytes += f.Size
	}

	m := &types.IntegrityManifest{
		Schema: SchemaID,
		App: types.ManifestApp{
			Name:    cfg.Project.Name,
			Version: cfg.Project.Version,
		},
		BuiltAt: time.Now().UTC().Format(time.RFC3339),
		BuildHost: types.ManifestHost{
			OS:    runtime.GOOS,
			Arch:  runtime.GOARCH,
			Glibc: glibcVersion,
		},
		Algorithm: "sha256",
		Payload: types.ManifestPayload{
			TotalFiles: int64(len(sortedFiles)),
			TotalBytes: totalBytes,
			Files:      sortedFiles,
		},
	}
	if pythonVersion != "" {
		m.Python = &types.ManifestPython{Version: pythonVersion}
	}
	if cfg.Deps != nil {
		m.Deps = &types.ManifestDeps{
			Strategy: string(cfg.Deps.Strategy),
			Files:    append([]string(nil), cfg.Deps.Files...),
			Packages: depPackages,
		}
	}
	if cfg.Assets != nil {
		for _, a := range cfg.Assets.Files {
			m.Assets = append(m.Assets, types.ManifestAsset{
				Path:        a.Path,
				Required:    a.Required,
				Description: a.Description,
				Found:       assetWasPackaged(a.Path, sortedFiles),
			})
		}
	}

	m.RootHash = ComputeRootHash(sortedFiles)
	return m
}

// assetWasPackaged checks whether any packaged file matches the asset path.
// Asset paths are project-relative; the embedder typically packages files
// under a "src/" prefix, so we check both unprefixed and prefixed forms.
// Also supports glob patterns via filepath.Match.
func assetWasPackaged(pattern string, packaged []types.PackagedFile) bool {
	pats := []string{pattern, filepath.ToSlash(filepath.Join("src", pattern))}
	for _, pf := range packaged {
		for _, p := range pats {
			if pf.Path == p {
				return true
			}
			if matched, _ := filepath.Match(p, pf.Path); matched {
				return true
			}
		}
	}
	return false
}

// WriteManifest serializes m to path as pretty JSON with a trailing newline.
func WriteManifest(m *types.IntegrityManifest, path string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ReadManifest deserializes a manifest from disk.
func ReadManifest(path string) (*types.IntegrityManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m types.IntegrityManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

// MarshalManifest is the same as WriteManifest but returns bytes rather
// than writing — used by the builder to embed the manifest into the tar.
func MarshalManifest(m *types.IntegrityManifest) ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ── Binary trailer ────────────────────────────────────────────────────────────
//
// Layout (48 bytes):
//   [payload_offset: 8 bytes LE int64]
//   [root_hash:     32 bytes raw sha256]
//   [magic:          8 bytes "MOLT0001"]
//
// Readers check magic first. Absent → fall back to legacy 8-byte trailer
// (offset only; no integrity check possible on those binaries).

// WriteTrailer appends a v1 trailer to w.
func WriteTrailer(w io.Writer, payloadOffset int64, rootHash [types.RootHashBytes]byte) error {
	if err := binary.Write(w, binary.LittleEndian, payloadOffset); err != nil {
		return err
	}
	if _, err := w.Write(rootHash[:]); err != nil {
		return err
	}
	_, err := w.Write([]byte(types.TrailerMagic))
	return err
}

// TrailerInfo is what ReadTrailer returns.
type TrailerInfo struct {
	PayloadOffset int64
	RootHash      [types.RootHashBytes]byte
	HasIntegrity  bool // false for legacy 8-byte trailers
}

// ReadTrailer opens path and parses its trailer.
func ReadTrailer(path string) (*TrailerInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadTrailerFrom(f)
}

// ReadTrailerFrom parses the trailer from a file-shaped source.
func ReadTrailerFrom(f *os.File) (*TrailerInfo, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size < types.LegacyTrailerSize {
		return nil, fmt.Errorf("binary too small (%d bytes)", size)
	}

	// Try v1 (magic check).
	if size >= int64(types.TrailerV1Size) {
		buf := make([]byte, types.TrailerV1Size)
		if _, err := f.ReadAt(buf, size-int64(types.TrailerV1Size)); err != nil {
			return nil, fmt.Errorf("read v1 trailer: %w", err)
		}
		magic := buf[types.TrailerV1Size-len(types.TrailerMagic):]
		if string(magic) == types.TrailerMagic {
			ti := &TrailerInfo{HasIntegrity: true}
			ti.PayloadOffset = int64(binary.LittleEndian.Uint64(buf[0:8]))
			copy(ti.RootHash[:], buf[8:8+types.RootHashBytes])
			return ti, nil
		}
	}

	// Legacy fallback.
	buf := make([]byte, types.LegacyTrailerSize)
	if _, err := f.ReadAt(buf, size-int64(types.LegacyTrailerSize)); err != nil {
		return nil, fmt.Errorf("read legacy trailer: %w", err)
	}
	return &TrailerInfo{
		PayloadOffset: int64(binary.LittleEndian.Uint64(buf)),
		HasIntegrity:  false,
	}, nil
}

// ── Verification ──────────────────────────────────────────────────────────────

// VerifyBinary checks integrity of binaryPath without executing it:
//   1. Parse trailer → get claimed root hash + payload offset.
//   2. Stream the payload, extract the embedded manifest.
//   3. Check trailer.RootHash == manifest.RootHash (consistency).
//   4. Recompute hash over manifest.Payload.Files and check it still
//      matches manifest.RootHash (catches manually-edited manifests).
//
// Does NOT re-hash the payload contents against the files list — that
// would require extracting the full payload. VerifyExtracted handles
// post-extraction verification.
func VerifyBinary(binaryPath string) error {
	trailer, err := ReadTrailer(binaryPath)
	if err != nil {
		return err
	}
	if !trailer.HasIntegrity {
		return fmt.Errorf("binary has no integrity information (legacy trailer)")
	}

	m, err := ExtractEmbeddedManifest(binaryPath, trailer.PayloadOffset)
	if err != nil {
		return fmt.Errorf("extract embedded manifest: %w", err)
	}

	trailerHex := hex.EncodeToString(trailer.RootHash[:])
	if trailerHex != m.RootHash {
		return fmt.Errorf(
			"trailer root_hash %s does not match embedded manifest root_hash %s (tampering?)",
			short(trailerHex), short(m.RootHash))
	}

	computed := ComputeRootHash(m.Payload.Files)
	if computed != m.RootHash {
		return fmt.Errorf(
			"embedded manifest root_hash %s does not match recomputed hash %s over its file list (manifest was modified)",
			short(m.RootHash), short(computed))
	}
	return nil
}

// VerifyBinaryAgainstManifest is the stronger check: it also streams the
// payload and rehashes every file, then compares the recomputed root hash
// against the trailer. Used by `molt verify --deep`.
func VerifyBinaryAgainstManifest(binaryPath string) error {
	trailer, err := ReadTrailer(binaryPath)
	if err != nil {
		return err
	}
	if !trailer.HasIntegrity {
		return fmt.Errorf("binary has no integrity information (legacy trailer)")
	}

	files, err := streamPayloadHashes(binaryPath, trailer.PayloadOffset)
	if err != nil {
		return fmt.Errorf("rehash payload: %w", err)
	}

	computed := ComputeRootHash(files)
	trailerHex := hex.EncodeToString(trailer.RootHash[:])
	if computed != trailerHex {
		return fmt.Errorf(
			"payload root_hash %s does not match trailer %s",
			short(computed), short(trailerHex))
	}
	return nil
}

// VerifyExtracted walks extractedRoot, hashes every file listed in `files`,
// and checks the recomputed root hash matches expectedHash. Used at
// install-time by the launcher when verify_on_install is enabled.
func VerifyExtracted(extractedRoot string, files []types.PackagedFile, expectedHash string) error {
	actual := make([]types.PackagedFile, 0, len(files))
	for _, f := range files {
		path := filepath.Join(extractedRoot, filepath.FromSlash(f.Path))
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("missing file %s: %w", f.Path, err)
		}
		if info.Size() != f.Size {
			return fmt.Errorf("size mismatch for %s: expected %d, got %d",
				f.Path, f.Size, info.Size())
		}
		h, err := hashFile(path)
		if err != nil {
			return fmt.Errorf("hash %s: %w", f.Path, err)
		}
		actual = append(actual, types.PackagedFile{
			Path:   f.Path,
			Size:   info.Size(),
			SHA256: h,
			Source: f.Source,
		})
	}
	got := ComputeRootHash(actual)
	if got != expectedHash {
		return fmt.Errorf(
			"extracted tree root_hash %s does not match expected %s",
			short(got), short(expectedHash))
	}
	return nil
}

// ── Payload streaming ─────────────────────────────────────────────────────────

// ExtractEmbeddedManifest reads the `.molt/integrity.json` entry out of the
// payload tar without extracting anything else. Caller provides the offset
// at which the tar.gz payload starts (from the trailer).
func ExtractEmbeddedManifest(binaryPath string, payloadOffset int64) (*types.IntegrityManifest, error) {
	f, err := os.Open(binaryPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(payloadOffset, io.SeekStart); err != nil {
		return nil, err
	}

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	targets := []string{
		EmbeddedManifestPath,
		"src/" + EmbeddedManifestPath, // the builder packages under src/
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := filepath.ToSlash(hdr.Name)
		for _, t := range targets {
			if name == t {
				data, err := io.ReadAll(tr)
				if err != nil {
					return nil, err
				}
				var m types.IntegrityManifest
				if err := json.Unmarshal(data, &m); err != nil {
					return nil, fmt.Errorf("parse embedded manifest: %w", err)
				}
				return &m, nil
			}
		}
	}
	return nil, fmt.Errorf("no embedded manifest found at %s in payload", EmbeddedManifestPath)
}

// streamPayloadHashes walks every tar entry in the payload, hashes it, and
// returns the corresponding PackagedFile list. Excludes the embedded
// manifest itself — the root hash is defined over content files only.
func streamPayloadHashes(binaryPath string, payloadOffset int64) ([]types.PackagedFile, error) {
	f, err := os.Open(binaryPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(payloadOffset, io.SeekStart); err != nil {
		return nil, err
	}

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	// Strip the tar prefix (currently "src/") when reporting paths, so we
	// match what BuildManifest records. This must stay in sync with the
	// embedder's PrefixInTar setting.
	const tarPrefix = "src/"

	var out []types.PackagedFile
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := filepath.ToSlash(hdr.Name)
		// Skip embedded manifest — it's not part of the hashed content set.
		if name == EmbeddedManifestPath || name == tarPrefix+EmbeddedManifestPath {
			// Drain the body so the tar reader advances.
			_, _ = io.Copy(io.Discard, tr)
			continue
		}
		reported := strings.TrimPrefix(name, tarPrefix)

		h := sha256.New()
		n, err := io.Copy(h, tr)
		if err != nil {
			return nil, err
		}
		out = append(out, types.PackagedFile{
			Path:   reported,
			Size:   n,
			SHA256: hex.EncodeToString(h.Sum(nil)),
			// Source unknown at stream time — not needed for root hash.
		})
	}
	return out, nil
}

// ── Diff ──────────────────────────────────────────────────────────────────────

// DiffResult summarizes changes between two manifests.
type DiffResult struct {
	Added    []types.PackagedFile
	Removed  []types.PackagedFile
	Changed  []DiffChange
	SizeDiff int64
}

// DiffChange is a file present in both manifests but with different content.
type DiffChange struct {
	Path    string
	OldSize int64
	NewSize int64
	OldSHA  string
	NewSHA  string
}

// Diff compares two manifests. a is the baseline, b is the new version.
func Diff(a, b *types.IntegrityManifest) DiffResult {
	aIdx := make(map[string]types.PackagedFile, len(a.Payload.Files))
	for _, f := range a.Payload.Files {
		aIdx[f.Path] = f
	}
	bIdx := make(map[string]types.PackagedFile, len(b.Payload.Files))
	for _, f := range b.Payload.Files {
		bIdx[f.Path] = f
	}

	var res DiffResult
	res.SizeDiff = b.Payload.TotalBytes - a.Payload.TotalBytes

	for p, bf := range bIdx {
		af, ok := aIdx[p]
		if !ok {
			res.Added = append(res.Added, bf)
			continue
		}
		if af.SHA256 != bf.SHA256 {
			res.Changed = append(res.Changed, DiffChange{
				Path: p, OldSize: af.Size, NewSize: bf.Size,
				OldSHA: af.SHA256, NewSHA: bf.SHA256,
			})
		}
	}
	for p, af := range aIdx {
		if _, ok := bIdx[p]; !ok {
			res.Removed = append(res.Removed, af)
		}
	}
	sort.Slice(res.Added, func(i, j int) bool { return res.Added[i].Path < res.Added[j].Path })
	sort.Slice(res.Removed, func(i, j int) bool { return res.Removed[i].Path < res.Removed[j].Path })
	sort.Slice(res.Changed, func(i, j int) bool { return res.Changed[i].Path < res.Changed[j].Path })
	return res
}

// FormatDiff renders DiffResult as a human-readable summary.
func FormatDiff(d DiffResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Added:   %d file(s)\n", len(d.Added))
	for _, f := range d.Added {
		fmt.Fprintf(&b, "  + %s (%s)\n", f.Path, humanBytes(f.Size))
	}
	fmt.Fprintf(&b, "Removed: %d file(s)\n", len(d.Removed))
	for _, f := range d.Removed {
		fmt.Fprintf(&b, "  - %s (%s)\n", f.Path, humanBytes(f.Size))
	}
	fmt.Fprintf(&b, "Changed: %d file(s)\n", len(d.Changed))
	for _, c := range d.Changed {
		delta := c.NewSize - c.OldSize
		sign := "+"
		if delta < 0 {
			sign = ""
		}
		fmt.Fprintf(&b, "  ~ %s (%s%d bytes)\n", c.Path, sign, delta)
	}
	fmt.Fprintf(&b, "\nTotal size change: %+d bytes\n", d.SizeDiff)
	return b.String()
}

// ── Pretty summary ────────────────────────────────────────────────────────────

// Summarize returns a human-readable multi-line summary of a manifest.
// Used by `molt inspect`.
func Summarize(m *types.IntegrityManifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "App:         %s v%s\n", m.App.Name, m.App.Version)
	fmt.Fprintf(&b, "Built:       %s (%s/%s", m.BuiltAt, m.BuildHost.OS, m.BuildHost.Arch)
	if m.BuildHost.Glibc != "" {
		fmt.Fprintf(&b, ", glibc %s", m.BuildHost.Glibc)
	}
	fmt.Fprintln(&b, ")")
	fmt.Fprintf(&b, "Algorithm:   %s\n", m.Algorithm)
	fmt.Fprintf(&b, "Root hash:   %s\n", m.RootHash)
	fmt.Fprintf(&b, "Files:       %d\n", m.Payload.TotalFiles)
	fmt.Fprintf(&b, "Total size:  %s (%d bytes)\n", humanBytes(m.Payload.TotalBytes), m.Payload.TotalBytes)

	if m.Python != nil && m.Python.Version != "" {
		fmt.Fprintf(&b, "Python:      %s\n", m.Python.Version)
	}
	if m.Deps != nil {
		fmt.Fprintf(&b, "Deps:        strategy=%s", m.Deps.Strategy)
		if len(m.Deps.Files) > 0 {
			fmt.Fprintf(&b, ", files=%s", strings.Join(m.Deps.Files, ","))
		}
		if len(m.Deps.Packages) > 0 {
			fmt.Fprintf(&b, ", %d pkg(s)", len(m.Deps.Packages))
		}
		fmt.Fprintln(&b)
	}

	if len(m.Assets) > 0 {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "Assets:")
		for _, a := range m.Assets {
			mark := "✓"
			if !a.Found {
				mark = "✗"
			}
			req := ""
			if a.Required {
				req = " [required]"
			}
			fmt.Fprintf(&b, "  %s %s%s", mark, a.Path, req)
			if a.Description != "" {
				fmt.Fprintf(&b, "  — %s", a.Description)
			}
			fmt.Fprintln(&b)
		}
	}
	return b.String()
}

// FormatFileList renders the file list as a sortable table. Used by
// `molt inspect --files`.
func FormatFileList(m *types.IntegrityManifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-60s  %10s  %12s  %s\n", "path", "size", "source", "sha256")
	fmt.Fprintln(&b, strings.Repeat("─", 105))
	for _, f := range m.Payload.Files {
		fmt.Fprintf(&b, "%-60s  %10s  %12s  %s\n",
			truncPath(f.Path, 60), humanBytes(f.Size), f.Source, short(f.SHA256))
	}
	return b.String()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func short(h string) string {
	if len(h) > 16 {
		return h[:16] + "…"
	}
	return h
}

func truncPath(p string, maxLen int) string {
	if len(p) <= maxLen {
		return p
	}
	return "…" + p[len(p)-maxLen+1:]
}
