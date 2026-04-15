package downloader_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"pyexec/internal/cache"
	"pyexec/internal/downloader"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCache(t *testing.T) *cache.Cache {
	t.Helper()
	c, err := cache.New(t.TempDir())
	require.NoError(t, err)
	return c
}

func TestDownloadAll_Success(t *testing.T) {
	payload := []byte("fake python binary content")
	h := sha256.Sum256(payload)
	checksum := hex.EncodeToString(h[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	c := newCache(t)
	dl := downloader.New(c, 2, false)
	dl.Client = srv.Client()

	items := []downloader.Item{
		{URL: srv.URL + "/python.tar.gz", SHA256: checksum, Name: "Python 3.12.1"},
	}

	err := dl.DownloadAll(context.Background(), items)
	require.NoError(t, err)
	assert.True(t, c.Has(checksum))
}

func TestDownloadAll_Cached(t *testing.T) {
	payload := []byte("already cached content")
	h := sha256.Sum256(payload)
	checksum := hex.EncodeToString(h[:])

	// Pre-populate cache.
	c := newCache(t)
	require.NoError(t, c.Put(checksum, payload))

	called := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dl := downloader.New(c, 2, false)
	dl.Client = srv.Client()

	items := []downloader.Item{
		{URL: srv.URL + "/file", SHA256: checksum, Name: "cached-item"},
	}

	err := dl.DownloadAll(context.Background(), items)
	require.NoError(t, err)
	assert.Equal(t, 0, called, "cached item should not trigger HTTP request")
}

func TestDownloadAll_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newCache(t)
	dl := downloader.New(c, 1, false)
	dl.Client = srv.Client()

	items := []downloader.Item{
		{URL: srv.URL + "/missing", SHA256: "abc", Name: "missing"},
	}

	err := dl.DownloadAll(context.Background(), items)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 404")
}

func TestDownloadAll_ChecksumMismatch(t *testing.T) {
	payload := []byte("real content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	c := newCache(t)
	dl := downloader.New(c, 1, false)
	dl.Client = srv.Client()

	items := []downloader.Item{
		{URL: srv.URL + "/file", SHA256: "wrong_checksum_aaaa", Name: "bad-checksum"},
	}

	err := dl.DownloadAll(context.Background(), items)
	assert.Error(t, err)
}

func TestDownloadAll_Empty(t *testing.T) {
	c := newCache(t)
	dl := downloader.New(c, 4, false)
	err := dl.DownloadAll(context.Background(), nil)
	require.NoError(t, err)
}

func TestCopyToDir(t *testing.T) {
	payload := []byte("copy me")
	h := sha256.Sum256(payload)
	checksum := hex.EncodeToString(h[:])

	c := newCache(t)
	require.NoError(t, c.Put(checksum, payload))

	destDir := t.TempDir()
	dl := downloader.New(c, 1, false)
	err := dl.CopyToDir(checksum, "output.bin", destDir)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(destDir, "output.bin"))
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

func TestCopyToDir_NotCached(t *testing.T) {
	c := newCache(t)
	dl := downloader.New(c, 1, false)
	err := dl.CopyToDir("nonexistent", "file", t.TempDir())
	assert.Error(t, err)
}

func TestParallelDownloads(t *testing.T) {
	const numItems = 8
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := []byte("content for " + r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	c := newCache(t)
	dl := downloader.New(c, 4, false)
	dl.Client = srv.Client()

	// Build items with correct checksums computed server-side.
	items := make([]downloader.Item, numItems)
	for i := 0; i < numItems; i++ {
		data := []byte("content for /file" + string(rune('0'+i)))
		h := sha256.Sum256(data)
		items[i] = downloader.Item{
			URL:    srv.URL + "/file" + string(rune('0'+i)),
			SHA256: hex.EncodeToString(h[:]),
			Name:   "item",
		}
	}

	err := dl.DownloadAll(context.Background(), items)
	require.NoError(t, err)
	for _, item := range items {
		assert.True(t, c.Has(item.SHA256))
	}
}
