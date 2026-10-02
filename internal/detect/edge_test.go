package detect

import "testing"

func TestChangepointFindsACleanStep(t *testing.T) {
	ys := []float64{0, 0, 0, 0, 0.9, 0.9, 0.9, 0.9}
	if got := changepoint(ys); got != 4 {
		t.Errorf("got %d, want 4", got)
	}
}

func TestChangepointIgnoresASingleNoisyAnswer(t *testing.T) {
	// One spurious high answer well before the real edge. A threshold would
	// trip on it; a fitted step will not.
	ys := []float64{0, 0.85, 0, 0, 0, 0.9, 0.9, 0.9, 0.9, 0.9}
	if got := changepoint(ys); got != 5 {
		t.Errorf("got %d, want 5: one spike must not move the edge", got)
	}
}

// The case that beat max-jump on real data: the curve climbs in two stages, and
// the boundary is where it leaves the floor, not where it climbs fastest.
//
// Indices 5 and 6 score within 0.002 of each other here, because whether that
// first 0.3 belongs to the floor or to the rise is genuinely ambiguous. So this
// asserts the behavior that distinguishes the two estimators, not a winner on
// a tie: the edge must land at the foot of the climb, not at its steepest point.
func TestChangepointPrefersTheFloorExitOverTheSteepestRise(t *testing.T) {
	ys := []float64{0, 0, 0, 0, 0, 0.3, 0.4, 0.4, 0.4, 0.8, 0.8, 0.8}
	const steepest = 9 // the 0.4 -> 0.8 jump, which max-jump would choose

	got := changepoint(ys)
	if got == steepest {
		t.Fatalf("got %d, the steepest rise; want the foot of the climb", got)
	}
	if got < 5 || got > 6 {
		t.Errorf("got %d, want 5 or 6 (the floor exit)", got)
	}
}

func TestChangepointOnCompressedProbabilities(t *testing.T) {
	// Real curves never reach 0 or 1. The estimator must not care.
	ys := []float64{0.10, 0.12, 0.10, 0.11, 0.62, 0.58, 0.65, 0.61}
	if got := changepoint(ys); got != 4 {
		t.Errorf("got %d, want 4", got)
	}
}

func TestChangepointHandlesDegenerateInput(t *testing.T) {
	if got := changepoint(nil); got != 0 {
		t.Errorf("nil: got %d, want 0", got)
	}
	if got := changepoint([]float64{0.5}); got != 0 {
		t.Errorf("single: got %d, want 0", got)
	}
	// A flat curve has no edge; whatever index comes back, the caller is told
	// the step does not rise.
	flat := []float64{0.3, 0.3, 0.3, 0.3}
	if stepRises(flat, changepoint(flat)) {
		t.Error("a flat curve must not report a rising step")
	}
}

func TestStepRises(t *testing.T) {
	up := []float64{0, 0, 0.9, 0.9}
	if !stepRises(up, 2) {
		t.Error("expected a rising step")
	}
	down := []float64{0.9, 0.9, 0, 0}
	if stepRises(down, 2) {
		t.Error("a falling step must not count as rising")
	}
	if stepRises(up, 0) || stepRises(up, len(up)) {
		t.Error("out-of-range indices must report no rise")
	}
}

func TestAverage(t *testing.T) {
	got := average([][]float64{
		{0.0, 0.6, 0.9},
		{0.2, 0.4, 0.9},
		{0.1, 0.5, 0.9},
	})
	want := []float64{0.1, 0.5, 0.9}
	for i := range want {
		if got[i] < want[i]-1e-9 || got[i] > want[i]+1e-9 {
			t.Errorf("index %d: got %v, want %v", i, got[i], want[i])
		}
	}
	if average(nil) != nil {
		t.Error("nil runs should give nil")
	}
}

// The measured SI2IggZ3Fac end-edge curve, rounded to two places. The true
// boundary is at index 9. Real narration afterward never settles near 1.0 --
// it stays a noisy 0.46-0.85 for the next 150+ seconds, some of it itself
// sounding claim-heavy ("will map 17 billion stars...").
var si2iggz3facEndCurve = []float64{
	0.14, 0.04, 0.05, 0.04, 0.04, 0.05, 0.15, 0.10, 0.39, // still the ad
	0.85, 0.76, 0.55, 0.47, 0.64, 0.64, 0.59, 0.46, 0.49, // content resumes at index 9
	0.50, 0.46, 0.51, 0.52, 0.49, 0.54, 0.65, 0.65, 0.72, 0.71,
}

func TestChangepointIsDraggedEarlyByANoisyTail(t *testing.T) {
	// Documents the bug this curve exposed: fit across the whole tail, the
	// noisy plateau of real content gives the global SSE split almost as much
	// reason to land at index 8 (still the ad's "thanks to..." line) as at the
	// true edge, index 9. This is not the estimator working as intended --
	// it's the failure EndFitWindow exists to prevent. See detect.go.
	if got := changepoint(si2iggz3facEndCurve); got != 8 {
		t.Errorf("got %d, want 8 (the bug: one sentence early). If this now "+
			"passes with a different value, the tail may no longer reproduce "+
			"the failure -- check whether the fix still needs regression "+
			"coverage another way before deleting this test.", got)
	}
}

