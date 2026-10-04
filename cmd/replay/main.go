// replay executes the production detector using saved model evidence. It never
// contacts a provider, fetches captions, renders audio, or publishes anything.
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
	"time"

	"github.com/cwebley/shearcast/internal/detect"
	"github.com/cwebley/shearcast/internal/experiment"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	in := flag.String("in", "", "directory containing recorded <channel>/<id>/render.m4a.json files")
	out := flag.String("out", "", "new directory for replayed detection records")
	start := flag.String("start-weights", "", "candidate start weights (default: recorded weights)")
	end := flag.String("end-weights", "", "candidate end weights (default: recorded weights)")
	conservativeEnd := flag.Bool("conservative-ambiguous-end", false, "enable the conservative low-ad ambiguous-end fallback")
	verify := flag.Bool("verify", true, "require recorded settings to reproduce saved decisions before evaluating candidates")
	flag.Parse()
	if *in == "" || *out == "" {
		return fmt.Errorf("need -in and -out")
	}
	startWeights, err := candidateWeights(*start, "start")
	if err != nil {
		return err
	}
	endWeights, err := candidateWeights(*end, "end")
	if err != nil {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(*in, "*", "*", "render.m4a.json"))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("no records in %s", *in)
	}
	code, err := experiment.CurrentCode()
	if err != nil {
		return err
	}
	if err := experiment.CreateOutput(*out); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	type outcome struct {
		Path   string `json:"path"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	var outcomes []outcome
	complete, incomplete, mismatch, failed := 0, 0, 0, 0
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		status, err := replayFile(ctx, path, *out, code, startWeights, endWeights, *verify, *conservativeEnd)
		entry := outcome{Path: path, Status: status}
		if err != nil {
			entry.Error = err.Error()
			fmt.Fprintf(os.Stderr, "%s: %s: %v\n", path, status, err)
		}
		outcomes = append(outcomes, entry)
		switch status {
		case "complete":
			complete++
		case "incomplete":
			incomplete++
		case "mismatch":
			mismatch++
		default:
			failed++
		}
	}
	if err := experiment.Write(filepath.Join(*out, "replay-summary.json"), outcomes); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%d episodes: %d complete, %d incomplete, %d decision mismatches, %d failed; no model calls\n",
		len(paths), complete, incomplete, mismatch, failed)
	if complete != len(paths) {
		return fmt.Errorf("replay incomplete; see %s", filepath.Join(*out, "replay-summary.json"))
	}
	return nil
}

func candidateWeights(path, edge string) (*detect.Weights, error) {
	if path == "" {
		return nil, nil
	}
	w, err := detect.LoadWeights(path)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, fmt.Errorf("weights not found: %s", path)
	}
	if w.Edge != "" && w.Edge != edge {
		return nil, fmt.Errorf("%s contains %s weights, want %s", path, w.Edge, edge)
	}
	features := detect.Features("")
	if edge == "end" {
		features = detect.EndFeatures("")
	}
	known := map[string]float64{}
	for _, feature := range features {
		known[feature.ID] = 0
	}
	for _, id := range detect.LexicalIDs() {
		known[id] = 0
	}
	if edge == "end" {
		known[detect.PredicateFeature] = 0
	}
	if missing := w.Missing(known); len(missing) > 0 {
		return nil, fmt.Errorf("%s requires unmeasured features: %v", path, missing)
	}
	return w, nil
}

func replayFile(ctx context.Context, path, out string, code experiment.Code, start, end *detect.Weights, verify, conservativeEnd bool) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "failed", err
	}
	var record experiment.Record
	if err := json.Unmarshal(data, &record); err != nil {
		return "failed", err
	}
	if record.Recording == nil || record.Detection == nil {
		return "incomplete", fmt.Errorf("missing recorded evidence or production decisions")
	}
	// Derive output placement from the input tree, never from record contents.
	id, channel := filepath.Base(filepath.Dir(path)), filepath.Base(filepath.Dir(filepath.Dir(path)))
	if id != record.VideoID || channel != record.Channel {
		return "failed", fmt.Errorf("record identity does not match its path")
	}
	var result *detect.Result
	if verify {
		result, err = detect.Replay(ctx, record.Recording, nil)
		if err != nil {
			return replayFailure(err)
		}
		if !experiment.SameDecisions(record.Detection, result) {
			return "mismatch", fmt.Errorf("recorded settings no longer reproduce production decisions")
		}
	}
	opts := record.Recording.Options
	if start != nil {
		opts.StartWeights = start
	}
	if end != nil {
		opts.EndWeights = end
	}
	if conservativeEnd {
		opts.ConservativeAmbiguousEnd = true
	}
	if !verify || start != nil || end != nil || conservativeEnd {
		result, err = detect.Replay(ctx, record.Recording, &opts)
		if err != nil {
			return replayFailure(err)
		}
	}
	record.Detection, record.DetectionOptions = result, opts
	record.Recording.Options = opts
	record.Code, record.CreatedAt = code, time.Now().UTC()
	record.Replay = &experiment.ReplayInfo{SourceSHA256: experiment.SHA256(data), Verified: verify}
	if err := experiment.Write(filepath.Join(out, channel, id, "render.m4a.json"), record); err != nil {
		return "failed", err
	}
	return "complete", nil
}

func replayFailure(err error) (string, error) {
	if errors.Is(err, detect.ErrIncompleteReplay) {
		return "incomplete", err
	}
	return "failed", err
}
