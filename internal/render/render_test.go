package render

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

// Compare decoded audio against the original left-to-right crossfade graph,
// including uneven trees and short sections whose fades overlap.
func TestCrossfadeAudioMatchesSequentialJoins(t *testing.T) {
	if _, err := exec.LookPath(Binary); err != nil {
		t.Skipf("%s not on PATH", Binary)
	}
	ctx := context.Background()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "source.wav")
	build := exec.CommandContext(ctx, Binary, "-v", "error", "-f", "lavfi", "-i",
		"anoisesrc=color=pink:r=48000:d=12:seed=42[l];anoisesrc=color=pink:r=48000:d=12:seed=43[r];[l][r]amerge=inputs=2", fixture)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	aac := filepath.Join(dir, "source.m4a")
	// PNS synthesizes noise using decoder PRNG state, which changes after a
	// seek. Disable it only in this fixture so waveform equality can check
	// sample alignment and the crossfade mix instead of random noise samples.
	encode := exec.CommandContext(ctx, Binary, "-v", "error", "-i", fixture, "-c:a", "aac", "-b:a", "192k", "-aac_pns", "0", aac)
	if out, err := encode.CombinedOutput(); err != nil {
		t.Fatalf("AAC fixture: %v\n%s", err, out)
	}
	aac44100 := filepath.Join(dir, "source-44100.m4a")
	encode = exec.CommandContext(ctx, Binary, "-v", "error", "-i", fixture, "-ar", "44100", "-c:a", "aac", "-b:a", "192k", "-aac_pns", "0", aac44100)
	if out, err := encode.CombinedOutput(); err != nil {
		t.Fatalf("44.1kHz fixture: %v\n%s", err, out)
	}
	for _, fixture := range []string{fixture, aac, aac44100} {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				count  int
				length float64
			}{
				{"single", 1, 0.25}, {"pair", 2, 0.25}, {"odd", 3, 0.25},
				{"uneven", 7, 0.25}, {"even", 8, 0.25}, {"deeper", 17, 0.25},
				{"overlapping fades", 7, 0.075}, {"touching fades", 7, 0.1},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var keep []Range
					var graph strings.Builder
					for i := 0; i < tc.count; i++ {
						start := float64(i) * 0.513
						keep = append(keep, Range{start, start + tc.length})
						fmt.Fprintf(&graph, "[0:a]atrim=start=%.3f:end=%.3f,asetpts=PTS-STARTPTS[s%d];", start, start+tc.length, i)
					}
					label := "s0"
					for i := 1; i < len(keep); i++ {
						next := fmt.Sprintf("a%d", i)
						fmt.Fprintf(&graph, "[%s][s%d]acrossfade=d=0.050[%s];", label, i, next)
						label = next
					}
					wantPath := filepath.Join(t.TempDir(), "reference.m4a")
					cmd := exec.CommandContext(ctx, Binary, "-v", "error", "-i", fixture,
						"-filter_complex", strings.TrimSuffix(graph.String(), ";"), "-map", "["+label+"]",
						"-c:a", "aac", "-b:a", "128k", wantPath)
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("reference: %v\n%s", err, out)
					}
					gotPath := filepath.Join(t.TempDir(), "actual.m4a")
					if err := Cut(ctx, fixture, keep, gotPath, DefaultCutOptions()); err != nil {
						t.Fatal(err)
					}
					decode := func(path string) []byte {
						t.Helper()
						out, err := exec.CommandContext(ctx, Binary, "-v", "error", "-i", path, "-f", "s16le", "-c:a", "pcm_s16le", "-").Output()
						if err != nil {
							t.Fatal(err)
						}
						return out
					}
					want, got := decode(wantPath), decode(gotPath)
					if !bytes.Equal(got, want) {
						t.Fatalf("decoded audio differs from sequential joins: got %d bytes, want %d", len(got), len(want))
					}
					duration, err := Probe(ctx, gotPath)
					if err != nil {
						t.Fatal(err)
					}
					expected := float64(tc.count)*tc.length - float64(tc.count-1)*0.05
					if abs(duration.Seconds()-expected) > 0.025 {
						t.Fatalf("duration %s, want %.3fs", duration, expected)
					}
				})
			}
		})
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

	out := filepath.Join(dir, "cut.m4a")
	keep := []Range{{Start: 0, End: 2}, {Start: 3, End: 5}}
	if err := Cut(ctx, fixture, keep, out, CutOptions{Crossfade: 0.05}); err != nil {
		t.Fatalf("Cut: %v", err)
	}

	dur, err := Probe(ctx, out)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	// 4s of kept audio minus one 0.05s crossfade overlap.
	if abs(dur.Seconds()-3.95) > 0.05 {
		t.Errorf("Probe duration = %v, want ~3.95s", dur)
	}
}

