package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/detect"
	"github.com/cwebley/shearcast/internal/experiment"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/segment"
	"github.com/cwebley/shearcast/internal/transcript"
)

// A model fixture for an ad-free episode. Requests still pass through Run,
// recording, JSON persistence and the CLI's normal replay path.
type contentModel struct{ promotion, ambiguousEnd bool }

func (contentModel) Stats() jev.Stats { return jev.Stats{} }
func (m contentModel) Ask(_ context.Context, _ string, questions map[string]jev.Question) (*jev.Response, error) {
	answers := map[string]jev.Answer{}
	for id, question := range questions {
		if question.Type == jev.TypeNoul {
			answers[id] = jev.Answer{Type: "noul", Noul: 0}
			if m.ambiguousEnd && strings.HasPrefix(id, "R") {
				answers[id] = jev.Answer{Type: "noul", Noul: 0.55}
			}
			continue
		}
		answers[id] = jev.Answer{Type: "choice", Choice: "content", Probabilities: map[string]float64{"content": 1, "sponsor": 0}}
		if m.promotion && (id == "W004" || id == "W005" || m.ambiguousEnd && id == "W006") {
			answers[id] = jev.Answer{Type: "choice", Choice: "sponsor", Probabilities: map[string]float64{"content": 0, "sponsor": 1}}
		}
	}
	return &jev.Response{Answers: answers}, nil
}

func TestReplayCLISelectsConservativeEndAfterVerifyingBaseline(t *testing.T) {
	root := t.TempDir()
	input, output := filepath.Join(root, "input"), filepath.Join(root, "output")
	opts := detect.Defaults([]detect.Rule{{ID: "sponsor", Prompt: "A paid promotion"}})
	opts.WeakAnchorThreshold = 1
	opts.OutroTrimFloor = 2
	opts.StartWeights = &detect.Weights{Bias: -5, Weights: map[string]float64{"lex_thanks": 10}}
	opts.EndWeights = &detect.Weights{Bias: -5}
	var cues []transcript.Cue
	for start := 0.0; start < 180; start += 5 {
		text := "The universe is vast and mostly empty."
		if start >= 90 && start < 150 {
			text = "Thanks to Acme for supporting the show."
		}
		cues = append(cues, transcript.Cue{Start: start, End: start + 5, Text: text})
	}
	result, recording, err := detect.New(contentModel{promotion: true, ambiguousEnd: true}, opts).RunRecorded(context.Background(), cues, 180)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Regions) != 1 || result.Regions[0].End != 180 {
		t.Fatalf("baseline should remove the closing 30s, got %+v", result.Regions)
	}
	record := experiment.Record{VideoID: "episode", Channel: "show", Detection: result, Recording: recording}
	if err := experiment.Write(filepath.Join(input, "show", "episode", "render.m4a.json"), record); err != nil {
		t.Fatal(err)
	}
	if err := invoke(t, "-in", input, "-out", output, "-conservative-ambiguous-end"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "show", "episode", "render.m4a.json"))
	if err != nil {
		t.Fatal(err)
	}
	var replayed experiment.Record
	if err := json.Unmarshal(data, &replayed); err != nil {
		t.Fatal(err)
	}
	if !replayed.Replay.Verified || !replayed.DetectionOptions.ConservativeAmbiguousEnd || !replayed.Recording.Options.ConservativeAmbiguousEnd {
		t.Fatal("output lost baseline verification or candidate options")
	}
	if len(replayed.Detection.Regions) != 1 || replayed.Detection.Regions[0].Start != 90 || replayed.Detection.Regions[0].End != 150 || replayed.Detection.Stats.Calls != 0 {
		t.Fatalf("want the 90-150s cut with zero model usage, got %+v", replayed.Detection.Regions)
	}
	if _, err := detect.Replay(context.Background(), replayed.Recording, nil); err != nil {
		t.Fatalf("persisted candidate cannot replay itself: %v", err)
	}
}

