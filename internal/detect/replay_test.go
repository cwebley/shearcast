package detect

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/transcript"
)

func TestRecordedEpisodeReplaysProductionDecisions(t *testing.T) {
	d := New(fakeJev(t), Defaults(sponsorRule()))
	live, recording, err := d.RunRecorded(context.Background(), segueCues(), 240)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the persisted representation, not just the in-memory record.
	data, err := json.Marshal(recording)
	if err != nil {
		t.Fatal(err)
	}
	var saved Recording
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	replayed, err := Replay(context.Background(), &saved, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Regions) != 1 || replayed.Regions[0].Start != 60 || replayed.Regions[0].End != 150 {
		t.Fatalf("want the full 60-150s promotion, got %+v", replayed.Regions)
	}
	live.Stats = jev.Stats{}
	if !reflect.DeepEqual(live, replayed) {
		t.Fatal("replay differs from the recorded production result")
	}
}

func TestNeutralContextIsRecordedAndRequiredForReplay(t *testing.T) {
	opts := Defaults(sponsorRule())
	opts.Subject = "How the universe works"
	opts.NeutralContext = true
	live, recording, err := New(fakeJev(t), opts).RunRecorded(context.Background(), segueCues(), 240)
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Regions) != 1 || live.Regions[0].Start != 60 || live.Regions[0].End != 150 {
		t.Fatalf("want the full 60-150s promotion, got %+v", live.Regions)
	}
	r := live.Regions[0]
	if !strings.Contains(r.StartState, "Title: How the universe works") ||
		!strings.Contains(r.StartState, "may still include unwanted material") ||
		!strings.Contains(r.StartState, "thanks to Acme for supporting this video.") ||
		!strings.Contains(r.StartState, "the universe is vast and mostly empty.") ||
		!strings.Contains(r.EndState, "MAY INCLUDE BOTH UNWANTED MATERIAL AND PROGRAM CONTENT") {
		t.Fatalf("missing neutral context or source text: start=%q end=%q", r.StartState, r.EndState)
	}
	if strings.Contains(r.StartState, "video's own narration") || strings.Contains(r.EndState, "ADVERTISEMENT ALREADY UNDERWAY") {
		t.Fatal("uncertain context still described as confirmed content or an advertisement")
	}
	data, err := json.Marshal(recording)
	if err != nil {
		t.Fatal(err)
	}
	var saved Recording
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Options.NeutralContext {
		t.Fatal("recording lost the context experiment setting")
	}
	replayed, err := Replay(context.Background(), &saved, nil)
	if err != nil {
		t.Fatal(err)
	}
	live.Stats = jev.Stats{}
	if !reflect.DeepEqual(live, replayed) {
		t.Fatal("neutral-context replay differs from production")
	}
	wrong := saved.Options
	wrong.NeutralContext = false
	if result, err := Replay(context.Background(), &saved, &wrong); result != nil || !errors.Is(err, ErrIncompleteReplay) {
		t.Fatalf("changed context must need new evidence, got result %v, error %v", result, err)
	}
}

func TestReplayRequiresEvidenceForNewBranchesAndEveryRepeat(t *testing.T) {
	opts := Defaults(sponsorRule())
	opts.StartWeights = &Weights{Bias: -5, Weights: map[string]float64{"lex_thanks": 10}}
	_, recording, err := New(fakeJev(t), opts).RunRecorded(context.Background(), segueCues(), 240)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name string
		edit func(*Options)
	}{
		{"new sentence fallback", func(o *Options) { o.StartWeights = &Weights{Bias: -5} }},
		{"unrecorded repeat", func(o *Options) { o.Repeats++ }},
		{"changed prompt context", func(o *Options) { o.Subject = "a different subject" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			candidate := recording.Options
			change.edit(&candidate)
			result, err := Replay(context.Background(), recording, &candidate)
			if !errors.Is(err, ErrIncompleteReplay) || result != nil {
				t.Fatalf("want an incomplete replay, got result %v, error %v", result, err)
			}
		})
	}
	// A failed candidate must not consume or mutate the original recording.
	if _, err := Replay(context.Background(), recording, nil); err != nil {
		t.Fatal(err)
	}
}

