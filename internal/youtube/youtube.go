// Package youtube shells out to yt-dlp.
//
// A pure-Go extractor would break every time YouTube changes its player.
// yt-dlp has maintainers for that fight, so this package only builds argument
// lists and parses JSON.
package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Binary is the yt-dlp executable; override for tests or a pinned install.
var Binary = "yt-dlp"

// Video is the metadata we need about one upload.
type Video struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Channel     string  `json:"channel"`
	ChannelID   string  `json:"channel_id"`
	Duration    float64 `json:"duration"`
	UploadDate  string  `json:"upload_date"` // YYYYMMDD
	Thumbnail   string  `json:"thumbnail"`
	WebpageURL  string  `json:"webpage_url"`
}

// URL returns a canonical watch URL for the video.
func (v Video) URL() string {
	if v.WebpageURL != "" {
		return v.WebpageURL
	}
	return "https://www.youtube.com/watch?v=" + v.ID
}

// Check confirms yt-dlp is installed and returns its version.
func Check(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, Binary, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH (brew install yt-dlp): %w", Binary, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Info fetches full metadata for a single video.
func Info(ctx context.Context, url string) (*Video, error) {
	out, err := run(ctx, "--no-warnings", "--skip-download", "--dump-single-json", url)
	if err != nil {
		return nil, err
	}
	var v Video
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, fmt.Errorf("parsing yt-dlp metadata: %w", err)
	}
	if v.ID == "" {
		return nil, fmt.Errorf("yt-dlp returned no video id for %s", url)
	}
	return &v, nil
}

// List enumerates a channel's uploads without fetching each one's metadata.
// Pass limit 0 for the whole catalogue.
func List(ctx context.Context, channelURL string, limit int) ([]Video, error) {
	args := []string{"--no-warnings", "--flat-playlist", "--dump-json"}
	if limit > 0 {
		args = append(args, "--playlist-end", fmt.Sprint(limit))
	}
	out, err := run(ctx, append(args, videosTab(channelURL))...)
	if err != nil {
		return nil, err
	}

	var videos []Video
	for _, line := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var v Video
		if err := json.Unmarshal(line, &v); err != nil {
			continue // a playlist entry we cannot parse is not fatal
		}
		if v.ID != "" {
			videos = append(videos, v)
		}
	}
	return videos, nil
}

// Captions downloads the caption track into dir and returns the .vtt path.
// It prefers a real caption track and falls back to the auto-generated one.
func Captions(ctx context.Context, url, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	_, err := run(ctx,
		"--no-warnings", "--skip-download",
		"--write-subs", "--write-auto-subs",
		"--sub-langs", "en.*,en",
		"--sub-format", "vtt/best",
		"--convert-subs", "vtt",
		"-o", filepath.Join(dir, "%(id)s.%(ext)s"),
		url,
	)
	if err != nil {
		return "", err
	}
	path, err := pickTrack(dir)
	if err != nil {
		return "", fmt.Errorf("no caption track available for %s", url)
	}
	return path, nil
}

// pickTrack chooses among the .vtt files in dir.
func pickTrack(dir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.vtt"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", os.ErrNotExist
	}
	// Prefer a manually written track: yt-dlp names auto captions with an
	// "-orig" or "a.en" style suffix, and those sort after the plain one.
	sort.Slice(matches, func(i, j int) bool { return len(matches[i]) < len(matches[j]) })
	return matches[0], nil
}

// DownloadAudio fetches the best audio stream and writes m4a to outPath.
func DownloadAudio(ctx context.Context, url, outPath string) error {
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	_, err := run(ctx,
		"--no-warnings",
		"-f", "bestaudio/best",
		"-x", "--audio-format", "m4a", "--audio-quality", "0",
		"-o", outPath,
		url,
	)
	return err
}

func run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, Binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("yt-dlp: %.500s", msg)
	}
	return stdout.Bytes(), nil
}

// videosTab points a bare channel URL at its uploads listing.
func videosTab(u string) string {
	u = strings.TrimRight(u, "/")
	for _, tab := range []string{"/videos", "/streams", "/shorts", "/playlist", "watch?v="} {
		if strings.Contains(u, tab) {
			return u
		}
	}
	return u + "/videos"
}
