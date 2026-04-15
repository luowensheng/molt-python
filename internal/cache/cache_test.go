package cache_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"pyexec/internal/cache"
)

func sum(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func TestNewAndHas(t *testing.T) {
	c, err := cache.New(t.TempDir())
	if err != nil { t.Fatal(err) }
	data := []byte("hello pyexec")
	key := sum(data)
	if c.Has(key) { t.Fatal("should not have key yet") }
	if err := c.Put(key, data); err != nil { t.Fatal(err) }
	if !c.Has(key) { t.Fatal("should have key after Put") }
}

func TestGet(t *testing.T) {
	c, _ := cache.New(t.TempDir())
	data := []byte("get test")
	key := sum(data)
	if _, ok := c.Get(key); ok { t.Fatal("should be absent") }
	c.Put(key, data)
	got, ok := c.Get(key)
	if !ok { t.Fatal("expected hit") }
	if string(got) != string(data) { t.Fatalf("got %q want %q", got, data) }
}

func TestPutChecksumMismatch(t *testing.T) {
	c, _ := cache.New(t.TempDir())
	err := c.Put("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", []byte("data"))
	if err == nil { t.Fatal("expected checksum mismatch error") }
}

func TestPath(t *testing.T) {
	dir := t.TempDir()
	c, _ := cache.New(dir)
	data := []byte("path test")
	key := sum(data)
	if c.Path(key) != "" { t.Fatal("path should be empty before put") }
	c.Put(key, data)
	if c.Path(key) != filepath.Join(dir, key) { t.Fatal("unexpected path") }
}

func TestPutStream(t *testing.T) {
	dir := t.TempDir()
	c, _ := cache.New(dir)
	data := []byte("stream content")
	key := sum(data)
	tmp, err := os.CreateTemp(dir, "stream")
	if err != nil { t.Fatal(err) }
	tmp.Write(data)
	tmp.Seek(0, 0)
	defer tmp.Close()
	dest, err := c.PutStream(key, tmp)
	if err != nil { t.Fatalf("PutStream: %v", err) }
	if dest != filepath.Join(dir, key) { t.Fatalf("dest %q", dest) }
}

func TestSize(t *testing.T) {
	c, _ := cache.New(t.TempDir())
	data := []byte("size content")
	c.Put(sum(data), data)
	sz, err := c.Size()
	if err != nil { t.Fatal(err) }
	if sz < int64(len(data)) { t.Fatalf("size %d < %d", sz, len(data)) }
}
