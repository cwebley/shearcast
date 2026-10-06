// Package youtube shells out to yt-dlp.
//
// A pure-Go extractor would break every time YouTube changes its player.
// yt-dlp has maintainers for that fight, so this package only builds argument
// lists and parses JSON.
package youtube

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/cwebley/shearcast/internal/chapters"
	"github.com/cwebley/shearcast/internal/fileutil"
	"github.com/cwebley/shearcast/internal/worklimit"
)

// Binary is the yt-dlp executable; override for tests or a pinned install.
var Binary = "yt-dlp"

// Video is the metadata we need about one upload.
type Video struct {
	Chapters    []chapters.Source `json:"chapters"`
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Channel     string            `json:"channel"`
	ChannelID   string            `json:"channel_id"`
	Duration    float64           `json:"duration"`
	UploadDate  string            `json:"upload_date"`         // YYYYMMDD
	Timestamp   int64             `json:"timestamp,omitempty"` // source publication time, when available
	Thumbnail   string            `json:"thumbnail"`
	WebpageURL  string            `json:"webpage_url"`
	// Availability and LiveStatus are yt-dlp's access and broadcast fields.
	// Flat listings leave Availability null for public videos.
	Availability string `json:"availability,omitempty"`
	LiveStatus   string `json:"live_status,omitempty"`
}

// Unavailable names why the video cannot be processed yet (members-only,
// private, upcoming...), or returns "" when nothing is known against it.
// These states can change, so callers skip such videos rather than record them.
func (v Video) Unavailable() string {
	switch v.Availability {
	case "subscriber_only":
		return "members-only"
	case "premium_only":
		return "premium-only"
	case "private":
		return "private"
	case "needs_auth":
		return "sign-in required"
	}
	switch v.LiveStatus {
	case "is_upcoming":
		return "upcoming"
	case "is_live":
		return "live now"
	}
	return ""
}

// ErrUnavailable matches an UnavailableError: yt-dlp refused the video
// because of who may watch it or when, not because of a transport failure.
var ErrUnavailable = errors.New("video is not available")

// UnavailableError carries the reason, in Video.Unavailable's vocabulary.
type UnavailableError struct {
	Reason string
	Err    error
}

func (e *UnavailableError) Error() string        { return e.Reason + " video: " + e.Err.Error() }
func (e *UnavailableError) Unwrap() error        { return e.Err }
func (e *UnavailableError) Is(target error) bool { return target == ErrUnavailable }

// unavailableMarkers maps yt-dlp's refusal messages to a reason. Bot checks
// and rate limits are deliberately absent: those are transient failures.
var unavailableMarkers = []struct{ text, reason string }{
	{"members-only", "members-only"},
	{"available to this channel's members", "members-only"},
	{"private video", "private"},
	{"premieres in", "upcoming"},
	{"this live event will begin", "upcoming"},
}

func classifyInfoError(err error) error {
	msg := strings.ToLower(err.Error())
	for _, m := range unavailableMarkers {
		if strings.Contains(msg, m.text) {
			return &UnavailableError{Reason: m.reason, Err: err}
		}
	}
	return err
}

// Clone keeps cached/state metadata independent of the caller's chapter slice.
func (v *Video) Clone() *Video {
	if v == nil {
		return nil
	}
	c := *v
	c.Chapters = chapters.CloneSource(v.Chapters)
	return &c
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

// Info fetches full metadata for a single video, given its id or any URL
// VideoID accepts. The request always goes to the canonical watch URL, so a
// pasted URL's playlist or other parameters cannot widen it, and the result
// must be the video asked for.
func Info(ctx context.Context, target string) (*Video, error) {
	id := VideoID(target)
	if id == "" {
		return nil, fmt.Errorf("need a YouTube video URL or eleven-character id, got %q", target)
	}
	url := (&Video{ID: id}).URL()
	out, err := run(ctx, "--no-warnings", "--no-playlist", "--skip-download", "--dump-single-json", url)
	if err != nil {
		return nil, classifyInfoError(err)
	}
	var v Video
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, fmt.Errorf("parsing yt-dlp metadata: %w", err)
	}
	if v.ID != id {
		return nil, fmt.Errorf("yt-dlp returned video id %q for %s", v.ID, id)
	}
	return &v, nil
}

// List enumerates a channel's uploads without fetching each one's metadata.
// A positive finite limit is required. It returns entries, newest first, up to
// and including the limit-th one without an Unavailable reason, so skipped
// uploads do not use up the window; it may return fewer accessible entries
// when more than listSlack of them are skipped.
func List(ctx context.Context, channelURL string, limit int) ([]Video, error) {
	return list(ctx, channelURL, limit, nil)
}

