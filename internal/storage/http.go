package storage

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/cwebley/shearcast/internal/subscribe"
)

// Handler serves only podcast feeds and episode audio. It has no state lock or
// write operations. ServeContent handles HEAD, conditional requests and ranges
// using an open file, so a concurrent atomic replacement cannot truncate it.
func (s *Filesystem) Handler() http.Handler {
	u, _ := url.Parse(s.baseURL)
	prefix := strings.TrimRight(u.Path, "/") + "/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok || !publicKey(key) {
			http.NotFound(w, r)
			return
		}
		root, err := os.OpenRoot(s.directory)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		if err := rejectSymlinks(root, key); err != nil {
			http.NotFound(w, r)
			return
		}
		f, err := root.Open(key)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		contentType := "audio/mp4"
		if key == subscribe.PageKey {
			contentType = subscribe.PageType
		} else if key == subscribe.OPMLKey {
			contentType = subscribe.OPMLType
		} else if strings.HasSuffix(key, "/feed.xml") {
			contentType = "application/rss+xml"
		} else if IsChapterKey(key) {
			contentType = "application/json+chapters"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, info.ModTime().UnixNano(), info.Size()))
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	})
}

func publicKey(key string) bool {
	if !validKey(key) {
		return false
	}
	if key == subscribe.PageKey || key == subscribe.OPMLKey {
		return true
	}
	dir, name, _ := strings.Cut(key, "/")
	if strings.Contains(dir, ".") {
		return false
	}
	if name == "feed.xml" {
		return true
	}
	if IsChapterKey(key) {
		return true
	}
	return IsAudioKey(key)
}

// IsAudioKey also accepts content-addressed replacements so a feed switches
// enclosure and chapter metadata together, without overwriting live audio.
func IsAudioKey(key string) bool {
	if !validKey(key) {
		return false
	}
	dir, name, _ := strings.Cut(key, "/")
	if strings.Contains(dir, ".") {
		return false
	}
	stem, ok := strings.CutSuffix(name, ".m4a")
	if !ok {
		return false
	}
	id, digest, revision := strings.Cut(stem, ".")
	if len(id) != 11 {
		return false
	}
	if !revision {
		return true
	}
	_, err := hex.DecodeString(digest)
	return len(digest) == 64 && err == nil && strings.ToLower(digest) == digest
}

// IsChapterKey accepts only managed public chapter revisions, never arbitrary
// JSON such as private processing records. The digest names the JSON content.
func IsChapterKey(key string) bool {
	if !validKey(key) {
		return false
	}
	dir, name, _ := strings.Cut(key, "/")
	if strings.Contains(dir, ".") {
		return false
	}
	stem, ok := strings.CutSuffix(name, ".chapters.json")
	if !ok {
		return false
	}
	id, digest, ok := strings.Cut(stem, ".")
	if !ok || len(id) != 11 || strings.Contains(id, ".") || len(digest) != 64 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil && strings.ToLower(digest) == digest
}

func rejectSymlinks(root *os.Root, key string) error {
	dir, _, _ := strings.Cut(key, "/")
	for _, path := range []string{dir, key} {
		info, err := root.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("published paths cannot be symlinks: %s", path)
		}
	}
	return nil
}
