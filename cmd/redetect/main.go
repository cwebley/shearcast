// redetect runs detection on the labeled videos from cached captions and writes
// a render record per video, so fit/score_labels.py can score a detection
// change without downloading audio or rendering anything.
//
//	go run ./cmd/redetect -out $SCRATCH/renders
//	go run ./cmd/redetect -out $SCRATCH/renders -rules sponsor,selfpromo,credits
//	python3 fit/score_labels.py $SCRATCH/renders -v
//
// Only Detection is written. The pipeline's own trims (the silent tail, snapping
// to silence) aren't applied, and score_labels.py doesn't read them.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/detect"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/transcript"
	"github.com/cwebley/shearcast/internal/youtube"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", config.DefaultConfigPath(), "config.toml")
	cacheDir := flag.String("cache", youtube.DefaultCacheDir(), "shearcast cache directory")
	labels := flag.String("labels", "data/opus-labels", "Opus label directory")
	out := flag.String("out", "", "directory to write <channel>/<id>/render.m4a.json into (required)")
	only := flag.String("video", "", "only these video ids or channel slugs, comma-separated")
	ruleList := flag.String("rules", "", "rule ids to run, from config.toml or the built-in defaults (default: the channel's own)")
	workers := flag.Int("workers", 4, "videos processed at once")
	flag.Parse()
	if *out == "" {
		return fmt.Errorf("need -out")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	key, err := cfg.Jev.APIKey()
	if err != nil {
		return err
	}
	client := jev.New(jev.Config{APIKey: key, Model: cfg.Jev.Model})
	cache := youtube.Cache{Dir: *cacheDir}

	var rules []detect.Rule
	if *ruleList != "" {
		known := map[string]detect.Rule{}
		for _, r := range append(config.DefaultRules(), cfg.Rules...) {
			known[r.ID] = detect.Rule{ID: r.ID, Prompt: r.Prompt, Threshold: r.Threshold}
		}
		for _, id := range strings.Split(*ruleList, ",") {
			r, ok := known[id]
			if !ok {
				return fmt.Errorf("unknown rule %q", id)
			}
			rules = append(rules, r)
		}
	}
	startWeights, err := detect.LoadWeights(cfg.Jev.Weights)
	if err != nil {
		return err
	}
	endWeights, err := detect.LoadWeights(cfg.Jev.EndWeights)
	if err != nil {
		return err
	}

	paths, err := filepath.Glob(filepath.Join(*labels, "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	var (
		mu     sync.Mutex
		failed int
		wg     sync.WaitGroup
		sem    = make(chan struct{}, *workers)
	)
	for _, path := range paths {
		var label struct {
			VideoID string `json:"video_id"`
			Channel string `json:"channel"`
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &label); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if *only != "" {
			want := strings.Split(*only, ",")
			if !slices.Contains(want, label.VideoID) && !slices.Contains(want, label.Channel) {
				continue
			}
		}
		ch, ok := cfg.ChannelBySlug(label.Channel)
		if !ok {
			return fmt.Errorf("%s: no channel %q in config", path, label.Channel)
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			opts := cfg.DetectOptions(ch.Rules)
			if rules != nil {
				opts.Rules = rules
			}
			if !ch.NoWeights {
				opts.StartWeights, opts.EndWeights = startWeights, endWeights
			}
			n, err := redetect(ctx, client, cache, opts, *out, label.Channel, label.VideoID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed++
				fmt.Fprintf(os.Stderr, "%s %s: %v\n", label.Channel, label.VideoID, err)
				return
			}
			fmt.Fprintf(os.Stderr, "%s %s: %d region(s)\n", label.Channel, label.VideoID, n)
		}()
	}
	wg.Wait()
	fmt.Fprintf(os.Stderr, "cost $%.4f\n", client.Stats().Cost)
	if failed > 0 {
		return fmt.Errorf("%d video(s) failed", failed)
	}
	return nil
}

func redetect(ctx context.Context, client *jev.Client, cache youtube.Cache, opts detect.Options, out, channel, id string) (int, error) {
	video, err := cache.Info(ctx, "https://www.youtube.com/watch?v="+id)
	if err != nil {
		return 0, err
	}
	vtt, err := cache.Captions(ctx, video)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(vtt)
	if err != nil {
		return 0, err
	}
	cues, err := transcript.ParseVTT(f)
	f.Close()
	if err != nil {
		return 0, err
	}
	opts.Subject = video.Title
	res, err := detect.New(client, opts).Run(ctx, cues, video.Duration)
	if err != nil {
		return 0, err
	}
	dir := filepath.Join(out, channel, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	data, err := json.MarshalIndent(map[string]any{"video_id": id, "channel": channel, "detection": res}, "", "  ")
	if err != nil {
		return 0, err
	}
	return len(res.Regions), os.WriteFile(filepath.Join(dir, "render.m4a.json"), data, 0o644)
}