func TestChangepointOnTheSameTailCappedFindsTheTrueEdge(t *testing.T) {
	// The fix: same curve, only the front considered. Mirrors what bound
	// does when Options.EndFitWindow caps the fit but still returns (and still
	// asks about) the full curve.
	capped := si2iggz3facEndCurve[:10]
	if got := changepoint(capped); got != 9 {
		t.Errorf("got %d, want 9 (the true edge)", got)
	}
}

// The measured d3Gjq-BffuI end-edge curve. The true boundary is at index 10.
// The ad's actual CTA line ("Links in the description.", index 9) still
// scores low, 0.29, but it is the least-low point in a run of otherwise
// near-zero scores, so a fit that cannot see index 10 has nothing resembling
// a real step to find and settles on that noise instead.
var d3gjqBffuiEndCurve = []float64{
	0.12, 0.08, 0.08, 0.07, 0.05, 0.05, 0.09, 0.09, 0.16, 0.29, // still the ad
	0.88, 0.77, 0.67, 0.56, 0.43, 0.60, 0.38, 0.64, 0.61, 0.64, // content resumes at index 10
	0.71, 0.64, 0.63, 0.59, 0.60, 0.54, 0.48, 0.37, 0.42, 0.55, 0.59, 0.69,
}

func TestChangepointExcludesTheTrueEdgeWhenTheWindowIsOneTooSmall(t *testing.T) {
	// The mirror bug to TestChangepointIsDraggedEarlyByANoisyTail: here the
	// window isn't too wide, it's one candidate too narrow. A cap of 10 caps
	// the fit to indices 0-9, one short of the true edge at index 10, so the
	// fit -- correctly, given what it can see -- finds the least-bad split in
	// nine near-zero answers instead.
	capped := d3gjqBffuiEndCurve[:10]
	if got := changepoint(capped); got != 9 {
		t.Errorf("got %d, want 9 (the bug: the true edge at index 10 isn't in "+
			"the window at all). If this now passes with a different value, the "+
			"tail may no longer reproduce the failure -- check whether the fix "+
			"still needs regression coverage another way before deleting this test.", got)
	}
}

func TestChangepointOnTheSameTailWithOneMoreCandidateFindsTheTrueEdge(t *testing.T) {
	// The fix: EndFitWindow raised from 10 to 11. One more candidate is all
	// this curve needed, and sweeping it against every other cached video's
	// end edge changed only this curve's outcome.
	capped := d3gjqBffuiEndCurve[:11]
	if got := changepoint(capped); got != 10 {
		t.Errorf("got %d, want 10 (the true edge)", got)
	}
}

// bridgeClusters: a read the scan stage broke into three fragments (two
// dips -- a music sting, a quieter aside -- each dropped one window below
// AnchorThreshold) must fuse back into one cluster with the full text,
// however many pieces it arrived in.
func TestBridgeClustersFusesAChainOfFragments(t *testing.T) {
	raw := []cluster{
		{rule: "sponsor", start: 60, end: 90, prob: 0.92, text: "frag1"},
		{rule: "sponsor", start: 120, end: 150, prob: 0.97, text: "frag2"},
		{rule: "sponsor", start: 180, end: 210, prob: 0.95, text: "frag3"},
	}
	got := bridgeClusters(raw, 40) // > the 30s gap between fragments
	if len(got) != 1 {
		t.Fatalf("got %d clusters, want 1: three fragments of one ad must fuse", len(got))
	}
	if got[0].start != 60 || got[0].end != 210 {
		t.Errorf("bridged span: got %v-%v, want 60-210", got[0].start, got[0].end)
	}
	if got[0].text != "frag1 frag2 frag3" {
		t.Errorf("bridged text must concatenate every fragment -- bound() quotes this as "+
			"\"the advertisement\", so a middle fragment must not be judged against an "+
			"incomplete slice of it: got %q", got[0].text)
	}
	if got[0].prob != 0.97 {
		t.Errorf("bridged prob should be the strongest fragment's: got %v, want 0.97", got[0].prob)
	}
}

func TestBridgeClustersLeavesDifferentRulesSeparate(t *testing.T) {
	raw := []cluster{
		{rule: "sponsor", start: 0, end: 30},
		{rule: "interaction", start: 35, end: 60}, // close in time, but not the same thing
	}
	if got := bridgeClusters(raw, 90); len(got) != 2 {
		t.Errorf("got %d clusters, want 2: different rules must never fuse just because they're close", len(got))
	}
}

func TestBridgeClustersRespectsTheGapCeiling(t *testing.T) {
	raw := []cluster{
		{rule: "sponsor", start: 0, end: 30},
		{rule: "sponsor", start: 500, end: 530}, // a genuinely separate, later read
	}
	if got := bridgeClusters(raw, 90); len(got) != 2 {
		t.Errorf("got %d clusters, want 2: a gap this large is a different ad, not a split one", len(got))
	}
}

