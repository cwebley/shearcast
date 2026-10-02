package detect

import (
	"fmt"
	"slices"
)

// Boundary finding by monotone predicate.
//
// Two earlier framings failed. Scoring each chunk for membership ("is this part
// of the ad") produced a smooth ramp with no step at the true edge, so every
// threshold placed on it landed either seconds late or half a minute early.
// Asking the model to select a sentence ("which one starts it") produced a
// confident answer that disagreed with human marks, and asking the mirror
// question got the same answer back, so it was not confusion and no amount of
// extra instruction moved it.
//
// This framing asks a cumulative question instead: for every candidate at once,
// "has the advertisement begun by here?". The true answer is monotone, false
// until the boundary and true after it, so the answers form a step and the
// boundary is its edge. That buys three things. Each question is sharper than a
// selection. All of them ride in one parallel request. And a step fitted across
// thirty answers cannot be moved by one noisy answer, which matters because the
// model's answers are noisiest exactly at the boundary.

// changepoint returns the index where a two-level step best fits ys.
//
// For every possible split it takes the mean either side and scores the squared
// error; the best split is the edge. There is no threshold, so there is nothing
// to tune and nothing to overfit. Reading a fixed cutoff off this curve does
// not work: the model's probabilities are compressed near the boundary, and a
// cutoff that suits one video's ramp misses another's by half a minute.
func changepoint(ys []float64) int {
	if len(ys) < 2 {
		return 0
	}
	// Prefix sums make this linear rather than quadratic, which matters when a
	// long read offers a couple of hundred candidates.
	n := len(ys)
	sum := make([]float64, n+1)
	sumSq := make([]float64, n+1)
	for i, y := range ys {
		sum[i+1] = sum[i] + y
		sumSq[i+1] = sumSq[i] + y*y
	}
	sse := func(lo, hi int) float64 {
		count := float64(hi - lo)
		if count <= 0 {
			return 0
		}
		s := sum[hi] - sum[lo]
		return (sumSq[hi] - sumSq[lo]) - s*s/count
	}

	best, bestSSE := 1, sse(0, 1)+sse(1, n)
	for i := 2; i < n; i++ {
		if total := sse(0, i) + sse(i, n); total < bestSSE {
			bestSSE, best = total, i
		}
	}
	return best
}

// fitStart finds the opening edge on a start curve: the index of the first
// candidate inside the advertisement, and the step's height. An index of zero
// means keep the anchor's own start.
//
// Both start curves answer "has the advertisement begun by here?", so the
// candidates before a real edge must read as not yet begun. When they already
// average over one half, the read began at or before the first candidate and
// the rise is a step inside it: a segue scoring ~0.7 then the pitch ~0.97.
// That happens when an episode opens on an advertisement, leaving no content
// to offer before it; taking the rise cut such a read in half. Replayed over
// 64 recorded start curves (fit/replay_start_edge.py, 2026-09-27), this
// changed that one decision and no other.
func fitStart(curve []float64) (int, float64) {
	idx := changepoint(curve)
	if !stepRises(curve, idx) {
		return 0, 0
	}
	var before float64
	for _, y := range curve[:idx] {
		before += y
	}
	if before/float64(idx) >= 0.5 {
		return 0, 0
	}
	return idx, stepHeight(curve, idx)
}

// stepRises reports whether the fitted step goes up at idx. A predicate that
// never rises means the boundary lies outside the candidates we offered, and we
// should keep the anchor's own edge rather than invent one.
func stepRises(ys []float64, idx int) bool {
	if idx <= 0 || idx >= len(ys) {
		return false
	}
	var lo, hi float64
	for _, y := range ys[:idx] {
		lo += y
	}
	for _, y := range ys[idx:] {
		hi += y
	}
	return hi/float64(len(ys)-idx) > lo/float64(idx)
}

// average collapses repeated runs of the same predicate into one curve.
// The answers are noisy right at the boundary, which is the only place they
// matter, and extra questions are nearly free.
func average(runs [][]float64) []float64 {
	if len(runs) == 0 {
		return nil
	}
	out := make([]float64, len(runs[0]))
	for _, run := range runs {
		for i := range out {
			if i < len(run) {
				out[i] += run[i]
			}
		}
	}
	for i := range out {
		out[i] /= float64(len(runs))
	}
	return out
}

// stepHeight is the rise of the two-level step split at idx: the mean after it
// minus the mean before it.
func stepHeight(ys []float64, idx int) float64 {
	if idx <= 0 || idx >= len(ys) {
		return 0
	}
	var lo, hi float64
	for _, y := range ys[:idx] {
		lo += y
	}
	for _, y := range ys[idx:] {
		hi += y
	}
	return hi/float64(len(ys)-idx) - lo/float64(idx)
}

// endEdge is where the closing boundary lies on a curve. idx is the candidate
// at which the program resumes; idx == len(curve) means it never does.
type endEdge struct {
	idx    int
	step   float64
	reason string
	// ambiguous means the curve decided nothing; see fitEnd.
	ambiguous bool
}

// fitEnd places the closing edge on an averaged "has the program returned"
// curve.
//
// A step only counts if it is at least minStep tall. The fit starts on the first
// window candidates, because real narration after a read does not settle near
// 1.0 and a long tail out-votes the true edge (see Options.EndFitWindow). When
// that window holds no real step, it widens one candidate at a time: a sponsor
// skit can run past the window with the model correctly scoring it low, and
// the return sits just beyond it.
//
// With no qualifying step anywhere, a curve that stays under neverReturns is
// the model saying the program does not come back, as with a read that ends
// the video, so the read runs past every candidate. Anything else is ambiguous,
// and the caller keeps the anchor's own end (see bound).
func fitEnd(curve []float64, window int, minStep, neverReturns float64) endEdge {
	if len(curve) < 2 {
		return endEdge{idx: 0, reason: "too few candidates"}
	}
	first := len(curve)
	if window > 1 && window < first {
		first = window
	}
	for w := first; w <= len(curve); w++ {
		fit := curve[:w]
		idx := changepoint(fit)
		if h := stepHeight(fit, idx); h >= minStep {
			reason := "step"
			if w > first {
				reason = fmt.Sprintf("step, fit widened to %d candidates", w)
			}
			return endEdge{idx: idx, step: h, reason: reason}
		}
	}
	if slices.Max(curve) < neverReturns {
		return endEdge{idx: len(curve), reason: "never returns"}
	}
	return endEdge{idx: 0, reason: "ambiguous, kept the anchor's end", ambiguous: true}
}
