// Package pipeline wires the individual packages (detect, render, storage,
// feed) into the two end-to-end operations every command needs: turning a
// video into a locally sheared file, and turning that file into a
// published podcast episode. render, publish and sync all share this code
// rather than each reimplementing it.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/detect"
	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/fileutil"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/render"
	"github.com/cwebley/shearcast/internal/segment"
	"github.com/cwebley/shearcast/internal/storage"
	"github.com/cwebley/shearcast/internal/transcript"
	"github.com/cwebley/shearcast/internal/usage"
	"github.com/cwebley/shearcast/internal/youtube"
)

// RenderOptions tunes the cutting step. Zero values take render's own defaults.
type RenderOptions struct {
	SnapWindow float64
	NoSnap     bool
	Crossfade  float64
	MinKeep    float64
	OutPath    string // default: <cacheDir>/renders/<channel>/<video-id>/render.m4a
	RenderID   string // lifecycle attempt identifier, copied into the durable record
}

// minTail is the shortest uncaptioned ending worth trimming. Anything shorter
// is a breath or a sting that a cut would only make more abrupt.
const minTail = 5.0

// Progress receives one line per notable step, so a CLI can print it to
// stderr; pass nil to run silently (as sync does across many videos).
type Progress func(string)

// RenderEpisode checks for usable cues and downloads and probes source audio
// before running detection. It then snaps boundaries to silence and writes a cut.
func RenderEpisode(
	ctx context.Context,
	cfg *config.Config,
	channel config.Channel,
	cache youtube.Cache,
	client *jev.Client,
	video *youtube.Video,
	cues []transcript.Cue,
	opts RenderOptions,
	progress Progress,
) (outPath string, result *detect.Result, keep []render.Range, err error) {
	say := progress
	if say == nil {
		say = func(string) {}
	}

	detectOpts := cfg.DetectOptions(channel.Rules)
	detectOpts.Subject = video.Title
	if !channel.NoWeights && cfg.Jev.Weights != "" {
		w, err := detect.LoadWeights(cfg.Jev.Weights)
		if err != nil {
			return "", nil, nil, fmt.Errorf("loading weights: %w", err)
		}
		detectOpts.StartWeights = w
	}
	if !channel.NoWeights && cfg.Jev.EndWeights != "" {
		w, err := detect.LoadWeights(cfg.Jev.EndWeights)
		if err != nil {
			return "", nil, nil, fmt.Errorf("loading end weights: %w", err)
		}
		detectOpts.EndWeights = w
	}

	if !channel.NoWeights && cfg.Jev.Weights == "" {
		say("no fitted weights found; the opening edge uses the plain predicate")
	}

	detector := detect.New(client, detectOpts)
	detectOpts = detector.Opts
	if !slices.ContainsFunc(cues, func(c transcript.Cue) bool { return strings.TrimSpace(c.Text) != "" }) {
		return "", nil, nil, fmt.Errorf("detection: no usable caption windows")
	}

	say("fetching full audio...")
	fetchStart := time.Now()
	audioPath, err := cache.Audio(ctx, video, progress)
	if err != nil {
		return "", nil, nil, fmt.Errorf("fetching audio: %w", err)
	}
	if info, err := os.Stat(audioPath); err == nil {
		say(fmt.Sprintf("audio ready: %.1f MB in %s", float64(info.Size())/1e6, time.Since(fetchStart).Round(100*time.Millisecond)))
	}
	sourceDuration, err := render.Probe(ctx, audioPath)
	if err == nil && sourceDuration <= 0 {
		err = fmt.Errorf("%w: duration must be positive (got %s)", render.ErrUnreadableMedia, sourceDuration)
	}
	if err != nil {
		// A bad download would otherwise be a cache hit on every retry.
		if errors.Is(err, render.ErrUnreadableMedia) {
			err = errors.Join(err, cache.RemoveAudio(video.ID))
		}
		return "", nil, nil, fmt.Errorf("probing source audio duration: %w", err)
	}

	detected := Progress(say).start(fmt.Sprintf("running detection passes against %s", cfg.Jev.Model))
	before := client.Stats()
	res, err := detector.Run(ctx, cues, video.Duration)
	detected(err)
	if err != nil {
		return "", nil, nil, fmt.Errorf("detection: %w", err)
	}
	if len(res.Windows) == 0 {
		return "", nil, nil, fmt.Errorf("detection: no usable caption windows")
	}
	res.Stats = client.Stats().Since(before)
	say(fmt.Sprintf("%d region(s) found", len(res.Regions)))

	segs := append([]segment.Segment(nil), res.Segments...)
	// Trim what plays after the last words: end cards, closing music, credits
	// shown on screen. None of it is in the captions, so detection cannot see
	// it. Channels whose closing music is wanted set keep_tail.
	var tailFrom float64
	if !channel.KeepTail {
		if end := transcript.SpeechEnd(cues); end > 0 && sourceDuration.Seconds()-end >= minTail {
			tailFrom = end
			segs = append(segs, segment.Segment{Start: end, End: sourceDuration.Seconds(), Rule: "tail", Source: "captions"})
			say(fmt.Sprintf("trimming %.0fs after the last spoken caption", sourceDuration.Seconds()-end))
		}
	}
	snapOpts := render.DefaultSnapOptions()
	if opts.SnapWindow > 0 {
		snapOpts.Window = opts.SnapWindow
	}
	if !opts.NoSnap {
		snapped := Progress(say).start("snapping boundaries to real silence")
		for i := range segs {
			start, err := render.Snap(ctx, audioPath, segs[i].Start, snapOpts)
			if err != nil {
				snapped(err)
				return "", nil, nil, fmt.Errorf("snapping start of region %d: %w", i+1, err)
			}
			end := segs[i].End
			// A cut running to the end of the file has no join to snap.
			if end < sourceDuration.Seconds() {
				if end, err = render.Snap(ctx, audioPath, end, snapOpts); err != nil {
					snapped(err)
					return "", nil, nil, fmt.Errorf("snapping end of region %d: %w", i+1, err)
				}
			}
			segs[i].Start, segs[i].End = start, end
		}
		snapped(nil)
	}

	minKeep := opts.MinKeep
	if minKeep == 0 {
		minKeep = 1.0
	}
	// Padding is deliberately skipped: Pad exists to compensate for an
	// imprecise boundary, and Snap already replaces the imprecise boundary
	// with a real measurement.
	_, segKeep := segment.Prepare(segs, sourceDuration.Seconds(), 0, 0, detectOpts.SegmentMergeGap, minKeep)
	if len(segKeep) == 0 {
		return "", nil, nil, fmt.Errorf("nothing survived to keep (check MinKeep and the detected regions)")
	}
	renderKeep := make([]render.Range, len(segKeep))
	for i, r := range segKeep {
		renderKeep[i] = render.Range{Start: r.Start, End: r.End}
	}

	path := opts.OutPath
	if path == "" {
		path = RenderPath(cache, channel, video.ID)
	}
	if sameFile(path, audioPath) {
		return "", nil, nil, fmt.Errorf("rendered output aliases source audio")
	}
	if err := fileutil.MkdirAll(filepath.Dir(path)); err != nil {
		return "", nil, nil, err
	}
	stage, err := os.CreateTemp(filepath.Dir(path), stagePrefix(path)+"*.m4a")
	if err != nil {
		return "", nil, nil, err
	}
	stagePath := stage.Name()
	retainStage := false
	defer func() {
		if !retainStage {
			os.Remove(stagePath)
		}
	}()
	if err := stage.Close(); err != nil {
		return "", nil, nil, err
	}

	crossfade := opts.Crossfade
	if crossfade == 0 {
		crossfade = render.DefaultCutOptions().Crossfade
	}
	cutOpts := render.CutOptions{Crossfade: render.EffectiveCrossfade(crossfade), BitrateKbps: cfg.OutputBitrate(channel)}
	encoded := Progress(say).start(fmt.Sprintf("encoding AAC at %d kbps", cutOpts.BitrateKbps))
	cutErr := render.Cut(ctx, audioPath, renderKeep, stagePath, cutOpts)
	encoded(cutErr)
	if err := cutErr; err != nil {
		return "", nil, nil, fmt.Errorf("cutting: %w", err)
	}
	duration, err := render.Probe(ctx, stagePath)
	if err != nil || duration <= 0 {
		return "", nil, nil, fmt.Errorf("verifying rendered audio (duration %s): %v", duration, err)
	}
	hash, size, err := audioDigest(stagePath)
	if err != nil {
		return "", nil, nil, err
	}
	audio, err := os.OpenFile(stagePath, os.O_RDWR, 0)
	if err != nil {
		return "", nil, nil, err
	}
	err = audio.Sync()
	closeErr := audio.Close()
	if err != nil || closeErr != nil {
		return "", nil, nil, errors.Join(err, closeErr)
	}
	record := RenderRecord{
		Version: 2, VideoID: video.ID, Channel: channel.Slug, CreatedAt: time.Now().UTC(),
		Codec: "aac", Cut: cutOpts, Snap: snapOpts,
		NoSnap: opts.NoSnap, MinKeep: minKeep, Model: cfg.Jev.Model,
		DetectionOptions: detectOpts, Detection: res, Keep: renderKeep,
		DurationSeconds:       duration.Seconds(),
		SourceDurationSeconds: sourceDuration.Seconds(),
		Source:                video, AudioSHA256: hash, AudioBytes: size,
		RenderID:     opts.RenderID,
		Usage:        &res.Stats,
		TailTrimFrom: tailFrom,
	}
	retainStage = true // journal or directory-sync failure must leave recoverable audio
	if err := installRender(path, stagePath, record); err != nil {
		return "", nil, nil, fmt.Errorf("installing render: %w", err)
	}
	return path, res, renderKeep, nil
}

