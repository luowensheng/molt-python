// Package integrity: builder-compat shims.
//
// The builder.go file was written against an earlier iteration of the
// integrity API. Rather than rewrite the builder in four places, we expose
// a compatible facade here that adapts the simpler, newer Build path. All
// functions in this file are thin wrappers — if the wrapped functions
// evolve, adjust these.
package integrity

import (
	"encoding/hex"
	"fmt"
	"io"
	"runtime"
	"sort"
	"time"

	"molt/pkg/types"
)

// ManifestOptions is the legacy shape the builder uses to configure
// BuildManifest. Keep it stable.
type ManifestOptions struct {
	MoltVersion   string
	Deps          *types.IntegrityDeps
	PythonVersion string
	Glibc         string
	// Assets mirrors what BuildManifest would derive from a full MoltConfig.
	// If nil, no asset list is emitted.
	Assets []types.ManifestAsset
}

// BuildManifest is the 3-argument signature used by the builder. It wraps
// BuildManifestFromConfig but skips the MoltConfig plumbing — callers who
// already have a resolved app identity and an embedder result only need
// these three things.
func BuildManifest(
	app types.IntegrityApp,
	files []types.PackagedFile,
	opts ManifestOptions,
) *types.IntegrityManifest {
	sorted := make([]types.PackagedFile, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	var total int64
	for _, f := range sorted {
		total += f.Size
	}

	m := &types.IntegrityManifest{
		Schema: SchemaID,
		App: types.ManifestApp{
			Name:    app.Name,
			Version: app.Version,
		},
		BuiltAt: time.Now().UTC().Format(time.RFC3339),
		BuildHost: types.ManifestHost{
			OS:    runtime.GOOS,
			Arch:  runtime.GOARCH,
			Glibc: opts.Glibc,
		},
		Algorithm: "sha256",
		Payload: types.ManifestPayload{
			TotalFiles: int64(len(sorted)),
			TotalBytes: total,
			Files:      sorted,
		},
	}
	if opts.PythonVersion != "" {
		m.Python = &types.ManifestPython{Version: opts.PythonVersion}
	}
	if opts.Deps != nil {
		m.Deps = &types.ManifestDeps{
			Strategy: opts.Deps.Strategy,
			Files:    append([]string(nil), opts.Deps.Files...),
		}
	}
	m.Assets = opts.Assets
	m.RootHash = ComputeRootHash(sorted)
	return m
}

// WriteExtendedTrailer is the string-hex-input version of WriteTrailer.
// Used by the builder which has the root hash as a hex string (from
// ComputeRootHash). Decodes hex to the required 32-byte form internally.
func WriteExtendedTrailer(w io.Writer, payloadOffset int64, rootHashHex string) error {
	raw, err := hex.DecodeString(rootHashHex)
	if err != nil {
		return fmt.Errorf("invalid root_hash hex: %w", err)
	}
	if len(raw) != types.RootHashBytes {
		return fmt.Errorf("root_hash length %d != %d", len(raw), types.RootHashBytes)
	}
	var arr [types.RootHashBytes]byte
	copy(arr[:], raw)
	return WriteTrailer(w, payloadOffset, arr)
}
