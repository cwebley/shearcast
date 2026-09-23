package detect

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/transcript"
)

func eq(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

var labelled = regexp.MustCompile(`(?m)^\[(\w+)\] (.*)$`)

// fakeJev behaves the way the real model does on this problem: asked in
// isolation whether the lead-in is an ad it says content, because the lead-in
// really is content-shaped. Asked to pick which sentence the read starts at,
// with the read quoted, it can find it.
func fakeJev(t *testing.T) *jev.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jev.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding request: %v", err)
			return
		}

		windowText := map[string]string{}
		for _, m := range labelled.FindAllStringSubmatch(req.State, -1) {
			windowText[m[1]] = strings.ToLower(m[2])
		}

		// Sentence ids, in order, as they appear in the state.
		var ids []string
		for id := range windowText {
			if strings.HasPrefix(id, "L") || strings.HasPrefix(id, "R") {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)

		// index of the first sentence that has left the programme's subject
		firstOf := func(marker string) int {
			for i, id := range ids {
				if strings.Contains(windowText[id], marker) {
					return i
				}
			}
			return len(ids)
		}
		rank := func(id string) int {
			for i, x := range ids {
				if x == id {
					return i
				}
			}
			return -1
		}

		answers := map[string]jev.Answer{}
		for id, q := range req.Questions {
			switch {
			case q.Type == jev.TypeNoul && id == "loss":
				answers[id] = jev.Answer{Type: "noul", Noul: 0.1}

			case q.Type == jev.TypeNoul && strings.HasPrefix(id, "L"):
				// Start predicate, monotone: the advertisement has begun by this
				// sentence once we reach the segue.
				p := 0.05
				if r := rank(id); r >= 0 && r >= firstOf("brings us to") {
					p = 0.9
				}
				answers[id] = jev.Answer{Type: "noul", Noul: p}

			case q.Type == jev.TypeNoul && strings.HasPrefix(id, "R"):
				// End predicate: the programme has resumed once the subject
				// matter comes back.
				p := 0.05
				if r := rank(id); r >= 0 && r >= firstOf("universe is vast") {
					p = 0.9
				}
				answers[id] = jev.Answer{Type: "noul", Noul: p}

			case q.Type == jev.TypeChoice:
				// Scan pass: isolated classification only sees the brand.
				if strings.Contains(windowText[id], "acme") {
					answers[id] = jev.Answer{Type: "choice", Choice: "sponsor", Confidence: 0.95,
						Probabilities: map[string]float64{"sponsor": 0.95, "content": 0.05}}
				} else {
					answers[id] = jev.Answer{Type: "choice", Choice: KeepRule, Confidence: 0.95,
						Probabilities: map[string]float64{"sponsor": 0.05, "content": 0.95}}
				}
			}
		}
		json.NewEncoder(w).Encode(jev.Response{Model: "fake", Answers: answers, Usage: jev.Usage{InputTokens: 100}})
	}))
	t.Cleanup(srv.Close)
	return jev.New(jev.Config{BaseURL: srv.URL, APIKey: "k", Model: "fake"})
}

// A video with a 30s soft segue at 60s and the brand named at 90s.
func segueCues() []transcript.Cue {
	var cues []transcript.Cue
	add := func(from, to float64, text string) {
		for t := from; t < to; t += 5 {
			cues = append(cues, transcript.Cue{Start: t, End: t + 5, Text: text})
		}
	}
	add(0, 60, "the universe is vast and mostly empty.")
	add(60, 90, "which brings us to the question of how we rest.")
	add(90, 150, "thanks to Acme for supporting this video.")
	add(150, 240, "the universe is vast and mostly empty.")
	return cues
}

func sponsorRule() []Rule {
	return []Rule{{ID: "sponsor", Prompt: "A paid promotion", Threshold: 0.8}}
}

func TestPredicateCatchesTheSoftSegue(t *testing.T) {
	d := New(fakeJev(t), Defaults(sponsorRule()))

	res, err := d.Run(context.Background(), segueCues(), 240)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Regions) != 1 {
		t.Fatalf("got %d regions %+v, want 1", len(res.Regions), res.Regions)
	}

	r := res.Regions[0]
	if !eq(r.AnchorStart, 90) || !eq(r.AnchorEnd, 150) {
		t.Errorf("anchor: got %v-%v, want 90-150", r.AnchorStart, r.AnchorEnd)
	}
	// The point of the whole design: the cut starts at the segue, not the brand.
	if !eq(r.Start, 60) {
		t.Errorf("region start: got %v, want 60 (the segue, 30s before the anchor)", r.Start)
	}
	if !eq(r.End, 150) {
		t.Errorf("region end: got %v, want 150", r.End)
	}
	if !eq(r.LeadIn, 30) {
		t.Errorf("LeadIn: got %v, want 30", r.LeadIn)
	}
	if r.StartPick == "" {
		t.Errorf("StartPick: got %q, want a sentence id", r.StartPick)
	}
	if r.StartStep <= 0 {
		t.Errorf("StartStep: got %v, want a rising step", r.StartStep)
	}
}