func TestReplayIncludesProductionFallbacksAndRegionFiltering(t *testing.T) {
	lateRead := tailCues()
	for i := range lateRead {
		s := &lateRead[i]
		s.Text = "the universe is vast and mostly empty."
		if s.Start >= 120 && s.Start < 130 {
			s.Text = "thanks to Acme for supporting this video."
		}
		if s.Start >= 140 && s.Start < 150 {
			s.Text = "Acme is available at example.com today."
		}
	}
	for _, tc := range []struct {
		name      string
		cues      []transcript.Cue
		duration  float64
		configure func(*Options)
		start     float64
		end       float64
		pick      string
	}{
		{"fitted start", segueCues(), 240, func(o *Options) {
			o.StartWeights = &Weights{Bias: -5, Weights: map[string]float64{"lex_thanks": 10}}
		}, 90, 150, "L"},
		{"sentence fallback", segueCues(), 240, func(o *Options) {
			o.StartWeights = &Weights{Bias: -5}
		}, 90, 150, "classified"},
		{"localized guard after dropped region", segueCues(), 240, func(o *Options) {
			o.MinRegion, o.LocalizedMinStep = 1000, 1.1
		}, 90, 150, ""},
		{"outro trim", segueCues()[:30], 150, func(o *Options) {}, 90, 150, "trimmed"},
		{"end extension", tailCues(), 300, func(o *Options) {
			o.EndWeights = &Weights{Bias: -5, Weights: map[string]float64{"lex_url": 10}}
		}, 90, 165, ""},
		{"ambiguous end", segueCues(), 240, func(o *Options) {
			o.EndMinStep = 2
			o.EndWeights = &Weights{Bias: -5, Weights: map[string]float64{"lex_thanks": 10}}
		}, 60, 150, "L"},
		{"ambiguous end uses refined start", lateRead, 300, func(o *Options) {
			o.StartWeights = &Weights{Bias: -5, Weights: map[string]float64{"lex_url": 10}}
			o.EndWeights = &Weights{Bias: -5, Weights: map[string]float64{"lex_thanks": 10}}
			o.EndMinStep = 2
		}, 140, 150, "L"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := Defaults(sponsorRule())
			tc.configure(&opts)
			live, recording, err := New(fakeJev(t), opts).RunRecorded(context.Background(), tc.cues, tc.duration)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(recording)
			if err != nil {
				t.Fatal(err)
			}
			var saved Recording
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			replayed, err := Replay(context.Background(), &saved, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(replayed.Regions) != 1 {
				t.Fatalf("want one cut, got %+v", replayed.Regions)
			}
			region := replayed.Regions[0]
			if region.Start != tc.start || region.End != tc.end || !strings.HasPrefix(region.StartPick, tc.pick) {
				t.Fatalf("want %v-%v (%s), got %+v", tc.start, tc.end, tc.pick, region)
			}
			live.Stats = jev.Stats{}
			if !reflect.DeepEqual(live, replayed) {
				t.Fatal("replay differs from production")
			}
			candidateOptions := saved.Options
			candidateOptions.ConservativeAmbiguousEnd = true
			candidate, err := Replay(context.Background(), &saved, &candidateOptions)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(replayed, candidate) {
				t.Fatal("conservative end fallback changed an established boundary or filtering decision")
			}
		})
	}
}

type leadInOnlyModel struct{ ModelClient }

func (m leadInOnlyModel) AskAll(ctx context.Context, batches []jev.Batch, parallel int) (map[string]jev.Answer, error) {
	answers, err := m.ModelClient.AskAll(ctx, batches, parallel)
	if err != nil {
		return nil, err
	}
	for _, batch := range batches {
		for id, question := range batch.Questions {
			if strings.HasPrefix(id, "leads_into_ad|") && strings.Contains(question.Instructions, "brings us") {
				answers[id] = jev.Answer{Type: "noul", Noul: 1}
			}
		}
	}
	return answers, nil
}

func TestReplayRejectsAnEarlierStartWhenTheCurveDoesNotRecognizeTheRead(t *testing.T) {
	opts := Defaults(sponsorRule())
	opts.StartWeights = &Weights{Bias: -4, Weights: map[string]float64{"leads_into_ad": 1}}
	live, recording, err := New(leadInOnlyModel{fakeJev(t)}, opts).RunRecorded(context.Background(), segueCues(), 240)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Replay(context.Background(), recording, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed.Regions) != 1 || replayed.Regions[0].Start != 90 || !strings.HasPrefix(replayed.Regions[0].StartPick, "classified") {
		t.Fatalf("want classification fallback at the confirmed read, got %+v", replayed.Regions)
	}
	live.Stats = jev.Stats{}
	if !reflect.DeepEqual(live, replayed) {
		t.Fatal("replay differs from production")
	}
}