// InspectUploads is a diagnostic listing. Ignore yt-dlp configuration that can
// enable cookie/page writes, and disable its disk cache. It may differ from a
// sync that relies on a user's proxy, cookies or other yt-dlp configuration.
func InspectUploads(ctx context.Context, channelURL string, limit int) ([]Video, error) {
	return list(ctx, channelURL, limit, []string{"--ignore-config", "--no-cache-dir"})
}

// ChannelArtwork returns the full-size avatar of the channel behind a channel
// or playlist URL: the show's own logo, unlike any one upload's thumbnail. A
// playlist carries no avatar of its own, so its owning channel is looked up.
func ChannelArtwork(ctx context.Context, sourceURL string) (string, error) {
	avatar, owner, err := channelListing(ctx, sourceURL)
	if err == nil && avatar == "" && owner != "" && owner != sourceURL {
		avatar, _, err = channelListing(ctx, owner)
	}
	if err != nil {
		return "", err
	}
	if avatar == "" {
		return "", fmt.Errorf("no channel avatar found for %s", sourceURL)
	}
	return avatar, nil
}

// channelListing reads a listing's own metadata, without any entries: its
// avatar, if it has one, and the URL of the channel that owns it.
func channelListing(ctx context.Context, url string) (avatar, owner string, err error) {
	out, err := run(ctx, "--no-warnings", "--flat-playlist", "--playlist-items", "0", "--dump-single-json", url)
	if err != nil {
		return "", "", err
	}
	var listing struct {
		ChannelURL string `json:"channel_url"`
		Thumbnails []struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"thumbnails"`
	}
	if err := json.Unmarshal(out, &listing); err != nil {
		return "", "", fmt.Errorf("parsing yt-dlp channel metadata: %w", err)
	}
	for _, t := range listing.Thumbnails {
		if t.ID == "avatar_uncropped" {
			avatar = t.URL
		}
	}
	return avatar, listing.ChannelURL, nil
}

// listSlack is how many skipped uploads a listing can step past. Flat listing
// fetches whole pages, so the extra entries cost little.
const listSlack = 10

func list(ctx context.Context, channelURL string, limit int, options []string) ([]Video, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("upload selection limit must be positive")
	}
	args := []string{"--no-warnings", "--flat-playlist", "--dump-json"}
	args = append(args, options...)
	args = append(args, "--playlist-end", fmt.Sprint(limit+listSlack))
	out, err := run(ctx, append(args, videosTab(channelURL))...)
	if err != nil {
		return nil, err
	}

	var videos []Video
	accessible := 0
	for _, line := range bytes.Split(out, []byte("\n")) {
		if accessible == limit {
			break
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var v Video
		if err := json.Unmarshal(line, &v); err != nil {
			continue // a playlist entry we cannot parse is not fatal
		}
		if v.ID != "" {
			videos = append(videos, v)
			if v.Unavailable() == "" {
				accessible++
			}
		}
	}
	return videos, nil
}

// Captions downloads the caption track into dir and returns the .vtt path.
// It prefers a real caption track and falls back to the auto-generated one.
func Captions(ctx context.Context, url, dir string) (string, error) {
	if err := fileutil.MkdirAll(dir); err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(dir, ".captions-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	// YouTube rate-limits caption requests, and on an auto-captioned video the
	// "en" track is a translated rendition of "en-orig" that is refused
	// (HTTP 429) while "en-orig" still downloads. yt-dlp cannot ask for a
	// manual "en" without falling back to that auto "en", so the creator's
	// track and the original auto captions are separate requests, each for
	// one track. See docs/source-acquisition-issues.md.
	//
	// Each request names its mode and disables the other, so a yt-dlp config
	// enabling --write-auto-subs cannot pull the refused auto "en" into the
	// first request.
	fetch := func(kind, lang string) error {
		other := "--no-write-auto-subs"
		if kind == "--write-auto-subs" {
			other = "--no-write-subs"
		}
		_, err := run(ctx,
			"--no-warnings", "--no-playlist", "--skip-download",
			kind, other, "--sub-langs", lang,
			"--sub-format", "vtt/best",
			"--convert-subs", "vtt",
			"-o", filepath.Join(work, "%(id)s.%(ext)s"),
			url,
		)
		return err
	}
	if err := fetch("--write-subs", "en"); err != nil {
		return "", err
	}
	if _, err := pickTrack(work); errors.Is(err, os.ErrNotExist) {
		if err := fetch("--write-auto-subs", "en-orig"); err != nil {
			return "", err
		}
	}
	path, err := pickTrack(work)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w for %s", ErrCaptionsUnavailable, url)
		}
		return "", err
	}
	final := filepath.Join(dir, filepath.Base(path))
	if err := os.Rename(path, final); err != nil {
		return "", err
	}
	return final, nil
}

// ErrCaptionsUnavailable means yt-dlp succeeded but provided no usable track.
// Transport, rate-limit and parsing failures remain distinct errors.
var ErrCaptionsUnavailable = errors.New("no English caption track available")

// pickTrack chooses among the .vtt files in dir.
func pickTrack(dir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.vtt"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", os.ErrNotExist
	}
	// Captions fetches one track at a time, so this only matters when a
	// directory holds several: prefer the plain name, since auto captions
	// carry an "-orig" style suffix.
	sort.Slice(matches, func(i, j int) bool { return len(matches[i]) < len(matches[j]) })
	return matches[0], nil
}

// audioFormat prefers YouTube's native AAC stream. The best stream is usually
// Opus, which "-x --audio-format m4a" then re-encodes in full before the render
// re-encodes it again: measured 2026-09-24 on a 15-minute episode, 13.7s and a
// 33 MB file against 3.9s and 15 MB for the native AAC stream. Opus stays as a
// fallback for videos with no AAC stream.
const audioFormat = "bestaudio[ext=m4a]/bestaudio/best"

// progressMarker tags the progress lines yt-dlp prints for us, so they can be
// told apart from anything else on stdout.
const progressMarker = "shearcast-progress"

// DownloadAudio fetches the source audio and writes m4a to outPath. progress,
// when non-nil, receives a line at every tenth of the download.
func DownloadAudio(ctx context.Context, url, outPath string, progress func(string)) error {
	if err := fileutil.MkdirAll(filepath.Dir(outPath)); err != nil {
		return err
	}
	work, err := os.MkdirTemp(filepath.Dir(outPath), ".download-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	staged := filepath.Join(work, "audio.m4a")
	args := []string{
		"--no-warnings", "--no-playlist",
		"-f", audioFormat,
		"-x", "--audio-format", "m4a", "--audio-quality", "0",
	}
	if progress != nil {
		args = append(args, "--quiet", "--progress", "--newline", "--progress-template",
			"download:"+progressMarker+" %(progress.downloaded_bytes)s %(progress.total_bytes)s %(progress.total_bytes_estimate)s")
	}
	args = append(args, "-o", staged, url)
	if progress == nil {
		_, err = run(ctx, args...)
	} else {
		err = runWithProgress(ctx, args, downloadReporter(progress))
	}
	if err != nil {
		return err
	}
	return os.Rename(staged, outPath)
}

// downloadReporter turns yt-dlp's progress lines into one message per tenth of
// the file, so a long download shows movement without flooding the terminal.
func downloadReporter(progress func(string)) func(string) {
	next := 10
	return func(line string) {
		fields := strings.Fields(strings.TrimPrefix(line, progressMarker))
		if len(fields) != 3 {
			return
		}
		done, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return
		}
		total, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || total <= 0 {
			if total, err = strconv.ParseFloat(fields[2], 64); err != nil || total <= 0 {
				return
			}
		}
		pct := int(100 * done / total)
		if pct < next {
			return
		}
		next = pct/10*10 + 10
		progress(fmt.Sprintf("downloading audio: %d%% of %.1f MB", min(pct, 100), total/1e6))
	}
}

// runWithProgress runs yt-dlp, handing each stdout line carrying the progress
// marker to onLine as it arrives. Stderr is kept for the error message.
func runWithProgress(ctx context.Context, args []string, onLine func(string)) error {
	release, err := worklimit.Acquire(ctx, worklimit.YouTube)
	if err != nil {
		return err
	}
	defer release()
	cmd := exec.CommandContext(ctx, Binary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("yt-dlp: %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); strings.HasPrefix(line, progressMarker) {
			onLine(line)
		}
	}
	// A line too long to scan (say, metadata a user's config prints) stops
	// the scanner, and only progress reporting is lost. Keep draining so
	// yt-dlp never blocks on a full pipe.
	io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("yt-dlp: %.500s", msg)
	}
	return nil
}

func run(ctx context.Context, args ...string) ([]byte, error) {
	release, err := worklimit.Acquire(ctx, worklimit.YouTube)
	if err != nil {
		return nil, err
	}
	defer release()
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
