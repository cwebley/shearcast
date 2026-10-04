package detect

import (
	"context"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/transcript"
)

// The last scan window includes closing discussion. The return predicate is
// indecisive, while the end features score those sentences as content.
type ambiguousClosingModel struct{ ModelClient }

func (m ambiguousClosingModel) Ask(ctx context.Context, state string, questions map[string]jev.Question) (*jev.Response, error) {
	response, err := m.ModelClient.Ask(ctx, state, questions)
	if err == nil {
		for id := range questions {
			if strings.HasPrefix(id, "R") {
				response.Answers[id] = jev.Answer{Type: "noul", Noul: 0.55}
			}
		}
	}
	return response, err
}

func TestConservativeAmbiguousEndKeepsUnsupportedAndShortCuts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*Options, []transcript.Cue)
		start     float64
	}{
		{"missing end weights", func(o *Options, _ []transcript.Cue) { o.EndWeights = nil }, 90},
		{"disputed ad scores", func(o *Options, _ []transcript.Cue) { o.EndWeights = &Weights{} }, 90},
		{"isolated low score before disputed sentence", func(o *Options, cues []transcript.Cue) {
			o.EndWeights = &Weights{Bias: -5, Weights: map[string]float64{"lex_url": 5}}
			cues[34].Text = "The universe is vast and mostly empty, see example.com."
		}, 90},
		{"end would leave a five-second cut", func(o *Options, cues []transcript.Cue) {
			o.StartWeights = &Weights{Bias: -5, Weights: map[string]float64{"lex_url": 10}}
			cues[29].Text = "Acme is available at example.com today."
		}, 145},
		{"low candidates precede refined start", func(o *Options, cues []transcript.Cue) {
			o.AnchorInteriorSpan = 90
			o.StartWeights = &Weights{Bias: -5, Weights: map[string]float64{"lex_url": 10}}
			cues[31].Text = "Acme is available at example.com today."
		}, 155},
		{"ad seed still runs through final read", func(o *Options, cues []transcript.Cue) {
			o.EndWeights = &Weights{Bias: -5, Weights: map[string]float64{"lex_thanks": 10}}
			for i := 30; i < len(cues); i++ {
				cues[i].Text = "Thanks to Acme for supporting this video."
			}
		}, 90},
		{"extension floor has no low-ad gap", func(o *Options, _ []transcript.Cue) { o.EndExtendFloor = 0.5 }, 90},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := Defaults(sponsorRule())
			opts.WeakAnchorThreshold, opts.OutroTrimFloor = 1, 2
			opts.StartWeights = &Weights{Bias: -5, Weights: map[string]float64{"lex_thanks": 10}}
			opts.EndWeights = &Weights{Bias: -5}
			cues := segueCues()[:36]
			tc.configure(&opts, cues)
			live, recording, err := New(ambiguousClosingModel{fakeJev(t)}, opts).RunRecorded(context.Background(), cues, 180)
			if err != nil {
				t.Fatal(err)
			}
			opts.ConservativeAmbiguousEnd = true
			candidate, err := Replay(context.Background(), recording, &opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(candidate.Regions) != 1 || len(candidate.Dropped) != 0 || candidate.Regions[0].Start != tc.start || candidate.Regions[0].End != 180 {
				t.Fatalf("want unchanged %v-180s cut, got %+v", tc.start, candidate.Regions)
			}
			if len(live.Regions) != 1 || live.Regions[0].EndReason != candidate.Regions[0].EndReason {
				t.Fatal("unsupported fallback changed its reason")
			}
		})
	}
}

func (m ambiguousClosingModel) AskAll(ctx context.Context, batches []jev.Batch, parallel int) (map[string]jev.Answer, error) {
	answers, err := m.ModelClient.AskAll(ctx, batches, parallel)
	if err == nil {
		for _, batch := range batches {
			if _, ok := batch.Questions["W006"]; ok {
				answers["W006"] = jev.Answer{Type: "choice", Choice: "sponsor",
					Probabilities: map[string]float64{"sponsor": 0.95, "content": 0.05}}
			}
		}
	}
	return answers, err
}

func TestConservativeAmbiguousEndPreservesClosingContentOnRecordedEvidence(t *testing.T) {
	opts := Defaults(sponsorRule())
	opts.WeakAnchorThreshold = 1
	opts.StartWeights = &Weights{Bias: -5, Weights: map[string]float64{"lex_thanks": 10}}
	opts.EndWeights = &Weights{Bias: -5}
	live, recording, err := New(ambiguousClosingModel{fakeJev(t)}, opts).RunRecorded(context.Background(), segueCues()[:36], 180)
	if err != nil {
		t.Fatal(err)
	}
	if len(live.Regions) != 1 || live.Regions[0].Start != 90 || live.Regions[0].End != 180 {
		t.Fatalf("baseline must cut the closing discussion from 150-180s, got %+v", live.Regions)
	}
	opts.ConservativeAmbiguousEnd = true
	candidate, err := Replay(context.Background(), recording, &opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.Regions) != 1 || len(candidate.Dropped) != 0 || candidate.Regions[0].Start != 90 || candidate.Regions[0].End != 150 {
		t.Fatalf("want the confirmed 90-150s promo with discussion preserved, got %+v", candidate.Regions)
	}
	if !strings.Contains(candidate.Regions[0].EndReason, "low-ad anchor tail") || candidate.Stats.Calls != 0 {
		t.Fatalf("missing reason or nonzero model usage: %+v", candidate)
	}
	baseline, err := Replay(context.Background(), recording, nil)
	if err != nil || len(baseline.Regions) != 1 || baseline.Regions[0].End != 180 {
		t.Fatalf("opt-in replay changed the baseline: result=%+v error=%v", baseline, err)
	}
}