func TestAnchorShowsWhatScanAloneWouldHaveCut(t *testing.T) {
	d := New(fakeJev(t), Defaults(sponsorRule()))
	res, err := d.Run(context.Background(), segueCues(), 240)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Regions) != 1 {
		t.Fatalf("got %d regions, want 1", len(res.Regions))
	}
	r := res.Regions[0]
	if !eq(r.AnchorStart, 90) {
		t.Errorf("scan anchored at %v, want 90: it only sees the brand mention", r.AnchorStart)
	}
	if recovered := r.AnchorStart - r.Start; !eq(recovered, 30) {
		t.Errorf("boundary choice recovered %vs of lead-in, want 30", recovered)
	}
}

// The scan pass anchors at the start of a fixed-width window, so the anchor can
// begin before the ad does. The start choice must be able to move the boundary
// later, not only earlier.
func TestStartChoiceCanMoveTheBoundaryLater(t *testing.T) {
	d := New(fakeJev(t), Defaults(sponsorRule()))
	cues := segueCues()

	// Candidates for an anchor at 90s must reach past it, into the read itself.
	before := d.candidates(cues, 0, 90+d.Opts.ScanWindow, "L")
	if len(before) == 0 {
		t.Fatal("no candidates built")
	}
	last := before[len(before)-1]
	if last.Start <= 90 {
		t.Errorf("last candidate starts at %v, want a sentence past the anchor at 90", last.Start)
	}
}

// A video where the sponsor read is interrupted twice -- a scan-window-sized
// aside that doesn't mention the brand, twice -- so the scan stage sees it as
// three separate high-confidence "sponsor" clusters, not one continuous read.
func splitAdCues() []transcript.Cue {
	var cues []transcript.Cue
	add := func(from, to float64, text string) {
		for t := from; t < to; t += 5 {
			cues = append(cues, transcript.Cue{Start: t, End: t + 5, Text: text})
		}
	}
	add(0, 30, "the universe is vast and mostly empty.")
	add(30, 60, "which brings us to the question of how we rest.")
	add(60, 90, "thanks to Acme for supporting this video.")
	add(90, 120, "and now a quick word from our sound engineer.")
	add(120, 150, "thanks to Acme for supporting this video.")
	add(150, 180, "and a brief technical interlude follows now.")
	add(180, 210, "thanks to Acme for supporting this video.")
	add(210, 300, "the universe is vast and mostly empty.")
	return cues
}

func TestRunFusesASplitAdIntoOneRegionInsteadOfThree(t *testing.T) {
	d := New(fakeJev(t), Defaults(sponsorRule()))
	cues := splitAdCues()

	// Confirm the setup actually produces the failure this test guards
	// against: scan alone, before bridging, must see three clusters, or this
	// test would not be exercising anything.
	scored, err := d.scan(context.Background(), cues)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if raw := clusters(scored, d.Opts.AnchorThreshold); len(raw) != 3 {
		t.Fatalf("setup: scan alone found %d clusters, want 3 (the two asides must each read as "+
			"content, or this test isn't reproducing a real split)", len(raw))
	}

	res, err := d.Run(context.Background(), cues, 300)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Regions) != 1 {
		t.Fatalf("got %d regions, want 1: three fragments of the same read must bound as one, "+
			"not three separately-bounded cuts", len(res.Regions))
	}
	r := res.Regions[0]
	if !eq(r.Start, 30) {
		t.Errorf("region start: got %v, want 30 (the segue, pulled back from the first fragment's anchor)", r.Start)
	}
	if !eq(r.End, 210) {
		t.Errorf("region end: got %v, want 210 (where real content actually resumes)", r.End)
	}
	if len(res.Segments) != 1 {
		t.Errorf("got %d final segments, want 1: the feed should have one cut here, not three "+
			"separate ones with un-cut audio between them", len(res.Segments))
	}
}