func RenderPath(cache youtube.Cache, channel config.Channel, id string) string {
	return filepath.Join(cache.Dir, "renders", channel.Slug, id, "render.m4a")
}

func audioDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), n, nil
}

// RenderRecord describes the settings and timeline used for an output file.
// It lives beside the audio as <audio-path>.json, independently of current config.
type RenderRecord struct {
	SourceDurationSeconds float64           `json:"source_duration_seconds,omitempty"`
	Version               int               `json:"version"`
	VideoID               string            `json:"video_id"`
	Channel               string            `json:"channel"`
	CreatedAt             time.Time         `json:"created_at"`
	Codec                 string            `json:"codec"`
	Cut                   render.CutOptions `json:"cut"`
	// TailTrimFrom is where the trimmed tail began in source seconds, or zero
	// when nothing after the last spoken caption was cut.
	TailTrimFrom     float64            `json:"tail_trim_from,omitempty"`
	Snap             render.SnapOptions `json:"snap"`
	NoSnap           bool               `json:"no_snap"`
	MinKeep          float64            `json:"min_keep"`
	Model            string             `json:"model"`
	DetectionOptions detect.Options     `json:"detection_options"`
	Detection        *detect.Result     `json:"detection"`
	Keep             []render.Range     `json:"keep"`
	DurationSeconds  float64            `json:"duration_seconds"`
	Source           *youtube.Video     `json:"source,omitempty"`
	AudioSHA256      string             `json:"audio_sha256,omitempty"`
	AudioBytes       int64              `json:"audio_bytes,omitempty"`
	RenderID         string             `json:"render_id,omitempty"`
	Usage            *usage.Summary     `json:"usage,omitempty"`
}

