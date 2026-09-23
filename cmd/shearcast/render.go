package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/pipeline"
	"github.com/cwebley/shearcast/internal/youtube"
)

// runRender detects ad regions in a video, snaps each boundary to real
// silence in the full downloaded audio, and writes the de-sponsored episode
// locally. It does not upload anything; see "publish" for that.
func runRender(ctx context.Context, args []string) error {
	fs := flagSet("render", "<video-url-or-id> -channel SLUG [-out PATH] [flags]")
	cfgPath := fs.String("config", "config.toml", "config file (optional)")
	channelSlug := fs.String("channel", "", "channel slug from config.toml (selects rules, weights and feed metadata)")
	out := fs.String("out", "", "output audio path (default: <cache>/<id>/render.m4a)")
	snapWindow := fs.Float64("snap-window", 1.0, "seconds searched on either side of each detected boundary for real silence")
	noSnap := fs.Bool("no-snap", false, "cut at the raw detected boundaries, skipping silence-snapping")
	crossfade := fs.Float64("crossfade", 0.05, "seconds of crossfade at each join")
	minKeep := fs.Float64("min-keep", 1.0, "shortest kept island worth splicing in, seconds")
	cacheDir := cacheFlag(fs)
	positional, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		fs.Usage()
		return fmt.Errorf("need exactly one video url or id")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	channel, err := requireChannel(cfg, *channelSlug)
	if err != nil {
		return err
	}

	if _, err := youtube.Check(ctx); err != nil {
		return err
	}
	cache := youtube.Cache{Dir: *cacheDir}
	video, cues, err := loadVideo(ctx, cache, positional[0])
	if err != nil {
		return err
	}

	client, err := newJevClient(cfg)
	if err != nil {
		return err
	}

	started := time.Now()
	path, res, keep, err := pipeline.RenderEpisode(ctx, cfg, channel, cache, client, video, cues,
		pipeline.RenderOptions{
			SnapWindow: *snapWindow,
			NoSnap:     *noSnap,
			Crossfade:  *crossfade,
			MinKeep:    *minKeep,
			OutPath:    *out,
		},
		func(msg string) { fmt.Fprintln(os.Stderr, msg) },
	)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "done in %s\n", time.Since(started).Round(time.Millisecond))

	var kept float64
	for _, r := range keep {
		kept += r.End - r.Start
	}
	fmt.Printf("wrote %s\n", path)
	fmt.Printf("%d region(s) removed, %s kept of %s\n",
		len(res.Regions), hms(kept), hms(video.Duration))
	return nil
}
