// redetect runs detection on the labeled videos from cached captions and writes
// a render record per video, so fit/score_labels.py can score a detection
// change without downloading audio or rendering anything.
//
//	go run ./cmd/redetect -out $SCRATCH/renders
//	go run ./cmd/redetect -out $SCRATCH/renders -rules sponsor,selfpromo,credits
//	python3 fit/score_labels.py $SCRATCH/renders -v
//
// Records include model evidence for offline replay. The pipeline's own trims
// (the silent tail, snapping to silence) aren't applied. All selected metadata
// and captions must be usable in the cache; this command never invokes yt-dlp.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/detect"
	"github.com/cwebley/shearcast/internal/experiment"
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
	neutralContext := flag.Bool("neutral-context", false, "experiment with neutral descriptions of coarse boundary context, preserving quoted text")
	checkCache := flag.Bool("check-cache", false, "validate all selected cached inputs without model calls or output")
	flag.Parse()
	if *out == "" && !*checkCache {
		return fmt.Errorf("need -out")
	}
	if *workers < 1 {
		return fmt.Errorf("-workers must be positive")
	}
	selected, err := loadSelection(*labels, *only)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cache := youtube.Cache{Dir: *cacheDir}
	inputs, err := loadCachedInputs(ctx, cache, selected)
	if err != nil {
		return err
	}
	if *checkCache {
		fmt.Fprintf(os.Stderr, "%d selected episodes have usable cached metadata and captions; no YouTube requests or model calls\n", len(inputs))
		return nil
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	key, err := cfg.Jev.APIKey()
	if err != nil {
		return err
	}

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

	type job struct {
		options detect.Options
		record  experiment.Record
		input   cachedInput
	}
	var jobs []job
	for i, label := range selected {
		ch, ok := cfg.ChannelBySlug(label.Channel)
		if !ok {
			return fmt.Errorf("no channel %q in config", label.Channel)
		}
		opts := cfg.DetectOptions(ch.Rules)
		opts.NeutralContext = *neutralContext
		if rules != nil {
			opts.Rules = rules
		}
		if !ch.NoWeights {
			opts.StartWeights, opts.EndWeights = startWeights, endWeights
		}
		jobs = append(jobs, job{opts, label, inputs[i]})
	}
	code, err := experiment.CurrentCode()
	if err != nil {
		return err
	}
	if err := experiment.CreateOutput(*out); err != nil {
		return err
	}
	var (
		mu     sync.Mutex
		failed int
		usage  jev.Stats
		wg     sync.WaitGroup
		sem    = make(chan struct{}, *workers)
	)
	for _, job := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			client := jev.New(jev.Config{APIKey: key, Model: cfg.Jev.Model})
			record := job.record
			record.Model, record.Code, record.CreatedAt = cfg.Jev.Model, code, time.Now().UTC()
			n, err := redetect(ctx, client, job.input, job.options, *out, record)
			mu.Lock()
			defer mu.Unlock()
			usage = usage.Add(client.Stats())
			if err != nil {
				failed++
				fmt.Fprintf(os.Stderr, "%s %s: %v\n", record.Channel, record.VideoID, err)
				return
			}
			fmt.Fprintf(os.Stderr, "%s %s: %d region(s)\n", record.Channel, record.VideoID, n)
		}()
	}
	wg.Wait()
	fmt.Fprintf(os.Stderr, "%d selected, %d completed, %d failed; cost $%.4f\n", len(jobs), len(jobs)-failed, failed, usage.Cost)
	if failed > 0 {
		return fmt.Errorf("%d video(s) failed", failed)
	}
	return nil
}

func loadSelection(dir, only string) ([]experiment.Record, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var selected []experiment.Record
	want := strings.Split(only, ",")
	matched := map[string]bool{}
	seen := map[string]bool{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var label struct {
			VideoID string `json:"video_id"`
			Channel string `json:"channel"`
		}
		if err := json.Unmarshal(data, &label); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if only != "" && !slices.Contains(want, label.VideoID) && !slices.Contains(want, label.Channel) {
			continue
		}
		for _, name := range []string{label.VideoID, label.Channel} {
			if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
				return nil, fmt.Errorf("%s: invalid video or channel identity", path)
			}
			matched[name] = true
		}
		identity := label.Channel + "/" + label.VideoID
		if seen[identity] {
			return nil, fmt.Errorf("duplicate labeled episode %s", identity)
		}
		seen[identity] = true
		selected = append(selected, experiment.Record{VideoID: label.VideoID, Channel: label.Channel, LabelSHA256: experiment.SHA256(data)})
	}
	if only != "" {
		var missing []string
		for _, selector := range want {
			if !matched[selector] {
				missing = append(missing, selector)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("selectors match no labels: %s", strings.Join(missing, ", "))
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no labeled videos selected from %s", dir)
	}
	return selected, nil
}

type cachedInput struct {
	video *youtube.Video
	cues  []transcript.Cue
}

func loadCachedInputs(ctx context.Context, cache youtube.Cache, selected []experiment.Record) ([]cachedInput, error) {
	inputs := make([]cachedInput, len(selected))
	var failures []error
	for i, record := range selected {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		video, cues, err := cache.CachedInputs(record.VideoID)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s %s: %w", record.Channel, record.VideoID, err))
			continue
		}
		inputs[i] = cachedInput{video, cues}
	}
	if len(failures) > 0 {
		return nil, fmt.Errorf("cache-only preflight failed for %d/%d selected episodes; no collection started:\n%w", len(failures), len(selected), errors.Join(failures...))
	}
	return inputs, nil
}

func redetect(ctx context.Context, client *jev.Client, input cachedInput, opts detect.Options, out string, record experiment.Record) (int, error) {
	opts.Subject = input.video.Title
	res, recording, err := detect.New(client, opts).RunRecorded(ctx, input.cues, input.video.Duration)
	if err != nil {
		return 0, err
	}
	record.Detection, record.Recording = res, recording
	record.DetectionOptions = recording.Options
	record.SourceDurationSeconds = input.video.Duration
	record.CollectedUsage = client.Stats()
	return len(res.Regions), experiment.Write(filepath.Join(out, record.Channel, record.VideoID, "render.m4a.json"), record)
}
