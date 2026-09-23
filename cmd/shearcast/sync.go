package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/pipeline"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/storage"
	"github.com/cwebley/shearcast/internal/youtube"
)

// runSync checks each configured channel's uploads for videos not already
// published, and renders + publishes each one it finds. It is meant to be
// invoked periodically by the OS scheduler (launchd, systemd), not run as a
// long-lived process: each invocation does one pass and exits.
func runSync(ctx context.Context, args []string) error {
	fs := flagSet("sync", "[-channel SLUG] [flags]")
	cfgPath := fs.String("config", "config.toml", "config file (optional)")
	channelSlug := fs.String("channel", "", "only sync this channel slug (default: every configured channel)")
	statePath := fs.String("state", "state.json", "path to the processed-episode record")
	cacheDir := cacheFlag(fs)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if len(cfg.Channels) == 0 {
		return fmt.Errorf("no channels configured in %s", *cfgPath)
	}
	channels := cfg.Channels
	if *channelSlug != "" {
		ch, err := requireChannel(cfg, *channelSlug)
		if err != nil {
			return err
		}
		channels = []config.Channel{ch}
	}

	if _, err := youtube.Check(ctx); err != nil {
		return err
	}
	st, err := state.Open(*statePath)
	if err != nil {
		return fmt.Errorf("opening state: %w", err)
	}
	client, err := newJevClient(cfg)
	if err != nil {
		return err
	}
	storeCfg, err := storageConfigFromEnv()
	if err != nil {
		return err
	}
	store, err := storage.New(ctx, storeCfg)
	if err != nil {
		return err
	}
	cache := youtube.Cache{Dir: *cacheDir}

	var failures int
	for _, channel := range channels {
		if channel.URL == "" {
			fmt.Fprintf(os.Stderr, "%s: no url configured, skipping\n", channel.Slug)
			continue
		}
		limit := channel.WatchLimit
		if limit == 0 {
			limit = 5
		}
		uploads, err := youtube.List(ctx, channel.URL, limit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: listing uploads: %v\n", channel.Slug, err)
			failures++
			continue
		}

		for _, upload := range uploads {
			if st.IsProcessed(channel.Slug, upload.ID) {
				continue
			}
			fmt.Fprintf(os.Stderr, "%s: new episode %s (%s)\n", channel.Slug, upload.ID, upload.Title)
			if err := syncOne(ctx, cfg, channel, cache, client, store, upload.ID); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %s: %v\n", channel.Slug, upload.ID, err)
				failures++
				continue
			}
			if err := st.MarkProcessed(channel.Slug, upload.ID); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %s: recording state: %v\n", channel.Slug, upload.ID, err)
				failures++
			}
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d episode(s) failed; see above", failures)
	}
	return nil
}

// syncOne renders and publishes a single video, sharing the exact pipeline
// steps "render" and "publish" use individually.
func syncOne(ctx context.Context, cfg *config.Config, channel config.Channel, cache youtube.Cache, client *jev.Client, store *storage.Store, target string) error {
	video, cues, err := loadVideo(ctx, cache, target)
	if err != nil {
		return fmt.Errorf("loading video: %w", err)
	}

	path, _, _, err := pipeline.RenderEpisode(ctx, cfg, channel, cache, client, video, cues, pipeline.RenderOptions{}, nil)
	if err != nil {
		return fmt.Errorf("rendering: %w", err)
	}

	result, err := pipeline.PublishEpisode(ctx, store, channel, video, path)
	if err != nil {
		return fmt.Errorf("publishing: %w", err)
	}
	fmt.Fprintf(os.Stderr, "%s: published %s (feed: %s)\n", channel.Slug, result.AudioURL, result.FeedURL)
	return nil
}
