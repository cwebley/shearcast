package pipeline

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/detect"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/render"
	"github.com/cwebley/shearcast/internal/transcript"
	"github.com/cwebley/shearcast/internal/youtube"
)

// ambiguousClosingModel mirrors the detector's ambiguous-end fixture: a promo
// at 90-150s is followed by closing discussion that the last scan window still
// scores as sponsor, and the return predicate is indecisive throughout.
func ambiguousClosingModel(t *testing.T) *jev.Client {
	t.Helper()
	labeled := regexp.MustCompile(`(?m)^\[(\w+)\] (.*)$`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jev.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		text := map[string]string{}
		var ids []string
		for _, m := range labeled.FindAllStringSubmatch(req.State, -1) {
			text[m[1]] = strings.ToLower(m[2])
			if strings.HasPrefix(m[1], "L") {
				ids = append(ids, m[1])
			}
		}
		sort.Strings(ids)
		segue := len(ids)
		for i, id := range ids {
			if strings.Contains(text[id], "brings us to") {
				segue = i
				break
			}
		}
		answers := map[string]jev.Answer{}
		for id, q := range req.Questions {
			switch {
			case q.Type == jev.TypeNoul && id == "loss":
				answers[id] = jev.Answer{Type: "noul", Noul: 0.1}
			case q.Type == jev.TypeNoul && strings.HasPrefix(id, "L"):
				p := 0.05
				if i := sort.SearchStrings(ids, id); i < len(ids) && ids[i] == id && i >= segue {
					p = 0.9
				}
				answers[id] = jev.Answer{Type: "noul", Noul: p}
			case q.Type == jev.TypeNoul && strings.HasPrefix(id, "R"):
				answers[id] = jev.Answer{Type: "noul", Noul: 0.55}
			case q.Type == jev.TypeNoul:
				answers[id] = jev.Answer{Type: "noul", Noul: 0}
			case q.Type == jev.TypeChoice:
				key := id
				if i := strings.Index(id, "-"); i >= 0 {
					key = id[i+1:]
				}
				if id == "W006" || strings.Contains(text[key], "acme") {
					answers[id] = jev.Answer{Type: "choice", Choice: "sponsor", Confidence: 0.95,
						Probabilities: map[string]float64{"sponsor": 0.95, "content": 0.05}}
				} else {
					answers[id] = jev.Answer{Type: "choice", Choice: detect.KeepRule, Confidence: 0.95,
						Probabilities: map[string]float64{"sponsor": 0.05, "content": 0.95}}
				}
			}
		}
		json.NewEncoder(w).Encode(jev.Response{Answers: answers, Usage: jev.Usage{InputTokens: 100}})
	}))
	t.Cleanup(srv.Close)
	return jev.New(jev.Config{BaseURL: srv.URL, Model: "test"})
}

func TestConservativeAmbiguousEndPreservesClosingContentInRender(t *testing.T) {
	if _, err := exec.LookPath(render.Binary); err != nil {
		t.Skip("ffmpeg not installed")
	}
	cache := youtube.Cache{Dir: t.TempDir()}
	video := &youtube.Video{ID: "closing", Duration: 180, Title: "Test episode"}
	dir := filepath.Join(cache.Dir, video.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Silences near both joins, slightly off the detected times, so the keep
	// ranges show the boundaries were snapped.
	cmd := exec.Command(render.Binary, "-v", "error", "-f", "lavfi", "-i",
		"anoisesrc=color=pink:duration=180:seed=42,volume=enable='between(t,89.7,90.1)+between(t,150.3,150.7)':volume=0",
		"-ac", "2", "-c:a", "aac", "-b:a", "192k", filepath.Join(dir, "audio.m4a"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	weights := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(weights, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	startWeights := write("weights.json", `{"bias":-5,"weights":{"lex_thanks":10}}`)
	endWeights := write("end-weights.json", `{"bias":-5,"weights":{"lex_url":0}}`)

	var cues []transcript.Cue
	add := func(from, to float64, text string) {
		for s := from; s < to; s += 5 {
			cues = append(cues, transcript.Cue{Start: s, End: s + 5, Text: text})
		}
	}
	add(0, 60, "the universe is vast and mostly empty.")
	add(60, 90, "which brings us to the question of how we rest.")
	add(90, 150, "thanks to Acme for supporting this video.")
	add(150, 180, "the universe is vast and mostly empty.")

	near := func(a, b float64) bool { return math.Abs(a-b) < 0.1 }
	promoOnly := func(keep []render.Range) bool {
		return len(keep) == 2 && keep[0].Start == 0 && near(keep[0].End, 89.9) && near(keep[1].Start, 150.5) && near(keep[1].End, 180)
	}
	throughEnd := func(keep []render.Range) bool {
		return len(keep) == 1 && keep[0].Start == 0 && near(keep[0].End, 89.9)
	}
	for _, tc := range []struct {
		name      string
		enabled   bool
		configure func(*config.Config)
		want      func([]render.Range) bool
	}{
		{"default cuts the closing discussion", false, nil, throughEnd},
		{"enabled keeps the closing discussion", true, nil, promoOnly},
		{"enabled without end weights is unchanged", true, func(c *config.Config) { c.Jev.EndWeights = "" }, throughEnd},
		{"enabled below minimum region is unchanged", true, func(c *config.Config) { c.Jev.MinRegion = 61 }, throughEnd},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Jev.WeakThreshold = 1
			cfg.Jev.Weights, cfg.Jev.EndWeights = startWeights, endWeights
			cfg.Jev.ConservativeAmbiguousEnd = tc.enabled
			if tc.configure != nil {
				tc.configure(cfg)
			}
			ch := config.Channel{Slug: "test-channel", Rules: []string{"sponsor"}, BitrateKbps: 64}
			out := filepath.Join(t.TempDir(), "render.m4a")
			path, result, keep, err := RenderEpisode(context.Background(), cfg, ch, cache, ambiguousClosingModel(t), video, cues, RenderOptions{OutPath: out}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Regions) != 1 || result.Regions[0].Start != 90 {
				t.Fatalf("want one region from 90s, got %+v", result.Regions)
			}
			if !tc.want(keep) {
				t.Fatalf("unexpected keep ranges %+v (region %v-%v, %s)", keep, result.Regions[0].Start, result.Regions[0].End, result.Regions[0].EndReason)
			}
			data, err := os.ReadFile(path + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var record RenderRecord
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			if record.DetectionOptions.ConservativeAmbiguousEnd != tc.enabled {
				t.Fatalf("record options ConservativeAmbiguousEnd = %v, want %v", record.DetectionOptions.ConservativeAmbiguousEnd, tc.enabled)
			}
			var kept float64
			for _, r := range record.Keep {
				kept += r.End - r.Start
			}
			if math.Abs(record.DurationSeconds-kept) > 0.5 {
				t.Fatalf("rendered %.2fs, keep ranges total %.2fs", record.DurationSeconds, kept)
			}
		})
	}
}
