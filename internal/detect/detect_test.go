package detect

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/transcript"
)

func eq(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

var labeled = regexp.MustCompile(`(?m)^\[(\w+)\] (.*)$`)

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
		for _, m := range labeled.FindAllStringSubmatch(req.State, -1) {
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

		// index of the first sentence that has left the program's subject
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
				// End predicate: the program has resumed once the subject
				// matter comes back.
				p := 0.05
				if r := rank(id); r >= 0 && r >= firstOf("universe is vast") {
					p = 0.9
				}
				answers[id] = jev.Answer{Type: "noul", Noul: p}

			case q.Type == jev.TypeNoul:
				// Fitted feature questions: none holds.
				answers[id] = jev.Answer{Type: "noul", Noul: 0}

			case q.Type == jev.TypeChoice:
				// Scan pass: isolated classification only sees the brand.
				// A classified sentence's question carries its lead's
				// prefix ("C0-S001"); the state labels it "[S001]".
				key := id
				if i := strings.Index(id, "-"); i >= 0 {
					key = id[i+1:]
				}
				if strings.Contains(windowText[key], "acme") {
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

func TestEdgeFitDefaults(t *testing.T) {
	opts := Defaults(sponsorRule())
	if opts.EndFitWindow != 11 {
		t.Errorf("EndFitWindow default: got %d, want 11", opts.EndFitWindow)
	}
	if opts.StartFitSpan != 75 || opts.AnchorInteriorSpan != 60 || opts.StartFallbackFloor != 0.5 {
		t.Errorf("start defaults: got fit span %v, interior %v, floor %v; want 75, 60, 0.5",
			opts.StartFitSpan, opts.AnchorInteriorSpan, opts.StartFallbackFloor)
	}
}

// sentencesAt builds one candidate per start time, in order.
func sentencesAt(starts ...float64) []transcript.Sentence {
	ss := make([]transcript.Sentence, len(starts))
	for i, t0 := range starts {
		ss[i] = transcript.Sentence{ID: fmt.Sprintf("L%03d", i+1), Start: t0, End: t0 + 3}
	}
	return ss
}

// Measured on Hank Green's MYa4vLcpBqg: an earlier shirt teaser three minutes
// before the anchor, then a plug starting 26s into the anchor. Fitted over the
// whole lead-in, the teaser won and the step fell, so the cut kept the anchor's
// window edge and took 26s of a real answer with it.
func TestStartEdgeIgnoresABumpBeyondTheFitSpan(t *testing.T) {
	d := New(fakeJev(t), Defaults(sponsorRule()))
	anchor := 1006.8
	cands := sentencesAt(825, 840, 842, 845, 849, 900, 950, 990, 1010, 1020, 1030, 1033, 1036, 1040, 1045)
	curve := []float64{0.03, 0.69, 0.77, 0.71, 0.56, 0.1, 0.07, 0.05, 0.06, 0.02, 0.02, 0.26, 0.64, 0.8, 0.85}
	idx, step, ok := d.startEdge(curve, cands, anchor)
	// The plug's first sentence scores only 0.26, so either of its first two
	// sentences is a right answer; anything earlier cuts the real answer.
	if !ok || cands[idx].Start < 1033 || cands[idx].Start > 1036 {
		t.Fatalf("got idx %d (ok %v), want the plug at 1033 or 1036", idx, ok)
	}
	if step <= 0 {
		t.Errorf("step: got %v, want a rise", step)
	}
}

func TestStartEdgeFallsBackInsideTheAnchor(t *testing.T) {
	d := New(fakeJev(t), Defaults(sponsorRule()))
	// A falling curve: no rise anywhere in the fit span.
	cands := sentencesAt(80, 90, 100, 110, 120, 130)
	curve := []float64{0.9, 0.8, 0.3, 0.2, 0.6, 0.7}
	idx, step, ok := d.startEdge(curve, cands, 105)
	if !ok || cands[idx].Start != 120 || step != 0 {
		t.Errorf("got idx %d step %v ok %v, want the first sentence inside the anchor at or above 0.5 (120), no step", idx, step, ok)
	}
}

func TestStartEdgeKeepsTheAnchorWithNothingToFallTo(t *testing.T) {
	d := New(fakeJev(t), Defaults(sponsorRule()))
	cands := sentencesAt(80, 90, 100, 110, 120)
	curve := []float64{0.9, 0.8, 0.3, 0.2, 0.1}
	if _, _, ok := d.startEdge(curve, cands, 105); ok {
		t.Error("got an edge, want the anchor's own start")
	}
}

// A strong anchor bounded to a sliver gets its sentences checked one by one,
// and a confident run survives as a localized region instead of being
// dropped with it.
func TestShortScanRegionIsLocalizedNotDropped(t *testing.T) {
	opts := Defaults(sponsorRule())
	opts.MinRegion = 1000
	res, err := New(fakeJev(t), opts).Run(context.Background(), segueCues(), 240)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Dropped) == 0 {
		t.Fatal("nothing dropped, want the scan region dropped under MinRegion")
	}
	if len(res.Regions) != 1 || !res.Regions[0].Localized {
		t.Fatalf("got %+v, want one localized region", res.Regions)
	}
	// The segue at 60 is a tall step, which may move a localized start.
	if r := res.Regions[0]; r.Start < 60 || r.Start > 90 || r.End > 150 {
		t.Errorf("got %.0f-%.0f, want the Acme read at 90-150, from its segue at 60 at the earliest", r.Start, r.End)
	}
}

func TestRecognizesReadLooksOnlyInsideTheAnchor(t *testing.T) {
	d := New(fakeJev(t), Defaults(sponsorRule()))
	cands := sentencesAt(80, 90, 100, 110, 120)
	// A bump before the anchor at 100, nothing at or after it: the curve
	// doesn't recognize the read, as with a break bumper.
	if d.recognizesRead([]float64{0.1, 0.6, 0.08, 0.1, 0.2}, cands, 100) {
		t.Error("recognized a read from a bump before the anchor")
	}
	if !d.recognizesRead([]float64{0.1, 0.2, 0.1, 0.7, 0.9}, cands, 100) {
		t.Error("missed a read scoring 0.7 inside the anchor")
	}
}

func TestLocalizedStartStaysInsideItsRun(t *testing.T) {
	d := New(fakeJev(t), Defaults(sponsorRule()))
	c := cluster{start: 100, end: 110, localized: true}
	for _, tc := range []struct {
		start, step float64
		want        bool
	}{
		{105, 0, true},    // inside the run
		{110, 0.9, false}, // at its end: nothing left to cut
		{120, 0.9, false}, // past it
		{80, 0.06, false}, // earlier on a shallow step
		{95, 0.52, true},  // earlier on a real one
	} {
		if got := d.localizedStartMove(tc.start, tc.step, c); got != tc.want {
			t.Errorf("move to %v on step %v: got %v, want %v", tc.start, tc.step, got, tc.want)
		}
	}
}

// An outro's start moves later past wrap-up talk to the first confident
// sentence, and never earlier.
func TestTrimOutroOnlyMovesTheStartLater(t *testing.T) {
	var cues []transcript.Cue
	add := func(from, to float64, text string) {
		for t := from; t < to; t += 5 {
			cues = append(cues, transcript.Cue{Start: t, End: t + 5, Text: text})
		}
	}
	add(0, 60, "the universe is vast and mostly empty.")
	add(60, 80, "that is it for today, stay curious everyone.")
	add(80, 100, "thanks to acme for supporting this video.")

	d := New(fakeJev(t), Defaults(sponsorRule()))
	c := cluster{rule: "sponsor", start: 70, end: 100}
	if !d.isOutro(cues, c) {
		t.Fatal("a read ending the video isn't an outro")
	}
	region := Region{Start: 60, End: 100}
	if err := d.trimOutro(context.Background(), cues, c, &region); err != nil {
		t.Fatal(err)
	}
	if region.Start != 80 || len(region.Trim) == 0 {
		t.Errorf("start %v, want the sponsor line at 80 with the trim recorded", region.Start)
	}

	region = Region{Start: 85, End: 100}
	if err := d.trimOutro(context.Background(), cues, c, &region); err != nil {
		t.Fatal(err)
	}
	if region.Start != 85 {
		t.Errorf("start %v, want 85 kept: a trim never moves earlier", region.Start)
	}
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
	opts.LocalizedMinRegion = 1000
	d := New(fakeJev(t), opts)
	res, err := d.Run(context.Background(), segueCues(), 240)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Regions) != 0 {
		t.Errorf("got %d regions, want 0 after the minimum length filter", len(res.Regions))
	}
}

// A window whose vote is split between two related rules shows only the
// winner in Rule and Prob. Probs keeps every choice, so a saved render record
// can show the split.
func TestScanRecordsEveryRuleProbability(t *testing.T) {
	d := New(fakeJev(t), Defaults(sponsorRule()))
	scored, err := d.scan(context.Background(), segueCues())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, sw := range scored {
		if len(sw.Probs) != 2 {
			t.Fatalf("%s: got probs %v, want sponsor and %s", sw.ID, sw.Probs, KeepRule)
		}
		if !eq(sw.Probs[sw.Rule], sw.Prob) && sw.Rule != KeepRule {
			t.Errorf("%s: Probs[%s]=%v disagrees with Prob=%v", sw.ID, sw.Rule, sw.Probs[sw.Rule], sw.Prob)
		}
		if !eq(sw.Probs["sponsor"]+sw.Probs[KeepRule], 1) {
			t.Errorf("%s: probs %v do not sum to 1", sw.ID, sw.Probs)
		}
	}
}

// A read whose tail (a web address, spoken) the end predicate already counts as the
// program returning, because it opens on the program's subject.
func tailCues() []transcript.Cue {
	var cues []transcript.Cue
	add := func(from, to float64, text string) {
		for t := from; t < to; t += 5 {
			cues = append(cues, transcript.Cue{Start: t, End: t + 5, Text: text})
		}
	}
	add(0, 90, "the universe is vast and mostly empty.")
	add(90, 150, "thanks to Acme for supporting this video.")
	add(150, 165, "the universe is vast, so visit example slash deals today.")
	add(165, 300, "the universe is vast and mostly empty.")
	return cues
}

func TestEndWeightsExtendPastTheTail(t *testing.T) {
	run := func(w *Weights) Region {
		t.Helper()
		opts := Defaults(sponsorRule())
		opts.EndWeights = w
		res, err := New(fakeJev(t), opts).Run(context.Background(), tailCues(), 300)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(res.Regions) != 1 {
			t.Fatalf("got %d regions, want 1", len(res.Regions))
		}
		return res.Regions[0]
	}

	if r := run(nil); !eq(r.End, 150) {
		t.Fatalf("setup: predicate alone ends at %v, want 150 (it should read the tail as returned)", r.End)
	}
	urls := &Weights{Bias: -5, Weights: map[string]float64{"lex_url": 10}}
	r := run(urls)
	if !eq(r.End, 165) {
		t.Errorf("with end weights: end %v, want 165, past the web address", r.End)
	}
	if !strings.Contains(r.EndReason, "extended 3") {
		t.Errorf("EndReason %q, want it to say the edge was extended by 3", r.EndReason)
	}
	for _, p := range r.EndCurve {
		if p.Start >= 150 && p.Start < 165 && p.Ad < 0.8 {
			t.Errorf("tail sentence at %v has Ad %v, want it recorded as ad", p.Start, p.Ad)
		}
	}
}

// An outro that asks for a subscription and then credits the producers splits
// its vote between two rules. Neither clears the threshold alone, but the
// window is plainly not content.
func TestClustersAnchorOnSplitVotes(t *testing.T) {
	split := func(id string, start float64, selfpromo, credits float64) ScoredWindow {
		return ScoredWindow{
			Window: transcript.Window{ID: id, Start: start, End: start + 30},
			Rule:   "selfpromo", Prob: selfpromo + credits,
			Probs: map[string]float64{KeepRule: 1 - selfpromo - credits, "selfpromo": selfpromo, "credits": credits},
		}
	}
	got := clusters([]ScoredWindow{
		split("W1", 0, 0.02, 0.01),
		split("W2", 30, 0.50, 0.45),
		split("W3", 60, 0.30, 0.65),
		split("W4", 90, 0.35, 0.60),
	}, 0.85)
	if len(got) != 1 {
		t.Fatalf("got %d clusters %+v, want 1", len(got), got)
	}
	if !eq(got[0].start, 30) || !eq(got[0].end, 120) {
		t.Errorf("cluster %v-%v, want 30-120", got[0].start, got[0].end)
	}
	if got[0].rule != "credits" {
		t.Errorf("rule %q, want credits, which has the most probability across the run", got[0].rule)
	}
}

func TestWeakLeadsSkipWindowsNextToStrongClusters(t *testing.T) {
	win := func(start, p float64) ScoredWindow {
		return ScoredWindow{Window: transcript.Window{Start: start, End: start + 30}, Rule: "sponsor", Prob: p}
	}
	got := weakLeads([]ScoredWindow{
		win(0, 0.1),
		win(30, 0.6), // a lead
		win(60, 0.7), // same lead
		win(90, 0.2),
		win(120, 0.6), // next to a strong window: the edge stage covers it
		win(150, 0.95),
		win(180, 0.4),
	}, 0.5, 0.85)
	if len(got) != 1 || !eq(got[0].start, 30) || !eq(got[0].end, 90) {
		t.Fatalf("got %+v, want one lead 30-90", got)
	}
}

func TestMissingBoundaryAnswersFail(t *testing.T) {
	// Scan answers are fine; every boundary answer is missing. Read as zeros
	// they would extend the cut over content to the end of the video.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jev.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		texts := map[string]string{}
		for _, m := range labeled.FindAllStringSubmatch(req.State, -1) {
			texts[m[1]] = strings.ToLower(m[2])
		}
		answers := map[string]jev.Answer{}
		for id, q := range req.Questions {
			if q.Type != jev.TypeChoice {
				continue
			}
			key := id
			if i := strings.Index(id, "-"); i >= 0 {
				key = id[i+1:]
			}
			p, choice := .05, KeepRule
			if strings.Contains(texts[key], "acme") {
				p, choice = .95, "sponsor"
			}
			answers[id] = jev.Answer{Type: "choice", Choice: choice, Probabilities: map[string]float64{"sponsor": p, KeepRule: 1 - p}}
		}
		json.NewEncoder(w).Encode(jev.Response{Answers: answers})
	}))
	defer srv.Close()
	d := New(jev.New(jev.Config{BaseURL: srv.URL, Model: "fake"}), Defaults(sponsorRule()))
	if res, err := d.Run(context.Background(), segueCues(), 240); err == nil {
		t.Fatalf("missing boundary answers accepted: %+v", res.Segments)
	}
}

func TestLocalizationRequestsAreBounded(t *testing.T) {
	var (
		mu             sync.Mutex
		requests, most int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jev.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests++
		most = max(most, len(req.Questions))
		mu.Unlock()
		answers := map[string]jev.Answer{}
		for id := range req.Questions {
			answers[id] = jev.Answer{Type: "choice", Choice: KeepRule, Probabilities: map[string]float64{"sponsor": 0, KeepRule: 1}}
		}
		json.NewEncoder(w).Encode(jev.Response{Answers: answers})
	}))
	defer srv.Close()
	d := New(jev.New(jev.Config{BaseURL: srv.URL, Model: "fake"}), Defaults(sponsorRule()))
	var cues []transcript.Cue
	for i := 0; i < 720; i++ {
		cues = append(cues, transcript.Cue{Start: float64(i * 5), End: float64((i + 1) * 5), Text: fmt.Sprintf("Distinct sentence number %d is about astronomy.", i)})
	}
	got, err := d.classify(context.Background(), cues, [][2]float64{{0, 3600}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0]) != 720 || most > maxLocalizeSentences || requests < 720/maxLocalizeSentences {
		t.Fatalf("%d sentences over %d requests, largest %d questions", len(got[0]), requests, most)
	}
}
