// embedder/embed_test.go
package embedder

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"molt/pkg/types"
)

// scaffold creates a small fake project tree under t.TempDir and returns the
// root path. Each string is "relative/path=contents".
func scaffold(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		parts := strings.SplitN(f, "=", 2)
		path := filepath.Join(root, parts[0])
		contents := ""
		if len(parts) == 2 {
			contents = parts[1]
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// tarContents reads the paths of every entry in a tar.gz. Helper for tests.
func tarContents(t *testing.T, archive string) []string {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()

	var names []string
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	sort.Strings(names)
	return names
}

// Allowlist mode: only files matching include patterns end up in the archive.
// Crucially, tests/ is NOT in the default ignore list's scope here — we
// verify the switch from denylist to allowlist really changed selection.
func TestAllowlistMode_OnlyIncludesMatchedFiles(t *testing.T) {
	root := scaffold(t,
		"src/app.py=print('hi')",
		"src/util/helper.py=def f(): pass",
		"tests/test_app.py=import pytest",
		"notebooks/explore.ipynb={}",
		"README.md=# project",
	)
	out := filepath.Join(t.TempDir(), "payload.tar.gz")

	res, err := Run(Config{
		RootDir:      root,
		OutputFile:   out,
		PrefixInTar:  "src/",
		IncludeGlobs: []string{"src/**/*.py"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Exactly two .py files should have been packaged.
	got := map[string]bool{}
	for _, f := range res.Files {
		got[f.Path] = true
	}
	for _, wantPath := range []string{"src/app.py", "src/util/helper.py"} {
		if !got[wantPath] {
			t.Errorf("missing %s", wantPath)
		}
	}
	for _, forbidden := range []string{"tests/test_app.py", "notebooks/explore.ipynb", "README.md"} {
		if got[forbidden] {
			t.Errorf("should not have included %s", forbidden)
		}
	}

	// Tar archive should reflect the same set (with prefix applied).
	names := tarContents(t, out)
	for _, n := range names {
		if !strings.HasPrefix(n, "src/") {
			t.Errorf("archive entry %q missing prefix", n)
		}
	}
}

// Required assets that don't exist abort the build — this is core to the
// "molt tells you when something's missing" promise.
func TestAssets_RequiredMissingAborts(t *testing.T) {
	root := scaffold(t, "src/app.py=x")
	_, err := Run(Config{
		RootDir:      root,
		OutputFile:   filepath.Join(t.TempDir(), "p.tar.gz"),
		PrefixInTar:  "src/",
		IncludeGlobs: []string{"src/**/*.py"},
		Assets: []types.MoltAssetFile{
			{Path: "models/weights.bin", Required: true},
		},
	})
	if err == nil {
		t.Fatal("expected error for missing required asset")
	}
	if !strings.Contains(err.Error(), "weights.bin") {
		t.Errorf("error should name the missing asset: %v", err)
	}
}

// Optional assets that don't match are silently skipped.
func TestAssets_OptionalMissingIsSilent(t *testing.T) {
	root := scaffold(t, "src/app.py=x")
	res, err := Run(Config{
		RootDir:      root,
		OutputFile:   filepath.Join(t.TempDir(), "p.tar.gz"),
		PrefixInTar:  "src/",
		IncludeGlobs: []string{"src/**/*.py"},
		Assets: []types.MoltAssetFile{
			{Path: "locales/*.po", Required: false},
		},
	})
	if err != nil {
		t.Fatalf("optional missing assets should not fail: %v", err)
	}
	if len(res.Files) != 1 {
		t.Errorf("want 1 file (app.py), got %d", len(res.Files))
	}
}

// An asset matched by glob is tagged SourceAsset in the manifest, and its
// description is preserved — enables "why is this shipped?" answers.
func TestAssets_GlobMatchedCarriesDescription(t *testing.T) {
	root := scaffold(t,
		"src/app.py=x",
		"models/tiny.bin=\x00\x01",
		"models/huge.bin=\x00\x01\x02",
	)
	res, err := Run(Config{
		RootDir:      root,
		OutputFile:   filepath.Join(t.TempDir(), "p.tar.gz"),
		PrefixInTar:  "src/",
		IncludeGlobs: []string{"src/**/*.py"},
		Assets: []types.MoltAssetFile{
			{Path: "models/*.bin", Required: true, Description: "ML model weights"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]types.PackagedFile{}
	for _, f := range res.Files {
		byPath[f.Path] = f
	}
	for _, name := range []string{"models/tiny.bin", "models/huge.bin"} {
		f, ok := byPath[name]
		if !ok {
			t.Fatalf("missing asset %s", name)
		}
		if f.Source != types.SourceAsset {
			t.Errorf("%s source = %q, want asset", name, f.Source)
		}
		if f.Description != "ML model weights" {
			t.Errorf("%s description lost: %q", name, f.Description)
		}
	}
}

// Sensitive files (private keys, etc.) are blocked EVEN in allowlist mode
// when the user would otherwise have globbed them in.
func TestSensitive_BlockedEvenIfIncluded(t *testing.T) {
	root := scaffold(t,
		"src/app.py=x",
		"src/id_rsa=-----BEGIN PRIVATE KEY-----",
	)
	_, err := Run(Config{
		RootDir:         root,
		OutputFile:      filepath.Join(t.TempDir(), "p.tar.gz"),
		PrefixInTar:     "src/",
		IncludeGlobs:    []string{"src/**/*"},
		FailOnSensitive: true,
	})
	if err == nil || !strings.Contains(err.Error(), "SECURITY BLOCK") {
		t.Fatalf("want SECURITY BLOCK error, got %v", err)
	}
}

// Deterministic output: running twice with identical input produces
// byte-identical archives. This is the foundation of reproducible builds.
func TestDeterministic_IdenticalInputs(t *testing.T) {
	root := scaffold(t,
		"a/x.py=content-a",
		"b/y.py=content-b",
	)
	out1 := filepath.Join(t.TempDir(), "p1.tar.gz")
	out2 := filepath.Join(t.TempDir(), "p2.tar.gz")

	for _, out := range []string{out1, out2} {
		if _, err := Run(Config{
			RootDir:      root,
			OutputFile:   out,
			PrefixInTar:  "src/",
			IncludeGlobs: []string{"**/*.py"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	b1, _ := os.ReadFile(out1)
	b2, _ := os.ReadFile(out2)
	if string(b1) != string(b2) {
		t.Error("archives differ despite identical inputs — build is non-deterministic")
	}
}

// Exclude globs subtract from the include set even when they'd otherwise match.
func TestAllowlistMode_ExcludeRemovesFromInclude(t *testing.T) {
	root := scaffold(t,
		"src/app.py=main",
		"src/app_test.py=pytest",
	)
	res, err := Run(Config{
		RootDir:      root,
		OutputFile:   filepath.Join(t.TempDir(), "p.tar.gz"),
		PrefixInTar:  "src/",
		IncludeGlobs: []string{"src/**/*.py"},
		ExcludeGlobs: []string{"**/*_test.py"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		if strings.HasSuffix(f.Path, "_test.py") {
			t.Errorf("exclude pattern failed to remove %s", f.Path)
		}
	}
}

// MaxTotalSizeMB aborts the build — otherwise users could ship a 5GB binary
// by accident and not discover it until CI upload fails.
func TestMaxTotalSize_Aborts(t *testing.T) {
	// One 2 MB file with a 1 MB limit.
	big := strings.Repeat("x", 2*1024*1024)
	root := scaffold(t, "src/big.dat="+big)
	_, err := Run(Config{
		RootDir:        root,
		OutputFile:     filepath.Join(t.TempDir(), "p.tar.gz"),
		PrefixInTar:    "src/",
		IncludeGlobs:   []string{"src/**/*"},
		MaxTotalSizeMB: 1,
	})
	if err == nil {
		t.Fatal("expected total-size error")
	}
}

// MaxFileSizeMB WARNS, it does not fail — used to flag oversize assets
// without preventing the build.
func TestMaxFileSize_WarnsNotFails(t *testing.T) {
	big := strings.Repeat("x", 2*1024*1024)
	root := scaffold(t, "src/big.dat="+big)
	res, err := Run(Config{
		RootDir:       root,
		OutputFile:    filepath.Join(t.TempDir(), "p.tar.gz"),
		PrefixInTar:   "src/",
		IncludeGlobs:  []string{"src/**/*"},
		MaxFileSizeMB: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SizeWarnings) == 0 {
		t.Error("expected a size warning for oversize file")
	}
}

// Legacy mode (no include globs) matches the original ignore-list behaviour:
// .venv/, __pycache__/, tests/ excluded by default; src/ stays.
func TestLegacyMode_DefaultIgnoreList(t *testing.T) {
	root := scaffold(t,
		"src/app.py=x",
		".venv/lib/site-packages/foo.py=should-skip",
		"__pycache__/foo.cpython-311.pyc=\x00",
	)
	res, err := Run(Config{
		RootDir:     root,
		OutputFile:  filepath.Join(t.TempDir(), "p.tar.gz"),
		PrefixInTar: "src/",
		// no IncludeGlobs → legacy mode
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		if strings.HasPrefix(f.Path, ".venv/") {
			t.Errorf("legacy mode leaked venv file: %s", f.Path)
		}
		if strings.Contains(f.Path, "__pycache__") {
			t.Errorf("legacy mode leaked pycache file: %s", f.Path)
		}
	}
}

// Source tagging: the PackagedFile.Source tells us WHY a file was included.
// This is important for the integrity manifest's audit trail.
func TestSourceTagging(t *testing.T) {
	root := scaffold(t,
		"src/app.py=x",
		"models/w.bin=\x00",
	)
	res, err := Run(Config{
		RootDir:      root,
		OutputFile:   filepath.Join(t.TempDir(), "p.tar.gz"),
		PrefixInTar:  "src/",
		IncludeGlobs: []string{"src/**/*.py"},
		Assets:       []types.MoltAssetFile{{Path: "models/w.bin", Required: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		switch f.Path {
		case "src/app.py":
			if f.Source != types.SourceInclude {
				t.Errorf("app.py should be SourceInclude, got %q", f.Source)
			}
		case "models/w.bin":
			if f.Source != types.SourceAsset {
				t.Errorf("w.bin should be SourceAsset, got %q", f.Source)
			}
		}
	}
}
