package main

import (
	"context"
	"fmt"
	"github.com/cwebley/shearcast/internal/config"
	"os"

	"github.com/cwebley/shearcast/internal/pipeline"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/youtube"
)

// runPublish uploads an already-rendered episode to storage and updates its
// channel's feed. It expects "render" (or "sync") to have already produced
// a channel-specific render. -audio can explicitly import an older output.
func runPublish(ctx context.Context, args []string) error {
	fs := flagSet("publish", "<video-url-or-id> -channel SLUG [flags]")
	cfgPath := fs.String("config", config.DefaultConfigPath(), "config file (optional)")
	channelSlug := fs.String("channel", "", "channel slug from config.toml")
	audioPath := fs.String("audio", "", "explicit path to rendered audio (default: recorded channel render)")
	statePath := fs.String("state", config.DefaultStatePath(), "episode state and command lock")
	cacheDir := cacheFlag(fs)
	positional, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		fs.Usage()
		return fmt.Errorf("need exactly one video url or id")
	}

	cfg, err := loadCommandConfig(*cfgPath, *statePath, *cacheDir)
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

	store, err := newPublisher(ctx, cfg, st, false)
	if err != nil {
		return err
	}

	runner := pipeline.Runner{Config: cfg, Cache: (youtube.Cache{Dir: *cacheDir}).ForChannel(channel.Slug), State: st, Publisher: store}
	defer func() { printUsage(os.Stderr, "model usage this run", &runner.Usage) }()
	episode, err := runner.Run(ctx, pipeline.EpisodeRequest{Action: pipeline.Publish, Channel: channel, Target: positional[0], AudioPath: *audioPath})
	if err != nil {
		return err
	}
	fmt.Printf("published %s\n", episode.AudioURL)
	fmt.Printf("feed updated: %s\n", episode.FeedURL)
	return nil
}