func TestReplayCLIWithVerificationDisabledEvaluatesCandidateDirectly(t *testing.T) {
	root := t.TempDir()
	input, output := filepath.Join(root, "input"), filepath.Join(root, "output")
	opts := detect.Defaults([]detect.Rule{{ID: "sponsor", Prompt: "A paid promotion"}})
	opts.WeakAnchorThreshold = 1
	candidate := &detect.Weights{Edge: "start", Bias: -5, Weights: map[string]float64{"lex_thanks": 10}}
	opts.StartWeights = candidate
	var cues []transcript.Cue
	for start := 0.0; start < 240; start += 5 {
		text := "The universe is vast and mostly empty."
		if start >= 90 && start < 150 {
			text = "Thanks to Acme for supporting the show."
		}
		cues = append(cues, transcript.Cue{Start: start, End: start + 5, Text: text})
	}
	result, recording, err := detect.New(contentModel{promotion: true}, opts).RunRecorded(context.Background(), cues, 240)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Regions) != 1 || result.Regions[0].Start != 90 {
		t.Fatalf("fixture should start its cut at 90s, got %+v", result.Regions)
	}
	// These settings need an unrecorded classification fallback. A caller who
	// disables baseline verification should still be able to evaluate the
	// candidate whose entire evidence is available.
	recording.Options.StartWeights = &detect.Weights{Bias: -5}
	record := experiment.Record{VideoID: "episode", Channel: "show", Detection: result, Recording: recording}
	if err := experiment.Write(filepath.Join(input, "show", "episode", "render.m4a.json"), record); err != nil {
		t.Fatal(err)
	}
	weights := filepath.Join(root, "candidate.json")
	if err := experiment.Write(weights, candidate); err != nil {
		t.Fatal(err)
	}
	if err := invoke(t, "-in", input, "-out", output, "-verify=false", "-start-weights", weights); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "show", "episode", "render.m4a.json"))
	if err != nil {
		t.Fatal(err)
	}
	var replayed experiment.Record
	if err := json.Unmarshal(data, &replayed); err != nil {
		t.Fatal(err)
	}
	if !experiment.SameDecisions(result, replayed.Detection) || replayed.Replay.Verified {
		t.Fatal("candidate was not replayed directly with verification marked disabled")
	}
}
func (m contentModel) AskAll(ctx context.Context, batches []jev.Batch, _ int) (map[string]jev.Answer, error) {
	answers := map[string]jev.Answer{}
	for _, batch := range batches {
		response, err := m.Ask(ctx, batch.State, batch.Questions)
		if err != nil {
			return nil, err
		}
		for id, answer := range response.Answers {
			answers[id] = answer
		}
	}
	return answers, nil
}

func invoke(t *testing.T, args ...string) error {
	t.Helper()
	previousFlags, previousArgs := flag.CommandLine, os.Args
	defer func() { flag.CommandLine, os.Args = previousFlags, previousArgs }()
	flag.CommandLine = flag.NewFlagSet("replay", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = append([]string{"replay"}, args...)
	return run()
}

func TestReplayCLIReportsCoverageAndNeverReusesOldOutput(t *testing.T) {
	for _, scenario := range []string{"complete", "missing evidence", "changed decision"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			input, output := filepath.Join(root, "input"), filepath.Join(root, "output")
			opts := detect.Defaults([]detect.Rule{{ID: "sponsor", Prompt: "A paid promotion"}})
			result, recording, err := detect.New(contentModel{}, opts).RunRecorded(context.Background(),
				[]transcript.Cue{{Start: 0, End: 30, Text: "The universe is vast and mostly empty."}}, 30)
			if err != nil {
				t.Fatal(err)
			}
			record := experiment.Record{VideoID: "episode", Channel: "show", Detection: result, Recording: recording, DetectionOptions: opts}
			wantStatus := "complete"
			switch scenario {
			case "missing evidence":
				record.Recording = nil
				wantStatus = "incomplete"
			case "changed decision":
				record.Detection.Segments = []segment.Segment{{Start: 10, End: 20}}
				wantStatus = "mismatch"
			}
			inputPath := filepath.Join(input, "show", "episode", "render.m4a.json")
			if err := experiment.Write(inputPath, record); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(inputPath)
			if err != nil {
				t.Fatal(err)
			}
			err = invoke(t, "-in", input, "-out", output)
			if (err == nil) != (wantStatus == "complete") {
				t.Fatalf("status %s: unexpected error %v", wantStatus, err)
			}
			data, err := os.ReadFile(filepath.Join(output, "replay-summary.json"))
			if err != nil {
				t.Fatal(err)
			}
			var outcomes []struct{ Status string }
			if err := json.Unmarshal(data, &outcomes); err != nil {
				t.Fatal(err)
			}
			if len(outcomes) != 1 || outcomes[0].Status != wantStatus {
				t.Fatalf("unexpected outcomes: %s", data)
			}
			outputPath := filepath.Join(output, "show", "episode", "render.m4a.json")
			data, err = os.ReadFile(outputPath)
			if wantStatus != "complete" {
				if !os.IsNotExist(err) {
					t.Fatal("failed replay wrote a scorable episode")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var saved map[string]json.RawMessage
				if err := json.Unmarshal(data, &saved); err != nil {
					t.Fatal(err)
				}
				if _, ok := saved["keep"]; ok {
					t.Fatal("replay presented stale audio keep ranges as rendered output")
				}
			}
			if err := invoke(t, "-in", input, "-out", output); err == nil {
				t.Fatal("existing output directory accepted")
			}
			after, err := os.ReadFile(inputPath)
			if err != nil || string(before) != string(after) {
				t.Fatal("replay changed its input")
			}
		})
	}
}