func TestBridgeClustersHandlesDegenerateInput(t *testing.T) {
	if got := bridgeClusters(nil, 90); len(got) != 0 {
		t.Errorf("nil: got %d clusters, want 0", len(got))
	}
}

func TestChangepointIsLinearOnLongCurves(t *testing.T) {
	ys := make([]float64, 2000)
	for i := range ys {
		if i >= 1200 {
			ys[i] = 0.8
		}
	}
	if got := changepoint(ys); got != 1200 {
		t.Errorf("got %d, want 1200", got)
	}
}

// fitEnd, on measured closing curves. Replayed across all 19 labeled curves in
// data/end-curves.jsonl by fit/replay_end_edge.py; these pin the three it
// changed and two it must leave alone.

// qgLaCZyKv_8 (doorbell): a sponsor skit runs past the 11-candidate window with
// the model correctly scoring it low; the return is index 15 (0.74).
var doorbellEndCurve = []float64{
	0.07, 0.08, 0.29, 0.26, 0.28, 0.35, 0.32, 0.20, 0.18, 0.14, 0.14,
	0.21, 0.19, 0.25, 0.15, 0.74, 0.62, 0.68,
}

func TestFitEndWidensPastASkitToTheRealReturn(t *testing.T) {
	// The old fit took index 2 on a 0.16 step and kept the whole skit.
	got := fitEnd(doorbellEndCurve, 11, 0.3, 0.3)
	if got.idx != 15 || got.step < 0.3 {
		t.Errorf("got idx %d step %.2f (%s), want 15 with a real step", got.idx, got.step, got.reason)
	}
}

func TestFitEndLetsAReadThatEndsTheVideoRunOut(t *testing.T) {
	// I07RBedXRYA: the Incogni read runs to the last caption. The old fit cut at
	// index 2 on a 0.01 step and kept "use the code SPACETIME".
	got := fitEnd([]float64{0.06, 0.05, 0.06, 0.07}, 11, 0.3, 0.3)
	if got.idx != 4 {
		t.Errorf("got idx %d (%s), want 4: the program never returns", got.idx, got.reason)
	}
}

func TestFitEndKeepsContentWhenTheCurveIsAmbiguous(t *testing.T) {
	// YSwRqNCeP9k: three candidates, all middling, content really resumes at
	// index 0. The old fit took index 2 on a 0.11 step and cut 7s of physics.
	got := fitEnd([]float64{0.51, 0.46, 0.60}, 11, 0.3, 0.3)
	if got.idx != 0 {
		t.Errorf("got idx %d (%s), want 0", got.idx, got.reason)
	}
}

func TestFitEndLeavesClearStepsAlone(t *testing.T) {
	for name, tc := range map[string]struct {
		curve []float64
		want  int
	}{
		"SI2IggZ3Fac": {si2iggz3facEndCurve, 9},
		"d3Gjq-BffuI": {d3gjqBffuiEndCurve, 10},
	} {
		if got := fitEnd(tc.curve, 11, 0.3, 0.3); got.idx != tc.want || got.reason != "step" {
			t.Errorf("%s: got idx %d (%s), want %d from the unwidened window", name, got.idx, got.reason, tc.want)
		}
	}
}

func TestFitStartKeepsTheAnchorWhenEveryCandidateIsAlreadyTheAdvertisement(t *testing.T) {
	// The Vergecast 2SyX3sudRrY opens on a Sonos read at 0:00, so no candidate
	// precedes it. The segue scored ~0.7, then the product pitch ~0.97; taking
	// that rise as the edge cut the read in half.
	curve := []float64{0.659, 0.728, 0.745, 0.662, 0.669, 0.975, 0.976, 0.927}
	if idx, _ := fitStart(curve); idx != 0 {
		t.Fatalf("edge at %d, want 0 (the advertisement had begun by the first candidate)", idx)
	}
}

func TestFitStartFindsASegueRisingFromContent(t *testing.T) {
	curve := []float64{0.02, 0.05, 0.03, 0.40, 0.62, 0.95, 0.97}
	if idx, step := fitStart(curve); idx != 4 || step <= 0 {
		t.Fatalf("edge at %d (step %.2f), want 4", idx, step)
	}
}

// A curve that neither steps nor stays low decides nothing. fitEnd says so,
// and bound keeps the anchor's end rather than the first candidate, which sits
// a scan window inside the anchor.
func TestFitEndFlagsAnAmbiguousCurve(t *testing.T) {
	e := fitEnd([]float64{0.4, 0.5, 0.45, 0.5, 0.42}, 11, 0.3, 0.3)
	if !e.ambiguous {
		t.Errorf("got %+v, want an ambiguous edge", e)
	}
	if e := fitEnd([]float64{0.05, 0.05, 0.9, 0.9}, 11, 0.3, 0.3); e.ambiguous || e.idx != 2 {
		t.Errorf("clear step: got %+v, want idx 2 and not ambiguous", e)
	}
}
