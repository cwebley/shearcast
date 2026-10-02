package storage_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/storage"
)

func TestFilesystemPublicationIsAtomicAndDeletionIsRepeatable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "public")
	s, err := storage.NewFilesystem(dir, "http://192.168.1.20:8080/podcasts/")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := "show/feed.xml"
	if _, err := s.Get(ctx, key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing feed: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("read created publication directory")
	}
	url, err := s.Put(ctx, key, strings.NewReader("old feed"), 8, "application/rss+xml")
	if err != nil || url != "http://192.168.1.20:8080/podcasts/show/feed.xml" {
		t.Fatalf("publish: %q %v", url, err)
	}
	reader := &failedReader{read: func() {
		data, err := s.Get(ctx, key)
		if err != nil || string(data) != "old feed" {
			t.Fatalf("partial replacement visible: %q %v", data, err)
		}
	}}
	if _, err := s.Put(ctx, key, reader, 12, "application/rss+xml"); err == nil {
		t.Fatal("accepted incomplete publication")
	}
	data, err := s.Get(ctx, key)
	if err != nil || string(data) != "old feed" {
		t.Fatalf("lost previous feed: %q %v", data, err)
	}
	if _, err := s.Put(ctx, key, strings.NewReader("new feed"), 8, "application/rss+xml"); err != nil {
		t.Fatal(err)
	}
	data, err = s.Get(ctx, key)
	if err != nil || string(data) != "new feed" {
		t.Fatalf("replacement: %q %v", data, err)
	}
	for i := 0; i < 2; i++ {
		if err := s.Delete(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Get(ctx, key); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("deleted feed: %v", err)
	}
}

type failedReader struct {
	read func()
	done bool
}

func (r *failedReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.ErrUnexpectedEOF
	}
	r.done = true
	r.read()
	return copy(p, "partial"), nil
}

func TestFilesystemRejectsSymlinksAndReclaimsInterruptedWrites(t *testing.T) {
	dir := t.TempDir()
	s, err := storage.NewFilesystem(dir, "http://lan.test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := os.Mkdir(filepath.Join(dir, "show"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../secret", filepath.Join(dir, "show/feed.xml")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "show/feed.xml"); err == nil {
		t.Fatal("followed symlink to private file")
	}
	if _, err := s.Put(ctx, "show/feed.xml", strings.NewReader("new"), 3, ""); err == nil {
		t.Fatal("replaced symlink")
	}
	if err := s.Delete(ctx, "show/feed.xml"); err == nil {
		t.Fatal("deleted symlink")
	}
	if err := os.Remove(filepath.Join(dir, "show/feed.xml")); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "show/.publish-feed.xml-interrupted")
	if err := os.WriteFile(staged, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "show/feed.xml", strings.NewReader("new"), 3, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatal("abandoned publication retained")
	}
	for _, key := range []string{"../secret", "/show/feed.xml", "show/../secret", "show\\feed.xml", "show/.env"} {
		if _, err := s.Get(ctx, key); err == nil {
			t.Fatalf("accepted %q", key)
		}
		if _, err := s.Put(ctx, key, strings.NewReader("bad"), 3, ""); err == nil {
			t.Fatalf("wrote %q", key)
		}
		if err := s.Delete(ctx, key); err == nil {
			t.Fatalf("deleted %q", key)
		}
	}
}

func TestFilesystemRejectsUnusableBaseURLs(t *testing.T) {
	for _, base := range []string{"http://lan.test?", "http://lan.test?token=x", "http://lan.test/#fragment", "http://user:password@lan.test", "file:///public", "http://lan.test/podcasts/../other", "http://lan.test/podcasts%2Fother", "http://lan.test/%2F", "http://:8080"} {
		if _, err := storage.NewFilesystem(t.TempDir(), base); err == nil {
			t.Fatalf("accepted unusable base URL %q", base)
		}
	}
}
