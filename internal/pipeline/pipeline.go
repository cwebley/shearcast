// Package pipeline wires the individual packages (detect, render, storage,
// feed) into the two end-to-end operations every command needs: turning a
// video into a de-sponsored local file, and turning that file into a
// published podcast episode. render, publish and sync all share this code
// rather than each reimplementing it.
package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/detect"
	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/render"
	"github.com/cwebley/shearcast/internal/segment"
	"github.com/cwebley/shearcast/internal/storage"
	"github.com/cwebley/shearcast/internal/transcript"
	"github.com/cwebley/shearcast/internal/youtube"
)

// RenderOptions tunes the cutting step. Zero values take render's own defaults.
type RenderOptions struct {
	SnapWindow float64
	NoSnap     bool
	Crossfade  float64
	MinKeep    float64
	OutPath    string // default: <cacheDir>/<video-id>/render.m4a
}

// Progress receives one line per notable step, so a CLI can print it to
// stderr; pass nil to run silently (as sync does across many videos).
type Progress func(string)

// RenderEpisode runs detection against cues, snaps each boundary to real
// silence in the full downloaded audio, and writes a de-sponsored cut.
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

	say(fmt.Sprintf("running detection passes against %s...", cfg.Jev.Model))
	res, err := detect.New(client, detectOpts).Run(ctx, cues, video.Duration)
	if err != nil {
		return "", nil, nil, fmt.Errorf("detection: %w", err)
	}
	say(fmt.Sprintf("%d region(s) found", len(res.Regions)))
	if len(res.Segments) == 0 {
		return "", res, nil, fmt.Errorf("nothing to cut")
	}

	say("fetching full audio (cached after the first run)...")
	audioPath, err := cache.Audio(ctx, video)
	if err != nil {
		return "", nil, nil, fmt.Errorf("fetching audio: %w", err)
	}

	segs := append([]segment.Segment(nil), res.Segments...)
	snapWindow := opts.SnapWindow
	if snapWindow == 0 {
		snapWindow = render.DefaultSnapOptions().Window
	}
	if !opts.NoSnap {
		say("snapping boundaries to real silence...")
		snapOpts := render.DefaultSnapOptions()
		snapOpts.Window = snapWindow
		for i := range segs {
			start, err := render.Snap(ctx, audioPath, segs[i].Start, snapOpts)
			if err != nil {
				return "", nil, nil, fmt.Errorf("snapping start of region %d: %w", i+1, err)
			}
			end, err := render.Snap(ctx, audioPath, segs[i].End, snapOpts)
			if err != nil {
				return "", nil, nil, fmt.Errorf("snapping end of region %d: %w", i+1, err)
			}
			segs[i].Start, segs[i].End = start, end
		}
	}

	minKeep := opts.MinKeep
	if minKeep == 0 {
		minKeep = 1.0
	}
	// Padding is deliberately skipped: Pad exists to compensate for an
	// imprecise boundary, and Snap already replaces the imprecise boundary
	// with a real measurement.
	_, segKeep := segment.Prepare(segs, video.Duration, 0, 0, detectOpts.SegmentMergeGap, minKeep)
	if len(segKeep) == 0 {
		return "", nil, nil, fmt.Errorf("nothing survived to keep (check MinKeep and the detected regions)")
	}
	renderKeep := make([]render.Range, len(segKeep))
	for i, r := range segKeep {
		renderKeep[i] = render.Range{Start: r.Start, End: r.End}
	}

	path := opts.OutPath
	if path == "" {
		path = filepath.Join(cache.Dir, video.ID, "render.m4a")
	}

	crossfade := opts.Crossfade
	if crossfade == 0 {
		crossfade = render.DefaultCutOptions().Crossfade
	}
	say("cutting...")
	if err := render.Cut(ctx, audioPath, renderKeep, path, render.CutOptions{Crossfade: crossfade}); err != nil {
		return "", nil, nil, fmt.Errorf("cutting: %w", err)
	}
	return path, res, renderKeep, nil
}

// PublishResult is what publishing one episode produced.
type PublishResult struct {
	AudioURL string
	FeedURL  string
}

// PublishEpisode uploads a rendered audio file to storage and upserts it into
// the channel's feed, creating the feed if this is its first episode.
func PublishEpisode(
	ctx context.Context,
	store *storage.Store,
	channel config.Channel,
	video *youtube.Video,
	audioPath string,
) (*PublishResult, error) {
	info, err := os.Stat(audioPath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", audioPath, err)
	}
	duration, err := render.Probe(ctx, audioPath)
	if err != nil {
		return nil, fmt.Errorf("probing duration: %w", err)
	}

	audioKey := channel.Slug + "/" + video.ID + ".m4a"
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
	fd.Upsert(feed.Item{
		ID:          video.ID,
		Title:       video.Title,
		Description: video.Description,
		PublishedAt: parseUploadDate(video.UploadDate),
		AudioURL:    audioURL,
		AudioBytes:  info.Size(),
		Duration:    duration,
	})

	xmlBytes, err := fd.XML()
	if err != nil {
		return nil, err
	}
	feedURL, err := store.Put(ctx, feedKey, strings.NewReader(string(xmlBytes)), int64(len(xmlBytes)), "application/rss+xml")
	if err != nil {
		return nil, fmt.Errorf("uploading feed: %w", err)
	}

	return &PublishResult{AudioURL: audioURL, FeedURL: feedURL}, nil
}

// loadOrCreateFeed fetches the channel's existing feed, or starts a new one
// if it has no episodes yet. SelfURL is always set fresh from the store
// rather than trusted from a parsed file: it is fully determined by the
// channel's storage key, so recomputing it here is both simpler and immune
// to ever drifting from wherever the feed actually lives.
func loadOrCreateFeed(ctx context.Context, store *storage.Store, channel config.Channel, feedKey string) (*feed.Feed, error) {
	data, err := store.Get(ctx, feedKey)
	var fd *feed.Feed
	switch {
	case err == nil:
		fd, err = feed.Parse(data)
		if err != nil {
			return nil, err
		}
	case err == storage.ErrNotFound:
		fd = &feed.Feed{
			Title:       channel.FeedTitle(),
			Description: channel.FeedDescription(),
			Category:    channel.FeedCategory(),
		}
	default:
		return nil, fmt.Errorf("fetching existing feed: %w", err)
	}
	fd.SelfURL = store.PublicURL(feedKey)
	return fd, nil
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