func writeRenderRecord(path string, record RenderRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteAtomic(path, data, 0o600)
}

// Publisher is the object-storage seam used by publication and metadata refresh.
type Publisher interface {
	Put(context.Context, string, io.Reader, int64, string) (string, error)
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error // idempotent, including missing objects
	PublicURL(string) string
}

// PublishResult is what publishing one episode produced.
type PublishResult struct {
	AudioKey string
	AudioURL string
	FeedURL  string
}

// PublishEpisode uploads a rendered audio file to storage and upserts it into
// the channel's feed, creating the feed if this is its first episode.
func PublishEpisode(
	ctx context.Context,
	store Publisher,
	channel config.Channel,
	video *youtube.Video,
	audioPath string,
) (*PublishResult, error) {
	return publishEpisode(ctx, store, channel, video, audioPath, nil, nil, channel.Slug+"/"+video.ID+".m4a")
}

func publishEpisode(ctx context.Context, store Publisher, channel config.Channel, video *youtube.Video, audioPath string, order map[string]int, metadata *chapterMetadata, audioKey string) (*PublishResult, error) {
	info, err := os.Stat(audioPath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", audioPath, err)
	}
	duration, err := render.Probe(ctx, audioPath)
	if err != nil {
		return nil, fmt.Errorf("probing duration: %w", err)
	}

	f, err := os.Open(audioPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	audioURL, err := store.Put(ctx, audioKey, f, info.Size(), "audio/x-m4a")
	if err != nil {
		return nil, fmt.Errorf("uploading audio: %w", err)
	}

	feedKey := channel.Slug + "/feed.xml"
	fd, err := loadOrCreateFeed(ctx, store, channel, feedKey)
	if err != nil {
		return nil, err
	}
	// Only when neither config nor the channel's avatar supplied artwork does
	// a feed borrow its first episode's thumbnail, and it keeps that one: a
	// show's icon should stay stable, not flip with each new episode.
	if fd.ImageURL == "" {
		fd.ImageURL = video.Thumbnail
	}
	publishedAt := publicationDate(video, time.Time{})
	for _, item := range fd.Items {
		if item.ID == video.ID {
			publishedAt = publicationDate(video, item.PublishedAt)
			break
		}
	}
	item := feed.Item{
		ID: video.ID,
		// YouTube descriptions occasionally contain literal HTML entities
		// (e.g. "&amp;" as text, from a pasted URL) rather than the raw
		// characters they represent. Decoding them here means our own XML
		// encoder escapes each one exactly once; skipping this step would
		// double-escape them into "&amp;amp;" and a listener would see the
		// literal text "&amp;" instead of "&".
		Title:       html.UnescapeString(video.Title),
		Description: html.UnescapeString(video.Description),
		PublishedAt: publishedAt,
		AudioURL:    audioURL,
		AudioBytes:  info.Size(),
		Duration:    duration,
		ImageURL:    video.Thumbnail,
	}
	if metadata != nil {
		item.PublicationID = metadata.publicationID
		item.Description, item.Chapters, item.ChaptersURL = metadata.description, metadata.list, metadata.url
	}
	fd.Upsert(item)
	feed.OrderItems(fd.Items, order)

	xmlBytes, err := fd.XML()
	if err != nil {
		return nil, err
	}
	feedURL, err := store.Put(ctx, feedKey, strings.NewReader(string(xmlBytes)), int64(len(xmlBytes)), "application/rss+xml")
	if err != nil {
		return nil, fmt.Errorf("uploading feed: %w", err)
	}

	return &PublishResult{AudioKey: audioKey, AudioURL: audioURL, FeedURL: feedURL}, nil
}

// loadOrCreateFeed fetches the channel's existing feed, or starts a new one
// if it has no episodes yet.
//
// Title, Description, Category and SelfURL are always overwritten from the
// current config and store, whether or not a feed already existed: they are
// fully config-determined, so an edit to config.toml takes effect on the
// next publish rather than requiring the feed to be deleted first. ImageURL
// is overwritten too when the channel has an image, set in config or found
// by sync. Items have no config source, so whatever Parse recovered is left
// alone and only added to.
func loadOrCreateFeed(ctx context.Context, store Publisher, channel config.Channel, feedKey string) (*feed.Feed, error) {
	fd := &feed.Feed{}
	data, err := store.Get(ctx, feedKey)
	switch {
	case err == nil:
		fd, err = feed.Parse(data)
		if err != nil {
			return nil, err
		}
	case errors.Is(err, storage.ErrNotFound):
		// fd stays zero-valued: no items, no image yet.
	default:
		return nil, fmt.Errorf("fetching existing feed: %w", err)
	}
	fd.Title = channel.FeedTitle()
	fd.Description = channel.FeedDescription()
	fd.Category = channel.FeedCategory()
	fd.SelfURL = store.PublicURL(feedKey)
	if channel.Image != "" {
		fd.ImageURL = channel.Image
	}
	return fd, nil
}

// RefreshPublishedEpisode changes metadata only. Enclosures, GUIDs and edited
// durations come from the existing feed, so neither source audio nor detection
// is needed. A missing entry is an error, never permission to rerender history.
func RefreshPublishedEpisode(ctx context.Context, store Publisher, channel config.Channel, video *youtube.Video) (*PublishResult, error) {
	return refreshPublishedEpisode(ctx, store, channel, video, nil)
}

func refreshPublishedEpisode(ctx context.Context, store Publisher, channel config.Channel, video *youtube.Video, metadata *chapterMetadata) (*PublishResult, error) {
	key := channel.Slug + "/feed.xml"
	fd, err := loadOrCreateFeed(ctx, store, channel, key)
	if err != nil {
		return nil, err
	}
	for _, item := range fd.Items {
		if item.ID != video.ID {
			continue
		}
		item.Title = html.UnescapeString(video.Title)
		item.Description = html.UnescapeString(video.Description)
		if metadata != nil {
			item.Description, item.Chapters, item.ChaptersURL = metadata.description, metadata.list, metadata.url
		}
		item.ImageURL = video.Thumbnail
		item.PublishedAt = publicationDate(video, item.PublishedAt)
		if fd.ImageURL == "" {
			fd.ImageURL = video.Thumbnail
		}
		fd.Upsert(item)
		data, err := fd.XML()
		if err != nil {
			return nil, err
		}
		url, err := store.Put(ctx, key, strings.NewReader(string(data)), int64(len(data)), "application/rss+xml")
		if err != nil {
			return nil, err
		}
		return &PublishResult{AudioURL: item.AudioURL, FeedURL: url}, nil
	}
	return nil, fmt.Errorf("published episode %s is missing from %s; explicitly publish its audio to restore it", video.ID, key)
}

// parseUploadDate parses yt-dlp's YYYYMMDD upload_date. An unparsable or
// empty date falls back to now, since a feed still needs some publish time
// for apps that sort by it.
func parseUploadDate(s string) time.Time {
	if t, err := time.Parse("20060102", s); err == nil {
		return t
	}
	return time.Now()
}