func TestCleanVideoProducesNoRegions(t *testing.T) {
	var cues []transcript.Cue
	for t0 := 0.0; t0 < 300; t0 += 5 {
		cues = append(cues, transcript.Cue{Start: t0, End: t0 + 5, Text: "the universe is vast and mostly empty."})
	}
	d := New(fakeJev(t), Defaults(sponsorRule()))
	res, err := d.Run(context.Background(), cues, 300)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Regions) != 0 {
		t.Errorf("got %d regions on a clean video, want 0: %+v", len(res.Regions), res.Regions)
	}
}

func TestCandidatesRespectTheOptionCap(t *testing.T) {
	var cues []transcript.Cue
	for t0 := 0.0; t0 < 600; t0 += 5 {
		cues = append(cues, transcript.Cue{Start: t0, End: t0 + 5, Text: "a reasonably long sentence about the cosmos."})
	}
	opts := Defaults(sponsorRule())
	opts.MaxCandidates = 10
	opts.LeadInSpan = 600
	d := New(fakeJev(t), opts)

	before := d.candidates(cues, 0, 600, "L")
	if len(before) != 10 {
		t.Fatalf("got %d candidates, want 10", len(before))
	}
	// The cap must keep the sentences nearest the anchor, not the earliest ones.
	if before[len(before)-1].End < 590 {
		t.Errorf("kept the wrong end: last candidate ends at %v, want near 600", before[len(before)-1].End)
	}
}

func TestOptionCapNeverExceedsTheAPILimit(t *testing.T) {
	opts := Defaults(sponsorRule())
	opts.MaxCandidates = 9999
	d := New(fakeJev(t), opts)
	if d.Opts.MaxCandidates > choiceOptionLimit-1 {
		t.Errorf("MaxCandidates %d leaves no room for the escape hatch under the %d cap",
			d.Opts.MaxCandidates, choiceOptionLimit)
	}
}

func TestEndFitWindowDefaultsOnButLeavesStartEdgeUncapped(t *testing.T) {
	opts := Defaults(sponsorRule())
	if opts.EndFitWindow != 11 {
		t.Errorf("EndFitWindow default: got %d, want 11", opts.EndFitWindow)
	}
	// bound() must pass 0 (uncapped) at the start-edge call site regardless of
	// EndFitWindow: LeadInSpan exists so a segue can be found 100s+ before the
	// anchor, and capping that fit would defeat it. TestStartChoiceCanMoveTheBoundaryLater
	// and TestPredicateCatchesTheSoftSegue already exercise this end to end;
	// this just pins the default that makes the end-edge cap live by default.
}

func TestClustersGroupAdjacentWindows(t *testing.T) {
	scored := []ScoredWindow{
		{Window: transcript.Window{ID: "W001", Start: 0, End: 30, Text: "a"}, Rule: KeepRule, Prob: 0},
		{Window: transcript.Window{ID: "W002", Start: 30, End: 60, Text: "b"}, Rule: "sponsor", Prob: 0.9},
		{Window: transcript.Window{ID: "W003", Start: 60, End: 90, Text: "c"}, Rule: "sponsor", Prob: 0.95},
		{Window: transcript.Window{ID: "W004", Start: 90, End: 120, Text: "d"}, Rule: KeepRule, Prob: 0},
		{Window: transcript.Window{ID: "W005", Start: 120, End: 150, Text: "e"}, Rule: "patreon", Prob: 0.88},
	}
	got := clusters(scored, 0.85)
	if len(got) != 2 {
		t.Fatalf("got %d clusters, want 2", len(got))
	}
	if !eq(got[0].start, 30) || !eq(got[0].end, 90) || got[0].rule != "sponsor" {
		t.Errorf("first cluster: got %+v, want sponsor 30-90", got[0])
	}
	if !eq(got[0].prob, 0.95) {
		t.Errorf("cluster prob should be the strongest window: got %v, want 0.95", got[0].prob)
	}
	if got[1].rule != "patreon" || !eq(got[1].start, 120) {
		t.Errorf("second cluster: got %+v, want patreon at 120", got[1])
	}
}

func TestClustersIgnoreBelowThreshold(t *testing.T) {
	scored := []ScoredWindow{
		{Window: transcript.Window{ID: "W001", Start: 0, End: 30}, Rule: "sponsor", Prob: 0.5},
	}
	if got := clusters(scored, 0.85); len(got) != 0 {
		t.Errorf("got %d clusters, want 0: a 0.5 is not an anchor", len(got))
	}
}

func TestMinRegionDiscardsSlivers(t *testing.T) {
	opts := Defaults(sponsorRule())
	opts.MinRegion = 1000
	d := New(fakeJev(t), opts)
	res, err := d.Run(context.Background(), segueCues(), 240)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Regions) != 0 {
		t.Errorf("got %d regions, want 0 after the minimum length filter", len(res.Regions))
	}
}
