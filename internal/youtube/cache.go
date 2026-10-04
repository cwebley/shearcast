package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/cwebley/shearcast/internal/fileutil"
	"github.com/cwebley/shearcast/internal/transcript"
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
	return c.RefreshInfo(ctx, target)
}

// RefreshInfo fetches metadata without downloading captions or source audio.
func (c Cache) RefreshInfo(ctx context.Context, target string) (*Video, error) {
	v, err := Info(ctx, target)
	if err != nil {
		return nil, err
	}
	if err := c.writeInfo(v); err != nil {
		return nil, err
	}
	return v, nil
}

func (c Cache) Cues(ctx context.Context, v *Video) ([]transcript.Cue, error) {
	path, err := c.Captions(ctx, v)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cues, err := parseUsableCues(f)
	if err != nil {
		f.Close()
		os.Remove(path) // do not turn malformed cached captions into a permanent failure
		return nil, err
	}
	return cues, nil
}

// RemoveAudio removes only the cached source, leaving metadata and captions.
func (c Cache) RemoveAudio(id string) error {
	err := os.Remove(c.SourceAudioPath(id))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return fileutil.SyncDir(c.videoDir(id))
}

func (c Cache) SourceAudioPath(id string) string {
	return filepath.Join(c.videoDir(id), "audio.m4a")
}

// CleanupWorking reclaims abandoned source downloads under the command lock.
// Completed audio, metadata and captions are left intact.
func (c Cache) CleanupWorking(id string) error {
	paths, err := filepath.Glob(filepath.Join(c.videoDir(id), ".download-*"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
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
// only when it is not on disk yet. progress may be nil.
func (c Cache) Audio(ctx context.Context, v *Video, progress func(string)) (string, error) {
	path := c.SourceAudioPath(v.ID)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := DownloadAudio(ctx, v.URL(), path, progress); err != nil {
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

// CachedInputs loads usable detection inputs strictly from disk. Missing,
// outdated or invalid inputs are errors; this never invokes yt-dlp, refreshes
// metadata, or removes malformed captions.
func (c Cache) CachedInputs(id string) (*Video, []transcript.Cue, error) {
	if !validID(id) {
		return nil, nil, fmt.Errorf("invalid video id %q", id)
	}
	v, ok := c.readInfo(id)
	if !ok {
		return nil, nil, fmt.Errorf("missing, outdated or invalid cached metadata in %s", c.videoDir(id))
	}
	if strings.TrimSpace(v.Title) == "" || v.Duration <= 0 || math.IsNaN(v.Duration) || math.IsInf(v.Duration, 0) {
		return nil, nil, fmt.Errorf("cached metadata needs a title and positive finite duration in %s", c.videoDir(id))
	}
	path, err := pickTrack(c.videoDir(id))
	if err != nil {
		return nil, nil, fmt.Errorf("cached captions in %s: %w", c.videoDir(id), err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	cues, err := parseUsableCues(f)
	if err != nil {
		return nil, nil, fmt.Errorf("cached captions %s: %w", path, err)
	}
	return v, cues, nil
}

func parseUsableCues(r io.Reader) ([]transcript.Cue, error) {
	cues, err := transcript.ParseVTT(r)
	if err != nil {
		return nil, fmt.Errorf("parsing captions: %w", err)
	}
	if len(transcript.Windows(cues, "W", 30, 45)) == 0 {
		return nil, fmt.Errorf("caption track contains no usable text")
	}
	return cues, nil
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
	// Older caches discarded chapters. Fetch full metadata once rather than
	// treating a missing field as evidence that the source has no chapters.
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields["chapters"] == nil {
		return nil, false
	}
	return &v, true
}

func (c Cache) writeInfo(v *Video) error {
	if !validID(v.ID) {
		return fmt.Errorf("refusing to cache metadata for invalid video id %q", v.ID)
	}
	dir := c.videoDir(v.ID)
	if err := fileutil.MkdirAll(dir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteAtomic(filepath.Join(dir, "info.json"), data, 0o600)
}

// cachedTrack picks a caption track already in dir, by the same rule Captions
// uses after a download.
func cachedTrack(dir string) (string, bool) {
	path, err := pickTrack(dir)
	return path, err == nil
}

// VideoID extracts the video id from a bare id or a YouTube watch, youtu.be,
// shorts, live or embed URL. It returns "" when target is none of those, such
// as a channel URL or another site's URL that happens to carry a v= parameter.
func VideoID(target string) string {
	if !strings.Contains(target, "/") {
		if validID(target) {
			return target
		}
		return ""
	}
	if !strings.Contains(target, "://") {
		target = "https://" + target
	}
	u, err := url.Parse(target)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host == "youtu.be" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 1 && validID(parts[0]) {
			return parts[0]
		}
		return ""
	}
	if host != "youtube.com" && !strings.HasSuffix(host, ".youtube.com") &&
		host != "youtube-nocookie.com" && !strings.HasSuffix(host, ".youtube-nocookie.com") {
		return ""
	}
	if id := u.Query().Get("v"); validID(id) {
		return id
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed") && validID(parts[1]) {
		return parts[1]
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
