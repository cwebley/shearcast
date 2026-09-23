package youtube

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Cache keeps each video's metadata and caption track on disk, keyed by video
// id, so a rerun never asks YouTube for them again.
//
// YouTube rate-limits caption fetches. Without a cache every run re-fetches
// every video, and a batch that trips the limit loses videos partway through.
type Cache struct {
	Dir string
}

// DefaultCacheDir is the user's cache directory (~/Library/Caches/shearcast on
// macOS), which survives reboots, unlike the system temp directory.
func DefaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "shearcast-cache")
	}
	return filepath.Join(dir, "shearcast")
}

// Info returns the video's metadata, from disk when it has been fetched before.
func (c Cache) Info(ctx context.Context, target string) (*Video, error) {
	if id := VideoID(target); id != "" {
		if v, ok := c.readInfo(id); ok {
			return v, nil
		}
	}
	v, err := Info(ctx, target)
	if err != nil {
		return nil, err
	}
	if err := c.writeInfo(v); err != nil {
		return nil, err
	}
	return v, nil
}

// Captions returns the path to the video's caption track, downloading it only
// when no track is on disk yet.
func (c Cache) Captions(ctx context.Context, v *Video) (string, error) {
	dir := c.videoDir(v.ID)
	if path, ok := cachedTrack(dir); ok {
		return path, nil
	}
	return Captions(ctx, v.URL(), dir)
}

// Audio returns the path to the video's full audio track, downloading it
// only when it is not on disk yet.
func (c Cache) Audio(ctx context.Context, v *Video) (string, error) {
	path := filepath.Join(c.videoDir(v.ID), "audio.m4a")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := DownloadAudio(ctx, v.URL(), path); err != nil {
		return "", err
	}
	return path, nil
}

// Cached reports whether both metadata and captions for id are on disk.
func (c Cache) Cached(id string) bool {
	if _, ok := c.readInfo(id); !ok {
		return false
	}
	_, ok := cachedTrack(c.videoDir(id))
	return ok
}

func (c Cache) videoDir(id string) string {
	return filepath.Join(c.Dir, id)
}

func (c Cache) readInfo(id string) (*Video, bool) {
	data, err := os.ReadFile(filepath.Join(c.videoDir(id), "info.json"))
	if err != nil {
		return nil, false
	}
	var v Video
	if json.Unmarshal(data, &v) != nil || v.ID != id {
		return nil, false
	}
	return &v, true
}

func (c Cache) writeInfo(v *Video) error {
	dir := c.videoDir(v.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// Write then rename, so an interrupted run never leaves a truncated file
	// that later reads as a cache hit.
	tmp := filepath.Join(dir, "info.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "info.json"))
}

// cachedTrack picks a caption track already in dir, by the same rule Captions
// uses after a download.
func cachedTrack(dir string) (string, bool) {
	path, err := pickTrack(dir)
	return path, err == nil
}

// VideoID extracts the video id from a bare id or a watch, youtu.be, shorts or
// live URL. It returns "" when target is none of those, such as a channel URL.
func VideoID(target string) string {
	if !strings.Contains(target, "/") {
		if validID(target) {
			return target
		}
		return ""
	}
	u, err := url.Parse(target)
	if err != nil {
		return ""
	}
	if id := u.Query().Get("v"); validID(id) {
		return id
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case strings.HasSuffix(u.Host, "youtu.be") && len(parts) == 1:
		if validID(parts[0]) {
			return parts[0]
		}
	case len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed"):
		if validID(parts[1]) {
			return parts[1]
		}
	}
	return ""
}

// validID matches YouTube's eleven-character base64url video ids.
func validID(s string) bool {
	if len(s) != 11 {
		return false
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}
