package storage

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/cwebley/shearcast/internal/fileutil"
)

// Filesystem publishes complete files by atomic replacement. Construction and
// reads do not create directories, so a dry run can inspect an empty library.
// Writers must hold the shared episode-state lock.
type Filesystem struct{ directory, baseURL string }

func NewFilesystem(directory, baseURL string) (*Filesystem, error) {
	if directory == "" {
		return nil, fmt.Errorf("filesystem publishing requires a directory")
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("publishing base_url must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	urlPath := strings.TrimRight(u.Path, "/")
	if u.RawPath != "" || urlPath != "" && (path.Clean(urlPath) != urlPath || strings.Contains(urlPath, "\\")) {
		return nil, fmt.Errorf("publishing base_url must use a clean, unescaped path")
	}
	directory, err = fileutil.CanonicalPath(directory)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(directory); err == nil && !info.IsDir() {
		return nil, fmt.Errorf("publishing directory is not a directory")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return &Filesystem{directory: directory, baseURL: strings.TrimRight(u.String(), "/")}, nil
}

func (s *Filesystem) PublicURL(key string) string { return s.baseURL + "/" + key }

// LocalPath identifies a published artifact for the lifecycle's post-publication
// checkpoint. Processing records stay in the private cache.
func (s *Filesystem) LocalPath(key string) (string, error) {
	if !validKey(key) {
		return "", fmt.Errorf("invalid published key %q", key)
	}
	return filepath.Join(s.directory, filepath.FromSlash(key)), nil
}

func validKey(key string) bool {
	parts := strings.Split(key, "/")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.HasPrefix(part, ".") {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return false
			}
		}
	}
	return true
}

func (s *Filesystem) Put(ctx context.Context, key string, reader io.Reader, size int64, contentType string) (string, error) {
	if !validKey(key) || size < 0 {
		return "", fmt.Errorf("invalid publication key or size")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := fileutil.MkdirAll(s.directory); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := rejectSymlinks(root, key); err != nil {
		return "", err
	}
	dir, name, _ := strings.Cut(key, "/")
	if err := root.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err := syncRootDir(root, "."); err != nil {
		return "", err
	}
	if err := cleanupPublication(root, key); err != nil {
		return "", err
	}
	tmp := dir + "/.publish-" + name + "-" + rand.Text()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	defer root.Remove(tmp)
	defer f.Close()
	n, err := io.Copy(f, contextReader{ctx, reader})
	if err != nil {
		return "", err
	}
	if n != size {
		return "", fmt.Errorf("publication size mismatch: got %d, expected %d", n, size)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := root.Rename(tmp, key); err != nil {
		return "", err
	}
	if err := syncRootDir(root, dir); err != nil {
		return "", err
	}
	return s.PublicURL(key), nil
}

func (s *Filesystem) Get(ctx context.Context, key string) ([]byte, error) {
	if !validKey(key) {
		return nil, fmt.Errorf("invalid published key %q", key)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.directory)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := rejectSymlinks(root, key); err != nil {
		return nil, err
	}
	data, err := root.ReadFile(key)
	if os.IsNotExist(err) {
		return nil, ErrNotFound
	}
	return data, err
}

func (s *Filesystem) Delete(ctx context.Context, key string) error {
	if !validKey(key) {
		return fmt.Errorf("invalid published key %q", key)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(s.directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	if err := rejectSymlinks(root, key); err != nil {
		return err
	}
	if err := cleanupPublication(root, key); err != nil {
		return err
	}
	if err := root.Remove(key); err != nil && !os.IsNotExist(err) {
		return err
	}
	dir, _, _ := strings.Cut(key, "/")
	if err := syncRootDir(root, dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// A state-locked retry owns abandoned staging files for this exact object.
func cleanupPublication(root *os.Root, key string) error {
	dir, name, _ := strings.Cut(key, "/")
	f, err := root.Open(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".publish-"+name+"-") && !entry.IsDir() {
			if err := root.Remove(dir + "/" + entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func syncRootDir(root *os.Root, name string) error {
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
