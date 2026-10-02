// featuredump asks the start- or end-edge feature questions of every detected region
// in the labeled videos and writes one JSON row per candidate sentence, for
// refitting the edge weights (see fit/fit_weights.py).
//
// Anchors come from the render records already in the cache, so pass 1 is not
// re-run. Candidates and state are rebuilt with the current detect options.
//
//	go run ./cmd/featuredump -edge end
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/detect"
	"github.com/cwebley/shearcast/internal/fileutil"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/transcript"
	"github.com/cwebley/shearcast/internal/youtube"
)

type row struct {
	Channel     string             `json:"channel"`
	Video       string             `json:"video"`
	Rule        string             `json:"rule"`
	AnchorStart float64            `json:"anchor_start"`
	AnchorEnd   float64            `json:"anchor_end"`
	ID          string             `json:"id"`
	Start       float64            `json:"start"`
	End         float64            `json:"end"`
	Text        string             `json:"text"`
	Values      map[string]float64 `json:"values"`
	// Predicate is the recorded single-question score for this sentence
	// (StartCurve or EndCurve P), when the cached record has a point at the
	// same time. It is the baseline the fitted weights have to beat.
	Predicate *float64 `json:"predicate,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", config.DefaultConfigPath(), "config.toml")
	cacheDir := flag.String("cache", youtube.DefaultCacheDir(), "shearcast cache directory")
	renders := flag.String("renders", "", "renders directory holding <channel>/<id>/render.m4a.json (default <cache>/renders)")
	labels := flag.String("labels", "data/opus-labels", "Opus label directory")
	edge := flag.String("edge", "start", "start or end")
	out := flag.String("out", "", "output JSONL (default data/<edge>-features.jsonl)")
	only := flag.String("video", "", "only this video id")
	workers := flag.Int("workers", 4, "videos processed at once")
	partial := flag.Bool("partial", false, "write the rows of the videos that succeeded even if others failed")
	flag.Parse()
	if *edge != "start" && *edge != "end" {
		return fmt.Errorf("-edge must be start or end")
	}
	if *out == "" {
		*out = "data/" + *edge + "-features.jsonl"
	}
	if *renders == "" {
		*renders = filepath.Join(*cacheDir, "renders")
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

	paths, err := filepath.Glob(filepath.Join(*labels, "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(paths)

	var (
		mu   sync.Mutex
		rows []row
		errs []error
		wg   sync.WaitGroup
		sem  = make(chan struct{}, *workers)
		jobs int
	)
	for _, path := range paths {
		var label struct {
			VideoID string `json:"video_id"`
			Channel string `json:"channel"`
		}
		if err := readJSON(path, &label); err != nil {
			return err
		}
		if *only != "" && label.VideoID != *only {
			continue
		}
		jobs++
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			got, err := dumpVideo(ctx, cfg, client, cache, *renders, *edge, label.Channel, label.VideoID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s %s: %w", label.Channel, label.VideoID, err))
				fmt.Fprintln(os.Stderr, errs[len(errs)-1])
				return
			}
			fmt.Fprintf(os.Stderr, "%s %s: %d rows\n", label.Channel, label.VideoID, len(got))
			rows = append(rows, got...)
		}()
	}
	if jobs == 0 {
		return fmt.Errorf("no labeled videos selected from %s", *labels)
	}
	wg.Wait()
	// The output is a training corpus: replace it only with a complete,
	// nonempty export unless a partial one is asked for.
	if len(errs) > 0 && !*partial {
		return fmt.Errorf("%d video(s) failed; %s left unchanged (-partial writes the rest)", len(errs), *out)
	}
	if len(rows) == 0 {
		return fmt.Errorf("no rows exported; %s left unchanged", *out)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Video != b.Video {
			return a.Video < b.Video
		}
		if a.AnchorStart != b.AnchorStart {
			return a.AnchorStart < b.AnchorStart
		}
		return a.Start < b.Start
	})
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	if err := fileutil.WriteAtomic(*out, buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %d rows to %s, cost $%.4f\n", len(rows), *out, client.Stats().Cost)
	if len(errs) > 0 {
		return fmt.Errorf("%d video(s) failed", len(errs))
	}
	return nil
}

func dumpVideo(ctx context.Context, cfg *config.Config, client *jev.Client, cache youtube.Cache, renders, edge, channel, id string) ([]row, error) {
	var rec struct {
		Detection struct {
			Regions []detect.Region
		} `json:"detection"`
	}
	if err := readJSON(filepath.Join(renders, channel, id, "render.m4a.json"), &rec); err != nil {
		return nil, err
	}
	ch, ok := cfg.ChannelBySlug(channel)
	if !ok {
		return nil, fmt.Errorf("no channel %q in config", channel)
	}
	video, err := cache.Info(ctx, "https://www.youtube.com/watch?v="+id)
	if err != nil {
		return nil, err
	}
	vtt, err := cache.Captions(ctx, video)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(vtt)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cues, err := transcript.ParseVTT(f)
	if err != nil {
		return nil, err
	}

	opts := cfg.DetectOptions(ch.Rules)
	opts.Subject = video.Title
	d := detect.New(client, opts)

	var out []row
	for _, g := range rec.Detection.Regions {
		features, recorded := d.StartFeatures, g.StartCurve
		if edge == "end" {
			features, recorded = d.EndFeatures, g.EndCurve
			// A localized region's end is its sentence run; production never
			// ran the end estimator on it, so it has no predicate to fit
			// against (fit_weights.py requires one).
			if len(recorded) == 0 {
				continue
			}
		}
		cands, values, err := features(ctx, cues, g)
		if err != nil {
			return nil, err
		}
		predicate := make(map[float64]float64, len(recorded))
		for _, p := range recorded {
			predicate[p.Start] = p.P
		}
		for _, s := range cands {
			if values == nil {
				break
			}
			out = append(out, row{Channel: channel, Video: id, Rule: g.Rule,
				AnchorStart: g.AnchorStart, AnchorEnd: g.AnchorEnd,
				ID: s.ID, Start: s.Start, End: s.End, Text: s.Text, Values: values[s.ID]})
			if p, ok := predicate[s.Start]; ok {
				out[len(out)-1].Predicate = &p
			} else if p, ok := overlapping(recorded, s); ok {
				// The record came from an older sentence splitter; take the
				// recorded sentence that overlaps this one most.
				out[len(out)-1].Predicate = &p
			}
		}
	}
	return out, nil
}

func overlapping(points []detect.CurvePoint, s transcript.Sentence) (float64, bool) {
	best, found := 0.0, false
	var most float64
	for _, p := range points {
		if o := min(p.End, s.End) - max(p.Start, s.Start); o > most {
			best, most, found = p.P, o, true
		}
	}
	return best, found
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
