// renderbench exercises the production renderer using local media only.
// scripts/measure-render.py samples this process and its FFmpeg children.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/cwebley/shearcast/internal/render"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	input := flag.String("input", "", "local audio fixture or cached source")
	outDir := flag.String("out-dir", "", "directory for benchmark outputs")
	pattern := flag.String("pattern", "typical", "none, typical (one 60s cut per 15m), heavy (one 15s cut per minute)")
	bitrate := flag.Int("bitrate", 128, "AAC target bitrate in kbps")
	repeat := flag.Int("repeat", 1, "consecutive renders in this process")
	flag.Parse()
	if *input == "" || *outDir == "" || *repeat < 1 {
		return fmt.Errorf("need -input, -out-dir and a positive -repeat")
	}
	if err := render.ValidateBitrate(*bitrate); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	duration, err := render.Probe(ctx, *input)
	if err != nil {
		return err
	}
	keep, err := ranges(duration.Seconds(), *pattern)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	for i := 0; i < *repeat; i++ {
		out := filepath.Join(*outDir, fmt.Sprintf("%03d.m4a", i+1))
		start := time.Now()
		opts := render.DefaultCutOptions()
		opts.BitrateKbps = *bitrate
		if err := render.Cut(ctx, *input, keep, out, opts); err != nil {
			return err
		}
		actual, err := render.Probe(ctx, out)
		if err != nil {
			return err
		}
		info, err := os.Stat(out)
		if err != nil {
			return err
		}
		var expected float64
		for _, r := range keep {
			expected += r.End - r.Start
		}
		expected -= float64(len(keep)-1) * opts.Crossfade
		if delta := actual.Seconds() - expected; delta < -0.1 || delta > 0.1 {
			return fmt.Errorf("duration %.3fs differs from timeline %.3fs", actual.Seconds(), expected)
		}
		row := struct {
			Episode       int     `json:"episode"`
			SourceSeconds float64 `json:"source_seconds"`
			Ranges        int     `json:"ranges"`
			OutputSeconds float64 `json:"output_seconds"`
			OutputBytes   int64   `json:"output_bytes"`
			WallSeconds   float64 `json:"wall_seconds"`
		}{i + 1, duration.Seconds(), len(keep), actual.Seconds(), info.Size(), time.Since(start).Seconds()}
		if err := json.NewEncoder(os.Stdout).Encode(row); err != nil {
			return err
		}
	}
	return nil
}

func ranges(duration float64, pattern string) ([]render.Range, error) {
	if duration <= 0 {
		return nil, fmt.Errorf("source duration must be positive")
	}
	var period, cut float64
	switch pattern {
	case "none":
		return []render.Range{{Start: 0, End: duration}}, nil
	case "typical":
		period, cut = 900, 60
	case "heavy":
		period, cut = 60, 15
	default:
		return nil, fmt.Errorf("unknown pattern %q", pattern)
	}
	var keep []render.Range
	cursor := 0.0
	for start := period / 2; start+cut < duration; start += period {
		keep = append(keep, render.Range{Start: cursor, End: start})
		cursor = start + cut
	}
	return append(keep, render.Range{Start: cursor, End: duration}), nil
}
