package render

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSilenceAddsSeekOffset(t *testing.T) {
	stderr := "" +
		"[silencedetect @ 0x0] silence_start: 0.701270\n" +
		"[silencedetect @ 0x0] silence_end: 1.077256 | silence_duration: 0.375986\n"
	got := parseSilence(stderr, 40, 70)
	if len(got) != 1 {
		t.Fatalf("intervals = %v, want 1", got)
	}
	want := silenceInterval{start: 40.70127, end: 41.077256}
	if abs(got[0].start-want.start) > 1e-6 || abs(got[0].end-want.end) > 1e-6 {
		t.Errorf("interval = %+v, want %+v", got[0], want)
	}
}

func TestParseSilenceUnclosedAtWindowEnd(t *testing.T) {
	// A silence that is still running when the queried span ends never gets
	// a matching silence_end line from ffmpeg.
	stderr := "[silencedetect @ 0x0] silence_start: 1.5\n"
	got := parseSilence(stderr, 10, 12)
	if len(got) != 1 {
		t.Fatalf("intervals = %v, want 1", got)
	}
	if got[0].start != 11.5 || got[0].end != 12 {
		t.Errorf("interval = %+v, want {11.5 12}", got[0])
	}
}

func TestNearestPicksClosestMidpoint(t *testing.T) {
	intervals := []silenceInterval{
		{start: 10, end: 10.2}, // mid 10.1
		{start: 14, end: 14.8}, // mid 14.4
	}
	best, ok := nearest(intervals, 14.5)
	if !ok || best != intervals[1] {
		t.Errorf("nearest = %+v, %v, want %+v, true", best, ok, intervals[1])
	}
}

func TestNearestEmpty(t *testing.T) {
	if _, ok := nearest(nil, 5); ok {
		t.Error("nearest on no intervals should report not found")
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// TestSnapAndCutAgainstRealFFmpeg builds a tone-silence-tone fixture with
// ffmpeg's own signal generator (no checked-in audio needed) and drives Snap
// and Cut against it, so the ffmpeg argument shapes are checked against the
// real binary rather than only against parsing logic. Skips if ffmpeg is not
// on PATH.
func TestSnapAndCutAgainstRealFFmpeg(t *testing.T) {
	if _, err := exec.LookPath(Binary); err != nil {
		t.Skipf("%s not on PATH", Binary)
	}
	ctx := context.Background()
	dir := t.TempDir()

	// 0-2s tone, 2-3s silence, 3-5s tone.
	fixture := filepath.Join(dir, "fixture.wav")
	build := exec.CommandContext(ctx, Binary, "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono:d=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-filter_complex", "[0][1][2]concat=n=3:v=0:a=1[out]",
		"-map", "[out]", fixture,
	)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building fixture: %v\n%s", err, out)
	}

	// The candidate boundary is deliberately off by 0.3s from the true
	// silence at 2.0-3.0s, the way a caption-interpolated boundary would be.
	got, err := Snap(ctx, fixture, 2.3, SnapOptions{Window: 1.0, Noise: -30, MinDur: 0.05})
	if err != nil {
		t.Fatalf("Snap: %v", err)
	}
	if got < 2.0 || got > 3.0 {
		t.Errorf("Snap(2.3) = %.3f, want inside the true silence [2.0, 3.0]", got)
	}

	out := filepath.Join(dir, "cut.wav")
	keep := []Range{{Start: 0, End: 2}, {Start: 3, End: 5}}
	if err := Cut(ctx, fixture, keep, out, CutOptions{Crossfade: 0.05}); err != nil {
		t.Fatalf("Cut: %v", err)
	}

	dur, err := Probe(ctx, out)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	// 4s of kept audio minus one 0.05s crossfade overlap.
	if dur < 3850*time.Millisecond || dur > 3950*time.Millisecond {
		t.Errorf("Probe duration = %v, want ~3.9s", dur)
	}
}
