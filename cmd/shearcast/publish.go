package main

import (
	"context"
	"fmt"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/pipeline"
	"github.com/cwebley/shearcast/internal/storage"
	"github.com/cwebley/shearcast/internal/youtube"
)

// runPublish uploads an already-rendered episode to storage and updates its
// channel's feed. It expects "render" (or "sync") to have already produced
// <cache>/<id>/render.m4a.
func runPublish(ctx context.Context, args []string) error {
	fs := flagSet("publish", "<video-url-or-id> -channel SLUG [flags]")
	cfgPath := fs.String("config", "config.toml", "config file (optional)")
	channelSlug := fs.String("channel", "", "channel slug from config.toml")
	audioPath := fs.String("audio", "", "path to the rendered audio (default: <cache>/<id>/render.m4a)")
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

	cache := youtube.Cache{Dir: *cacheDir}
	video, _, err := loadVideo(ctx, cache, positional[0])
	if err != nil {
		return err
	}

	path := *audioPath
	if path == "" {
		path = cache.Dir + "/" + video.ID + "/render.m4a"
	}

	storeCfg, err := storageConfigFromEnv()
	if err != nil {
		return err
	}
	store, err := storage.New(ctx, storeCfg)
	if err != nil {
		return err
	}

	result, err := pipeline.PublishEpisode(ctx, store, channel, video, path)
	if err != nil {
		return err
	}
	fmt.Printf("uploaded %s\n", result.AudioURL)
	fmt.Printf("feed updated: %s\n", result.FeedURL)
	return nil
}
