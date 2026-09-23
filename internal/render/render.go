// Package render turns detected ad regions into a finished cut: snapping
// each approximate boundary to real silence in the actual downloaded audio,
// then removing the regions between snapped boundaries.
//
// Detection's boundaries come from caption timestamps, and the caption track
// only carries a timestamp per cue, not per word: the time assigned to a
// sentence's start is a character-position interpolation across its cue, a
// guess, not a measurement. That guess is usually close enough not to
// matter, but on a channel with fast or uneven cadence it can land up to
// half a second off, which is enough to clip a syllable audibly. This
// package fixes that the direct way: instead of trusting the interpolated
// time, it looks at the real audio right around it and moves the cut to
// wherever it actually is quiet.
package render

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Binary is the ffmpeg executable; override for tests or a pinned install.
var Binary = "ffmpeg"

// ProbeBinary is the ffprobe executable; override for tests or a pinned install.
var ProbeBinary = "ffprobe"

// SnapOptions tunes silence detection.
type SnapOptions struct {
	Window float64 // seconds searched on either side of the candidate boundary
	Noise  float64 // dB threshold below which audio counts as silence (negative)
	MinDur float64 // shortest silence worth snapping to, seconds
}

// DefaultSnapOptions covers the worst caption-interpolation error measured
// against real cached videos (~0.56s) with margin to spare.
//
// MinDur is 0.2 rather than a smaller value because ffmpeg's silencedetect
// will happily report an ordinary breath pause inside one sentence (as short
// as 0.1s) as valid silence. If that pause happens to be the nearest thing to
// the candidate boundary, Snap will land the cut inside a word. 0.2 sits
// comfortably above typical within-sentence gaps while still catching the
// longer pause between sentences.
func DefaultSnapOptions() SnapOptions {
	return SnapOptions{Window: 1.0, Noise: -30, MinDur: 0.2}
}

// Snap searches audioPath for real silence near t and returns the midpoint
// of the closest silent interval, so the cut lands with slack on both sides
// rather than right at the edge of speech. If no silence falls within the
// window, it returns t unchanged: the caller keeps the detected boundary
// rather than snapping to something unrelated.
func Snap(ctx context.Context, audioPath string, t float64, opts SnapOptions) (float64, error) {
	if opts.Window <= 0 {
		opts = DefaultSnapOptions()
	}
	seekFrom := math.Max(0, t-opts.Window)
	span := 2 * opts.Window

	cmd := exec.CommandContext(ctx, Binary, "-nostdin",
		"-ss", fmt.Sprintf("%.3f", seekFrom),
		"-i", audioPath,
		"-t", fmt.Sprintf("%.3f", span),
		"-af", fmt.Sprintf("silencedetect=noise=%gdB:d=%.3f", opts.Noise, opts.MinDur),
		"-f", "null", "-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffmpeg silencedetect: %w: %.500s", err, stderr.String())
	}

	// ffmpeg's -ss, used as an input option, resets reported filter
	// timestamps to be relative to the seek point rather than the original
	// file. The offset has to be added back by hand.
	intervals := parseSilence(stderr.String(), seekFrom, seekFrom+span)
	best, ok := nearest(intervals, t)
	if !ok {
		return t, nil
	}
	return (best.start + best.end) / 2, nil
}

type silenceInterval struct{ start, end float64 }

var (
	silenceStartRe = regexp.MustCompile(`silence_start:\s*(-?[0-9.]+)`)
	silenceEndRe   = regexp.MustCompile(`silence_end:\s*(-?[0-9.]+)`)
)

// parseSilence reads ffmpeg's silencedetect stderr and returns each silent
// interval in the source file's own timeline, given the seek offset applied
// to produce that stderr. windowEnd closes off a silence that was still
// running when the queried span ended, since silencedetect never prints a
// matching silence_end in that case.
func parseSilence(stderr string, offset, windowEnd float64) []silenceInterval {
	var out []silenceInterval
	var pending *float64
	for _, line := range strings.Split(stderr, "\n") {
		if m := silenceStartRe.FindStringSubmatch(line); m != nil {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				v += offset
				pending = &v
			}
			continue
		}
		if m := silenceEndRe.FindStringSubmatch(line); m != nil {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				v += offset
				if pending != nil {
					out = append(out, silenceInterval{start: *pending, end: v})
					pending = nil
				}
			}
		}
	}
	if pending != nil {
		out = append(out, silenceInterval{start: *pending, end: windowEnd})
	}
	return out
}

// nearest returns the interval whose midpoint is closest to t.
func nearest(intervals []silenceInterval, t float64) (silenceInterval, bool) {
	var best silenceInterval
	bestDist := math.MaxFloat64
	found := false
	for _, iv := range intervals {
		d := math.Abs((iv.start+iv.end)/2 - t)
		if !found || d < bestDist {
			best, bestDist, found = iv, d, true
		}
	}
	return best, found
}

// CutOptions tunes the final splice.
type CutOptions struct {
	// Crossfade is how many seconds each join overlaps and blends, so a cut
	// never lands as an audible click. Zero means a hard concat instead.
	Crossfade float64
}

func DefaultCutOptions() CutOptions {
	return CutOptions{Crossfade: 0.05}
}

// Range is a span of audio to keep, in seconds.
type Range struct{ Start, End float64 }

// Cut keeps only the given ranges of audioPath, in order, joined with a
// crossfade, and writes the result to outPath.
func Cut(ctx context.Context, audioPath string, keep []Range, outPath string, opts CutOptions) error {
	if len(keep) == 0 {
		return fmt.Errorf("render: nothing to keep")
	}
	if opts.Crossfade < 0 {
		opts.Crossfade = 0
	}

	var filter strings.Builder
	for i, r := range keep {
		fmt.Fprintf(&filter, "[0:a]atrim=start=%.3f:end=%.3f,asetpts=PTS-STARTPTS[s%d];", r.Start, r.End, i)
	}

	label := "s0"
	switch {
	case len(keep) > 1 && opts.Crossfade > 0:
		for i := 1; i < len(keep); i++ {
			next := fmt.Sprintf("a%d", i)
			fmt.Fprintf(&filter, "[%s][s%d]acrossfade=d=%.3f[%s];", label, i, opts.Crossfade, next)
			label = next
		}
	case len(keep) > 1:
		var ins strings.Builder
		for i := range keep {
			fmt.Fprintf(&ins, "[s%d]", i)
		}
		fmt.Fprintf(&filter, "%sconcat=n=%d:v=0:a=1[out];", ins.String(), len(keep))
		label = "out"
	}

	graph := strings.TrimSuffix(filter.String(), ";")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, Binary, "-nostdin", "-y",
		"-i", audioPath,
		"-filter_complex", graph,
		"-map", "["+label+"]",
		outPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg cut: %w: %.800s", err, stderr.String())
	}
	return nil
}

// Probe returns the duration of a media file, using ffprobe. Used to report
// the real, post-cut duration of a rendered episode, which is what a podcast
// feed's itunes:duration must reflect -- the original video's duration no
// longer applies once regions have been removed.
func Probe(ctx context.Context, path string) (time.Duration, error) {
	cmd := exec.CommandContext(ctx, ProbeBinary, "-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffprobe: %w: %.500s", err, stderr.String())
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(stdout.String()), 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe: parsing duration %q: %w", stdout.String(), err)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
