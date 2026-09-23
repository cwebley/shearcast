package detect

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
