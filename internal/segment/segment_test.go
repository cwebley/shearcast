package segment

import (
	"math"
	"testing"
)

func seg(start, end float64, rule string) Segment {
	return Segment{Start: start, End: end, Rule: rule, Source: "jev", Confidence: 0.9}
}

func eq(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func assertRanges(t *testing.T, got []Range, want []Range) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d ranges %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range got {
		if !eq(got[i].Start, want[i].Start) || !eq(got[i].End, want[i].End) {
			t.Fatalf("range %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestClamp(t *testing.T) {
	in := []Segment{
		seg(-10, 5, "sponsor"),   // starts before zero
		seg(590, 700, "sponsor"), // runs past the end
		seg(100, 100.01, "x"),    // too short to matter
		seg(800, 900, "x"),       // entirely past the end
	}
	got := Clamp(in, 600)
	if len(got) != 2 {
		t.Fatalf("got %d segments %v, want 2", len(got), got)
	}
	if !eq(got[0].Start, 0) || !eq(got[0].End, 5) {
		t.Errorf("first: got %v, want {0 5}", got[0])
	}
	if !eq(got[1].Start, 590) || !eq(got[1].End, 600) {
		t.Errorf("second: got %v, want {590 600}", got[1])
	}
}

func TestPadStopsAtBounds(t *testing.T) {
	got := Pad([]Segment{seg(1, 10, "sponsor")}, 5, 5, 12)
	if len(got) != 1 {
		t.Fatalf("got %d segments, want 1", len(got))
	}
	if !eq(got[0].Start, 0) || !eq(got[0].End, 12) {
		t.Errorf("got %v, want {0 12}", got[0])
	}
}

func TestMerge(t *testing.T) {
	tests := []struct {
		name string
		in   []Segment
		gap  float64
		want []Range
	}{
		{
			name: "disjoint segments stay separate",
			in:   []Segment{seg(0, 10, "a"), seg(100, 110, "a")},
			gap:  2,
			want: []Range{{0, 10}, {100, 110}},
		},
		{
			name: "overlapping segments fuse",
			in:   []Segment{seg(0, 10, "a"), seg(5, 20, "a")},
			gap:  0,
			want: []Range{{0, 20}},
		},
		{
			name: "island smaller than gap is bridged",
			in:   []Segment{seg(0, 10, "a"), seg(11, 20, "a")},
			gap:  2,
			want: []Range{{0, 20}},
		},
		{
			name: "island larger than gap survives",
			in:   []Segment{seg(0, 10, "a"), seg(15, 20, "a")},
			gap:  2,
			want: []Range{{0, 10}, {15, 20}},
		},
		{
			name: "unsorted input is handled",
			in:   []Segment{seg(100, 110, "a"), seg(0, 10, "a")},
			gap:  0,
			want: []Range{{0, 10}, {100, 110}},
		},
		{
			// An ad split into three scan fragments by two separate mid-read
			// dips, each island smaller than gap. Must chain into one span,
			// not just fuse the first pair and stop.
			name: "a chain of three islands all bridge into one",
			in:   []Segment{seg(0, 10, "a"), seg(11, 20, "a"), seg(21, 30, "a")},
			gap:  2,
			want: []Range{{0, 30}},
		},
		{
			// Same chain, but the third island sits too far out. The first
			// two must still bridge -- one distant gap must not veto the
			// merges that are within tolerance.
			name: "a chain still merges the islands within tolerance when one is out of range",
			in:   []Segment{seg(0, 10, "a"), seg(11, 20, "a"), seg(40, 50, "a")},
			gap:  2,
			want: []Range{{0, 20}, {40, 50}},
		},
		{
			name: "fully contained segment does not shrink the parent",
			in:   []Segment{seg(0, 100, "a"), seg(10, 20, "a")},
			gap:  0,
			want: []Range{{0, 100}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			merged := Merge(tc.in, tc.gap)
			got := make([]Range, len(merged))
			for i, s := range merged {
				got[i] = Range{s.Start, s.End}
			}
			assertRanges(t, got, tc.want)
		})
	}
}

func TestMergeMarksMixedProvenance(t *testing.T) {
	a := Segment{Start: 0, End: 10, Rule: "sponsor", Source: "sponsorblock", Confidence: 1.0}
	b := Segment{Start: 9, End: 20, Rule: "patreon", Source: "jev", Confidence: 0.82}
	got := Merge([]Segment{a, b}, 0)
	if len(got) != 1 {
		t.Fatalf("got %d segments, want 1", len(got))
	}
	if got[0].Rule != "mixed" || got[0].Source != "mixed" {
		t.Errorf("got rule=%q source=%q, want both mixed", got[0].Rule, got[0].Source)
	}
	// Confidence of a fused cut is the weakest evidence in it.
	if !eq(got[0].Confidence, 0.82) {
		t.Errorf("got confidence %v, want 0.82", got[0].Confidence)
	}
}

func TestKeepRanges(t *testing.T) {
	tests := []struct {
		name     string
		cuts     []Segment
		duration float64
		minKeep  float64
		want     []Range
	}{
		{
			name:     "no cuts keeps everything",
			cuts:     nil,
			duration: 600,
			minKeep:  1,
			want:     []Range{{0, 600}},
		},
		{
			name:     "cut in the middle splits the audio",
			cuts:     []Segment{seg(100, 200, "sponsor")},
			duration: 600,
			minKeep:  1,
			want:     []Range{{0, 100}, {200, 600}},
		},
		{
			name:     "cut at the very start",
			cuts:     []Segment{seg(0, 30, "intro")},
			duration: 600,
			minKeep:  1,
			want:     []Range{{30, 600}},
		},
		{
			name:     "cut running to the end",
			cuts:     []Segment{seg(500, 600, "outro")},
			duration: 600,
			minKeep:  1,
			want:     []Range{{0, 500}},
		},
		{
			name:     "sliver between two cuts is dropped",
			cuts:     []Segment{seg(100, 200, "a"), seg(200.5, 300, "a")},
			duration: 600,
			minKeep:  1,
			want:     []Range{{0, 100}, {300, 600}},
		},
		{
			name:     "whole video cut leaves nothing",
			cuts:     []Segment{seg(0, 600, "a")},
			duration: 600,
			minKeep:  1,
			want:     nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertRanges(t, KeepRanges(tc.cuts, tc.duration, tc.minKeep), tc.want)
		})
	}
}

func TestPrepareRoundTrip(t *testing.T) {
	// Two ad reads in a ten minute video, with a short island between them that
	// the merge gap should swallow.
	raw := []Segment{seg(120, 180, "sponsor"), seg(181, 210, "patreon")}
	cuts, keeps := Prepare(raw, 600, 0.25, 0.25, 2, 1)

	if len(cuts) != 1 {
		t.Fatalf("got %d cuts %v, want 1 fused cut", len(cuts), cuts)
	}
	if !eq(cuts[0].Start, 119.75) || !eq(cuts[0].End, 210.25) {
		t.Errorf("fused cut: got %v, want {119.75 210.25}", cuts[0])
	}
	assertRanges(t, keeps, []Range{{0, 119.75}, {210.25, 600}})

	// Nothing should go missing: kept plus cut must equal the original duration.
	if total := TotalRanges(keeps) + Covered(cuts); !eq(total, 600) {
		t.Errorf("kept + cut = %v, want 600", total)
	}
}

func TestCoveredCountsOverlapOnce(t *testing.T) {
	segs := []Segment{seg(0, 100, "a"), seg(50, 150, "a")}
	if got := Total(segs); !eq(got, 200) {
		t.Errorf("Total: got %v, want 200", got)
	}
	if got := Covered(segs); !eq(got, 150) {
		t.Errorf("Covered: got %v, want 150", got)
	}
}
