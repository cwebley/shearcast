// Package experiment stores detection-only runs and their replay provenance.
package experiment

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"time"

	"github.com/cwebley/shearcast/internal/detect"
	"github.com/cwebley/shearcast/internal/fileutil"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/segment"
)

// Record deliberately has no rendered keep ranges. Replay only produces
// detection decisions; audio snapping and rendering need their own inputs.
type Record struct {
	VideoID               string            `json:"video_id"`
	Channel               string            `json:"channel"`
	CreatedAt             time.Time         `json:"created_at"`
	Model                 string            `json:"model"`
	Code                  Code              `json:"code"`
	LabelSHA256           string            `json:"label_sha256"`
	SourceDurationSeconds float64           `json:"source_duration_seconds"`
	DetectionOptions      detect.Options    `json:"detection_options"`
	Detection             *detect.Result    `json:"detection"`
	Recording             *detect.Recording `json:"recording"`
	CollectedUsage        jev.Stats         `json:"collected_usage"`
	Replay                *ReplayInfo       `json:"replay,omitempty"`
}

type ReplayInfo struct {
	SourceSHA256 string `json:"source_sha256"`
	Verified     bool   `json:"baseline_verified"`
}

type Code struct {
	Revision         string `json:"revision"`
	Modified         bool   `json:"modified"`
	ExecutableSHA256 string `json:"executable_sha256"`
}

// CurrentCode identifies even go-run builds or dirty worktrees for which a
// commit alone would not identify the code. No repository or git binary is needed.
func CurrentCode() (Code, error) {
	c := Code{Revision: "unknown"}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				c.Revision = setting.Value
			case "vcs.modified":
				c.Modified = setting.Value == "true"
			}
		}
	}
	path, err := os.Executable()
	if err != nil {
		return c, err
	}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return c, err
	}
	c.ExecutableSHA256 = fmt.Sprintf("%x", h.Sum(nil))
	return c, nil
}

func SHA256(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

// CreateOutput requires a fresh directory so failed or partial experiments
// cannot leave old records masquerading as successful results from this run.
func CreateOutput(path string) error {
	if err := os.MkdirAll(filepath.Dir(filepath.Clean(path)), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		return fmt.Errorf("experiment output must be a new directory: %w", err)
	}
	return nil
}

func Write(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteAtomic(path, append(data, '\n'), 0o600)
}

// SameDecisions compares kept and dropped regions and the final detector cuts.
// Curves and timing/cost statistics are evidence, not decisions. Branch choices
// are compared as well as timestamps so a changed fallback is visible.
func SameDecisions(a, b *detect.Result) bool {
	if a == nil || b == nil {
		return false
	}
	type decision struct {
		Rule, StartPick, EndPick, EndReason string
		AnchorStart, AnchorEnd, Start, End  float64
		Localized                           bool
	}
	decisions := func(regions []detect.Region) []decision {
		out := make([]decision, len(regions))
		for i, r := range regions {
			out[i] = decision{r.Rule, r.StartPick, r.EndPick, r.EndReason, r.AnchorStart, r.AnchorEnd, r.Start, r.End, r.Localized}
		}
		return out
	}
	// Normalize nil and empty slices after JSON round trips.
	segments := func(ss []segment.Segment) []segment.Segment { return append([]segment.Segment{}, ss...) }
	return reflect.DeepEqual(decisions(a.Regions), decisions(b.Regions)) &&
		reflect.DeepEqual(decisions(a.Dropped), decisions(b.Dropped)) &&
		reflect.DeepEqual(segments(a.Segments), segments(b.Segments))
}
