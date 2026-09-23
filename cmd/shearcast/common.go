package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/storage"
	"github.com/cwebley/shearcast/internal/transcript"
	"github.com/cwebley/shearcast/internal/youtube"
)

// cacheFlag registers the caption cache location on a command's flags.
func cacheFlag(fs *flag.FlagSet) *string {
	return fs.String("cache", youtube.DefaultCacheDir(), "directory caching video metadata, captions and rendered audio")
}

// loadVideo returns a video's metadata and parsed captions, fetching from
// YouTube only what the cache does not already hold.
func loadVideo(ctx context.Context, cache youtube.Cache, target string) (*youtube.Video, []transcript.Cue, error) {
	if !strings.Contains(target, "/") {
		target = "https://www.youtube.com/watch?v=" + target
	}
	if id := youtube.VideoID(target); id != "" && cache.Cached(id) {
		fmt.Fprintf(os.Stderr, "%s: cached\n", id)
	} else {
		fmt.Fprintf(os.Stderr, "fetching metadata and captions...\n")
	}

	video, err := cache.Info(ctx, target)
	if err != nil {
		return nil, nil, err
	}
	vttPath, err := cache.Captions(ctx, video)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(vttPath)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	cues, err := transcript.ParseVTT(f)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", vttPath, err)
	}
	return video, cues, nil
}

// requireChannel looks up slug in cfg, or returns a clear error naming what
// is configured.
func requireChannel(cfg *config.Config, slug string) (config.Channel, error) {
	if slug == "" {
		return config.Channel{}, fmt.Errorf("need -channel (a slug from config.toml)")
	}
	ch, ok := cfg.ChannelBySlug(slug)
	if !ok {
		var known []string
		for _, c := range cfg.Channels {
			known = append(known, c.Slug)
		}
		return config.Channel{}, fmt.Errorf("no channel with slug %q in config.toml (known: %s)", slug, strings.Join(known, ", "))
	}
	return ch, nil
}

// newJevClient builds a Jev client from config, reading the API key from the
// environment (see config.Jev.APIKey).
func newJevClient(cfg *config.Config) (*jev.Client, error) {
	key, err := cfg.Jev.APIKey()
	if err != nil {
		return nil, err
	}
	return jev.New(jev.Config{
		BaseURL: cfg.Jev.ResolveBaseURL(),
		APIKey:  key,
		Model:   cfg.Jev.Model,
	}), nil
}

// storageConfigFromEnv reads R2 credentials and bucket info from the
// environment (see .env.example): these are account-scoped values, not
// per-channel settings, so they live alongside the Jev API key rather than
// in config.toml.
func storageConfigFromEnv() (storage.Config, error) {
	cfg := storage.Config{
		AccountID:       os.Getenv("R2_ACCOUNT_ID"),
		Bucket:          os.Getenv("R2_BUCKET"),
		AccessKeyID:     os.Getenv("R2_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		PublicBaseURL:   os.Getenv("R2_PUBLIC_BASE_URL"),
	}
	var missing []string
	for name, v := range map[string]string{
		"R2_ACCOUNT_ID": cfg.AccountID, "R2_BUCKET": cfg.Bucket,
		"R2_ACCESS_KEY_ID": cfg.AccessKeyID, "R2_SECRET_ACCESS_KEY": cfg.SecretAccessKey,
		"R2_PUBLIC_BASE_URL": cfg.PublicBaseURL,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return storage.Config{}, fmt.Errorf("missing %s (set them in .env)", strings.Join(missing, ", "))
	}
	return cfg, nil
}