func TestSubMillisecondCrossfadeConcatenates(t *testing.T) {
	if _, err := exec.LookPath(Binary); err != nil {
		t.Skipf("%s not on PATH", Binary)
	}
	ctx := context.Background()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.wav")
	build := exec.CommandContext(ctx, Binary, "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=4", fixture)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building fixture: %v\n%s", err, out)
	}
	// 0.0001s formats as d=0.000, which ffmpeg reads as its one-second default.
	out := filepath.Join(dir, "cut.m4a")
	if err := Cut(ctx, fixture, []Range{{Start: 0, End: 2}, {Start: 2, End: 4}}, out, CutOptions{Crossfade: 0.0001}); err != nil {
		t.Fatal(err)
	}
	dur, err := Probe(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if abs(dur.Seconds()-4) > 0.05 {
		t.Errorf("duration %v, want ~4s", dur)
	}
}

func TestAACBitrateAndCleanEncoding(t *testing.T) {
	if _, err := exec.LookPath(Binary); err != nil {
		t.Skipf("%s not on PATH", Binary)
	}
	ctx := context.Background()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "noise.wav")
	cmd := exec.CommandContext(ctx, Binary, "-v", "error", "-f", "lavfi", "-i",
		"anoisesrc=color=pink:sample_rate=48000:duration=12:seed=42[l];anoisesrc=color=pink:sample_rate=48000:duration=12:seed=43[r];[l][r]amerge=inputs=2", fixture)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	var previousSize int64
	for _, kbps := range []int{32, 64, 96, 128, 320} {
		t.Run(fmt.Sprint(kbps), func(t *testing.T) {
			out := filepath.Join(dir, fmt.Sprintf("%d.m4a", kbps))
			if err := Cut(ctx, fixture, []Range{{0, 12}}, out, CutOptions{BitrateKbps: kbps}); err != nil {
				t.Fatal(err)
			}
			probe := exec.CommandContext(ctx, ProbeBinary, "-v", "error", "-show_entries", "stream=codec_name,bit_rate", "-of", "json", out)
			data, err := probe.Output()
			if err != nil {
				t.Fatal(err)
			}
			var info struct {
				Streams []struct {
					Codec   string `json:"codec_name"`
					Bitrate int    `json:"bit_rate,string"`
				}
			}
			if err := json.Unmarshal(data, &info); err != nil {
				t.Fatal(err)
			}
			if len(info.Streams) != 1 || info.Streams[0].Codec != "aac" {
				t.Fatalf("unexpected streams: %s", data)
			}
			if got := info.Streams[0].Bitrate; abs(float64(got-kbps*1000))/float64(kbps*1000) > 0.15 {
				t.Errorf("bitrate = %d, target = %d kbps", got, kbps)
			}
			duration, err := Probe(ctx, out)
			if err != nil || abs(duration.Seconds()-12) > 0.05 {
				t.Fatalf("duration = %v, %v", duration, err)
			}
			stat, err := os.Stat(out)
			if err != nil {
				t.Fatal(err)
			}
			if stat.Size() <= previousSize {
				t.Errorf("size %d did not increase from %d", stat.Size(), previousSize)
			}
			previousSize = stat.Size()
		})
	}
}
