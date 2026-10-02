package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/detect"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/render"
	"github.com/cwebley/shearcast/internal/transcript"
	"github.com/cwebley/shearcast/internal/youtube"
)

func TestCleanEpisodeEncodesAndRecordsSettings(t *testing.T) {
	if _, err := exec.LookPath(render.Binary); err != nil {
		t.Skip("ffmpeg not installed")
	}
	cache := youtube.Cache{Dir: t.TempDir()}
	video := &youtube.Video{ID: "test", Duration: 12, Title: "Test episode"}
	dir := filepath.Join(cache.Dir, video.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(render.Binary, "-v", "error", "-f", "lavfi", "-i",
		"anoisesrc=color=pink:duration=12:seed=42", "-ac", "2", "-c:a", "aac", "-b:a", "192k", filepath.Join(dir, "audio.m4a"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jev.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		answers := map[string]jev.Answer{}
		for id := range req.Questions {
			answers[id] = jev.Answer{Type: "choice", Choice: detect.KeepRule, Probabilities: map[string]float64{"content": 1, "sponsor": 0, "selfpromo": 0, "credits": 0}}
		}
		json.NewEncoder(w).Encode(jev.Response{Answers: answers, Usage: jev.Usage{InputTokens: 100}})
	}))
	defer srv.Close()
	client := jev.New(jev.Config{BaseURL: srv.URL, Model: "test"})
	cfg := config.Default()
	cfg.Jev.MaxCandidates = 500
	// KeepTail: a clean episode keeps everything, including audio after its
	// only caption. TestUncaptionedTailIsTrimmed covers the trim.
	ch := config.Channel{Slug: "test-channel", NoWeights: true, BitrateKbps: 64, KeepTail: true}
	cues := []transcript.Cue{{Start: 0, End: 12, Text: "This is an episode about stars and how they form."}}
	path, result, keep, err := RenderEpisode(context.Background(), cfg, ch, cache, client, video, cues, RenderOptions{SnapWindow: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Segments) != 0 || len(keep) != 1 || keep[0].Start != 0 || keep[0].End != 12 {
		t.Fatalf("unexpected clean result: %+v, %+v", result, keep)
	}
	data, err := os.ReadFile(path + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var record RenderRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Cut.BitrateKbps != 64 || record.Codec != "aac" || record.Channel != ch.Slug || record.DurationSeconds < 11.95 || record.DurationSeconds > 12.05 || record.Detection.Stats.InputTokens != 100 {
		t.Fatalf("unexpected record: %+v", record)
	}
	if record.DetectionOptions.MaxCandidates != 254 || record.Snap.Window != render.DefaultSnapOptions().Window {
		t.Fatalf("record did not capture normalized settings: %+v", record)
	}
	input, err := os.Stat(filepath.Join(dir, "audio.m4a"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if output.Size() >= input.Size()/2 {
		t.Fatalf("64 kbps clean output %d bytes, source %d bytes", output.Size(), input.Size())
	}

	t.Run("failed replacement preserves prior audio and record", func(t *testing.T) {
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		oldProbe := render.ProbeBinary
		render.ProbeBinary = filepath.Join(t.TempDir(), "missing-ffprobe")
		t.Cleanup(func() { render.ProbeBinary = oldProbe })
		ch.BitrateKbps = 96
		before := client.Stats()
		if _, _, _, err := RenderEpisode(context.Background(), cfg, ch, cache, client, video, cues, RenderOptions{}, nil); err == nil {
			t.Fatal("expected probe failure")
		}
		if client.Stats().Since(before).Attempts != 0 {
			t.Fatal("probe failure made a model request")
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatalf("previous audio changed: %v", err)
		}
		got, err = os.ReadFile(path + ".json")
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("previous record changed: %v", err)
		}
		staged, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".render-*"))
		if err != nil || len(staged) != 0 {
			t.Fatalf("staged files remain: %v, %v", staged, err)
		}
	})
}

func TestDetectionFailureDoesNotRender(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, `{}`},
		{"missing answers", 200, `{"answers":{}}`},
		{"unknown probability", 200, `{"answers":{"W001":{"type":"choice","choice":"sponsor","probabilities":{"unknown":1}}}}`},
		{"wrong probability keys", 200, `{"answers":{"W001":{"type":"choice","choice":"sponsor","probabilities":{"unknown":1,"content":0,"selfpromo":0}}}}`},
		{"zero probabilities", 200, `{"answers":{"W001":{"type":"choice","choice":"content","probabilities":{"sponsor":0,"content":0,"selfpromo":0}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.audio(t)
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			client := jev.New(jev.Config{BaseURL: srv.URL})
			cache := f.runner.Cache
			cues := []transcript.Cue{{Start: 0, End: 10, Text: "An episode about stars."}}
			path, _, _, err := RenderEpisode(context.Background(), config.Default(), config.Channel{NoWeights: true}, cache, client,
				&f.source.video, cues, RenderOptions{}, nil)
			if err == nil || !strings.Contains(err.Error(), "detection:") || path != "" || calls.Load() == 0 {
				t.Fatalf("path = %q, error = %v", path, err)
			}
			if _, err := os.Stat(cache.SourceAudioPath(lifecycleID)); err != nil {
				t.Fatalf("failed detection lost source audio: %v", err)
			}
			if _, err := os.Stat(filepath.Join(cache.Dir, "renders")); !os.IsNotExist(err) {
				t.Fatalf("failed detection created render output: %v", err)
			}
		})
	}
}

func TestUnusableCaptionsDoNotFetchAudioOrCallModel(t *testing.T) {
	oldDownloader := youtube.Binary
	youtube.Binary = filepath.Join(t.TempDir(), "missing-yt-dlp")
	t.Cleanup(func() { youtube.Binary = oldDownloader })
	for _, cues := range [][]transcript.Cue{nil, {{Start: 0, End: 3, Text: "   "}}} {
		cache := youtube.Cache{Dir: t.TempDir()}
		_, _, _, err := RenderEpisode(context.Background(), config.Default(), config.Channel{NoWeights: true}, cache, nil,
			&youtube.Video{ID: lifecycleID, Duration: 3}, cues, RenderOptions{}, nil)
		if err == nil || !strings.Contains(err.Error(), "no usable caption windows") {
			t.Fatalf("expected caption failure: %v", err)
		}
		entries, err := os.ReadDir(cache.Dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("unusable captions touched audio cache: %v, %v", entries, err)
		}
	}
}

func TestUncaptionedTailIsTrimmed(t *testing.T) {
	if _, err := exec.LookPath(render.Binary); err != nil {
		t.Skip("ffmpeg not installed")
	}
	cache := youtube.Cache{Dir: t.TempDir()}
	video := &youtube.Video{ID: "tail", Duration: 30, Title: "Test episode"}
	dir := filepath.Join(cache.Dir, video.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(render.Binary, "-v", "error", "-f", "lavfi", "-i",
		"anoisesrc=color=pink:duration=30:seed=42", "-ac", "2", "-c:a", "aac", "-b:a", "192k", filepath.Join(dir, "audio.m4a"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jev.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		answers := map[string]jev.Answer{}
		for id := range req.Questions {
			answers[id] = jev.Answer{Type: "choice", Choice: detect.KeepRule, Probabilities: map[string]float64{"content": 1, "sponsor": 0, "selfpromo": 0, "credits": 0}}
		}
		json.NewEncoder(w).Encode(jev.Response{Answers: answers, Usage: jev.Usage{InputTokens: 100}})
	}))
	defer srv.Close()
	client := jev.New(jev.Config{BaseURL: srv.URL, Model: "test"})
	cfg := config.Default()

	// The only words end by 8s; the caption stays up to 20s and audio runs to 30s.
	cues := []transcript.Cue{
		{Start: 0, End: 4, Text: "This is an episode about stars."},
		{Start: 4, End: 20, Text: "Thanks for watching."},
	}
	for _, keepTail := range []bool{false, true} {
		ch := config.Channel{Slug: "test-channel", NoWeights: true, BitrateKbps: 64, KeepTail: keepTail}
		_, _, keep, err := RenderEpisode(context.Background(), cfg, ch, cache, client, video, cues, RenderOptions{NoSnap: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := transcript.SpeechEnd(cues)
		if keepTail {
			want = 30
		}
		if len(keep) != 1 || keep[0].Start != 0 || math.Abs(keep[0].End-want) > 0.05 {
			t.Errorf("keep_tail=%v: kept %+v, want [0, %.2f]", keepTail, keep, want)
		}
	}
}
