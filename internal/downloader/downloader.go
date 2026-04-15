package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"pyexec/internal/cache"
)

// Item is a single file to download.
type Item struct {
	URL    string
	SHA256 string
	Name   string
}

// Downloader fetches remote files, using a cache to avoid re-downloads.
type Downloader struct {
	Cache       *cache.Cache
	Parallelism int
	Verbose     bool
	Client      *http.Client
}

// New creates a Downloader.
func New(c *cache.Cache, parallelism int, verbose bool) *Downloader {
	if parallelism <= 0 {
		parallelism = 4
	}
	return &Downloader{
		Cache:       c,
		Parallelism: parallelism,
		Verbose:     verbose,
		Client:      &http.Client{},
	}
}

// DownloadAll fetches all items in parallel, skipping cached entries.
func (d *Downloader) DownloadAll(ctx context.Context, items []Item) error {
	if len(items) == 0 {
		return nil
	}
	sem := make(chan struct{}, d.Parallelism)
	errs := make(chan error, len(items))
	var wg sync.WaitGroup
	for _, item := range items {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := d.downloadOne(ctx, item); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *Downloader) downloadOne(ctx context.Context, item Item) error {
	if d.Cache.Has(item.SHA256) {
		if d.Verbose {
			fmt.Printf("  cached %s\n", item.Name)
		}
		return nil
	}
	if d.Verbose {
		fmt.Printf("  downloading %s\n", item.Name)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
	if err != nil {
		return fmt.Errorf("create request for %s: %w", item.Name, err)
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", item.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", item.Name, resp.StatusCode)
	}
	if _, err = d.Cache.PutStream(item.SHA256, resp.Body); err != nil {
		return fmt.Errorf("cache %s: %w", item.Name, err)
	}
	return nil
}

// CopyToDir copies a cached item to destDir/filename.
func (d *Downloader) CopyToDir(sha256, filename, destDir string) error {
	src := d.Cache.Path(sha256)
	if src == "" {
		return fmt.Errorf("not in cache: %s", sha256)
	}
	return copyFile(src, filepath.Join(destDir, filename))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
