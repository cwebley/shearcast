// Package segment holds the cut arithmetic: intervals in, intervals out.
//
// Every detection source funnels through these functions, so SponsorBlock and
// Jev produce identical cuts given identical ranges. Keeping it pure also means
// the tuning questions (how much to pad, when to bridge a gap) are answerable
// by a unit test rather than by re-downloading a video.
package segment

import "sort"

// minSegment discards intervals too short to be worth an ffmpeg splice.
const minSegment = 0.05

// Segment is a span of the source audio that some rule says to remove.
type Segment struct {
	Start      float64
	End        float64
	Rule       string  // which cut rule matched: "sponsor", "subscribe_plug", ...
	Source     string  // "sponsorblock" or "jev"
	Confidence float64 // 1.0 for a human-marked crowd segment
}

func (s Segment) Duration() float64 {
	if s.End <= s.Start {
		return 0
	}
	return s.End - s.Start
}

// Range is a bare interval, used for the kept audio handed to ffmpeg.
type Range struct{ Start, End float64 }

func (r Range) Duration() float64 {
	if r.End <= r.Start {
		return 0
	}
	return r.End - r.Start
}

// Clamp trims segments to the media's real bounds and drops degenerate ones.
func Clamp(segs []Segment, duration float64) []Segment {
	out := make([]Segment, 0, len(segs))
	for _, s := range segs {
		s.Start = clampF(s.Start, 0, duration)
		s.End = clampF(s.End, 0, duration)
		if s.Duration() > minSegment {
			out = append(out, s)
		}
	}
	return out
}

// Pad widens each cut. Crowd timestamps and model boundaries both tend to clip
// the first syllable, and a little slop outward is cheaper than an audible tail.
func Pad(segs []Segment, pre, post, duration float64) []Segment {
	out := make([]Segment, 0, len(segs))
	for _, s := range segs {
		s.Start -= pre
		s.End += post
		out = append(out, s)
	}
	return Clamp(out, duration)
}

// Merge fuses overlapping cuts, and cuts separated by less than gap seconds.
// A two second island of narration between two ad reads is more jarring kept
// than cut, so it goes.
func Merge(segs []Segment, gap float64) []Segment {
	if len(segs) == 0 {
		return nil
	}
	ordered := make([]Segment, len(segs))
	copy(ordered, segs)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Start != ordered[j].Start {
			return ordered[i].Start < ordered[j].Start
		}
		return ordered[i].End < ordered[j].End
	})

	merged := []Segment{ordered[0]}
	for _, s := range ordered[1:] {
		last := &merged[len(merged)-1]
		if s.Start <= last.End+gap {
			if s.End > last.End {
				last.End = s.End
			}
			if last.Rule != s.Rule {
				last.Rule = "mixed"
			}
			if last.Source != s.Source {
				last.Source = "mixed"
			}
			if s.Confidence < last.Confidence {
				last.Confidence = s.Confidence
			}
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

// KeepRanges inverts cuts into the audio worth keeping. Slivers shorter than
// minKeep are dropped rather than spliced in; a half second of stranded audio
// between two cuts just sounds like a glitch.
func KeepRanges(cuts []Segment, duration, minKeep float64) []Range {
	if duration <= 0 {
		return nil
	}
	ordered := Merge(Clamp(cuts, duration), 0)
	var out []Range
	cursor := 0.0
	for _, s := range ordered {
		if s.Start-cursor >= minKeep {
			out = append(out, Range{cursor, s.Start})
		}
		if s.End > cursor {
			cursor = s.End
		}
	}
	if duration-cursor >= minKeep {
		out = append(out, Range{cursor, duration})
	}
	return out
}

// Prepare runs the whole pipeline: clamp, pad, merge, invert.
func Prepare(raw []Segment, duration, pre, post, gap, minKeep float64) ([]Segment, []Range) {
	cuts := Merge(Pad(Clamp(raw, duration), pre, post, duration), gap)
	return cuts, KeepRanges(cuts, duration, minKeep)
}

// Total sums segment durations, ignoring overlap.
func Total(segs []Segment) float64 {
	var t float64
	for _, s := range segs {
		t += s.Duration()
	}
	return t
}

// Covered sums segment durations after merging, so overlap counts once.
func Covered(segs []Segment) float64 {
	var t float64
	for _, s := range Merge(segs, 0) {
		t += s.Duration()
	}
	return t
}

// TotalRanges sums kept ranges.
func TotalRanges(rs []Range) float64 {
	var t float64
	for _, r := range rs {
		t += r.Duration()
	}
	return t
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
