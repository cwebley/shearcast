package main

import (
	"context"
	"fmt"
	"github.com/cwebley/shearcast/internal/config"
	"os"
	"time"

	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/pipeline"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/youtube"
)

// runRender detects ad regions in a video, snaps each boundary to real
// silence in the full downloaded audio, and writes the de-sponsored episode
// locally. It does not upload anything; see "publish" for that.
func runRender(ctx context.Context, args []string) error {
	fs := flagSet("render", "<video-url-or-id> -channel SLUG [-out PATH] [flags]")
	cfgPath := fs.String("config", config.DefaultConfigPath(), "config file (optional)")
	channelSlug := fs.String("channel", "", "channel slug from config.toml (selects rules, weights and feed metadata)")
	out := fs.String("out", "", "output audio path (default: <cache>/renders/<channel>/<id>/render.m4a)")
	statePath := fs.String("state", config.DefaultStatePath(), "episode state and command lock")
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

	cfg, err := loadCommandConfig(*cfgPath, *statePath, *cacheDir, *out)
	if err != nil {
		return err
	}
	st, err := state.Open(*statePath)
	if err != nil {
		return err
	}
	defer st.Close()
	channel, err := requireChannel(cfg, *channelSlug)
	if err != nil {
		return err
	}

	runner := pipeline.Runner{Config: cfg, State: st, Cache: youtube.Cache{Dir: *cacheDir},
		NewClient: func() (*jev.Client, error) { return newJevClient(cfg) },
		Progress:  func(msg string) { fmt.Fprintln(os.Stderr, msg) },
	}
	defer func() { printUsage(os.Stderr, "model usage this run", &runner.Usage) }()

	started := time.Now()
	episode, err := runner.Run(ctx, pipeline.EpisodeRequest{Action: pipeline.Render, Channel: channel, Target: positional[0],
		RenderOptions: pipeline.RenderOptions{
			SnapWindow: *snapWindow,
			NoSnap:     *noSnap,
			Crossfade:  *crossfade,
			MinKeep:    *minKeep,
			OutPath:    *out,
		},
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "done in %s\n", time.Since(started).Round(time.Millisecond))

	if episode.Stage == state.Waiting {
		fmt.Println("waiting for English captions; retry on a later sync")
		return nil
	}
	fmt.Printf("wrote %s\n", episode.RenderPath)
	fmt.Printf("%s kept of %s\n", hms(episode.DurationSeconds), hms(episode.Video.Duration))
	return nil
}
