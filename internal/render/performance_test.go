package render

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This opt-in test has a wall-clock budget, so it is separate from ordinary
// correctness checks that must also run on slow CI machines.
func TestCutManyRanges(t *testing.T) {
	if os.Getenv("SHEARCAST_RENDER_PERF") != "1" {
		t.Skip("set SHEARCAST_RENDER_PERF=1 to run the renderer performance check")
	}
	if _, err := exec.LookPath(Binary); err != nil {
		t.Skipf("%s not on PATH", Binary)
	}
	for _, count := range []int{90, 180} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			dir := t.TempDir()
			fixture := filepath.Join(dir, "source.m4a")
			cmd := exec.Command(Binary, "-v", "error", "-f", "lavfi", "-i",
				fmt.Sprintf("anoisesrc=color=pink:r=48000:d=%d:seed=42[l];anoisesrc=color=pink:r=48000:d=%d:seed=43[r];[l][r]amerge=inputs=2", count/5, count/5),
				"-c:a", "aac", "-b:a", "192k", fixture)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v\n%s", err, out)
			}
			var keep []Range
			for i := 0; i < count; i++ {
				start := float64(i) * 0.2
				keep = append(keep, Range{Start: start, End: start + 0.15})
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			opts := DefaultCutOptions()
			baselineStart := time.Now()
			third := float64(count) / 15
			if err := Cut(ctx, fixture, []Range{{0, third}, {third + 1, 2 * third}, {2*third + 1, 3 * third}}, filepath.Join(dir, "few.m4a"), opts); err != nil {
				t.Fatal(err)
			}
			baseline := time.Since(baselineStart)
			start := time.Now()
			out := filepath.Join(dir, "cut.m4a")
			if err := Cut(ctx, fixture, keep, out, opts); err != nil {
				t.Fatalf("%d-range render after %s: %v", count, time.Since(start), err)
			}
			elapsed := time.Since(start)
			t.Logf("3-range render: %s; %d-range render: %s", baseline, count, elapsed)
			if elapsed > 5*baseline+100*time.Millisecond {
				t.Errorf("%d-range render took %s, more than five times the %s baseline plus 100ms slack", count, elapsed, baseline)
			}
			duration, err := Probe(ctx, out)
			if err != nil {
				t.Fatal(err)
			}
			if abs(duration.Seconds()-(float64(count)*0.15-float64(count-1)*opts.Crossfade)) > 0.05 {
				t.Fatalf("unexpected duration %s", duration)
			}
		})
	}
}
