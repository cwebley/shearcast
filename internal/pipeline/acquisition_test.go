package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/render"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/usage"
	"github.com/cwebley/shearcast/internal/youtube"
)

func TestSourceFailureMakesNoModelRequestsAndPreservesPublication(t *testing.T) {
	for _, stage := range []string{"download", "corrupt audio", "zero duration"} {
		t.Run(stage, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.audio(t)
			ctx := context.Background()
			published, err := f.runner.Run(ctx, request(Sync))
			if err != nil {
				t.Fatal(err)
			}
			original := make(map[string][]byte)
			for key, data := range f.store.objects {
				original[key] = bytes.Clone(data)
			}
			oldDownloader, oldProbe := youtube.Binary, render.ProbeBinary
			t.Cleanup(func() { youtube.Binary, render.ProbeBinary = oldDownloader, oldProbe })
			wantError := "probing source audio duration"
			switch stage {
			case "download":
				youtube.Binary = filepath.Join(t.TempDir(), "yt-dlp")
				if err := os.WriteFile(youtube.Binary, []byte("#!/bin/sh\nprintf 'fixture download failed' >&2\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
				wantError = "fetching audio"
			case "corrupt audio":
				if err := os.WriteFile(f.runner.Cache.SourceAudioPath(lifecycleID), []byte("not media"), 0600); err != nil {
					t.Fatal(err)
				}
			case "zero duration":
				f.audio(t)
				render.ProbeBinary = filepath.Join(t.TempDir(), "ffprobe")
				if err := os.WriteFile(render.ProbeBinary, []byte("#!/bin/sh\nprintf '0\\n'\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			f.runner.Usage = usage.Summary{}
			ep, err := f.runner.Run(ctx, request(Reprocess))
			if err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("expected %s failure, got %v", wantError, err)
			}
			if f.modelCalls.Load() != 1 || !reflect.DeepEqual(f.runner.Usage, usage.Summary{}) {
				t.Fatalf("source failure incurred new model work: %+v", f.runner.Usage)
			}
			// Unreadable audio must not stay cached, or every retry reuses it.
			if _, err := os.Stat(f.runner.Cache.SourceAudioPath(lifecycleID)); stage != "download" && !os.IsNotExist(err) {
				t.Fatalf("%s source audio still cached: %v", stage, err)
			}
			if !ep.HasPublished || ep.PublishedAudioSHA256 != published.PublishedAudioSHA256 || !reflect.DeepEqual(f.store.objects, original) {
				t.Fatal("source failure changed published history or objects")
			}
			f.reopen(t)
			saved, _ := f.runner.State.Episode("show", lifecycleID)
			if saved.Usage == nil || saved.Usage.Total.InputTokens != 100 || saved.Usage.Latest.FinishedAt.IsZero() ||
				!reflect.DeepEqual(saved.Usage.Latest.Usage, usage.Summary{}) || saved.LastError == "" {
				t.Fatalf("source failure did not preserve prior usage and known zero latest usage: %+v", saved)
			}
			// A retry pays for detection only after source acquisition succeeds.
			youtube.Binary, render.ProbeBinary = oldDownloader, oldProbe
			f.audio(t)
			ep, err = f.runner.Run(ctx, request(Reprocess))
			if err != nil || ep.Stage != state.Published || f.modelCalls.Load() != 2 || ep.Usage.Total.InputTokens != 200 {
				t.Fatalf("retry: %+v, %v", ep, err)
			}
		})
	}
}

func TestDownloadCompletesBeforeDetectionAndCleanupFollowsRender(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	source := f.runner.Cache.SourceAudioPath(lifecycleID)
	fixture := filepath.Join(t.TempDir(), "fixture.m4a")
	if err := os.Rename(source, fixture); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHEARCAST_TEST_AUDIO", fixture)
	oldDownloader := youtube.Binary
	t.Cleanup(func() { youtube.Binary = oldDownloader })
	youtube.Binary = filepath.Join(t.TempDir(), "yt-dlp")
	script := `#!/bin/sh
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    cp "$SHEARCAST_TEST_AUDIO" "$2"
    exit $?
  fi
  shift
done
exit 1
`
	if err := os.WriteFile(youtube.Binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var stages []string
	f.runner.Progress = func(line string) {
		// Runner prefixes each message with its channel and episode.
		_, s, _ := strings.Cut(line, lifecycleID+": ")
		// Stage completion messages now report timing as well as stage starts.
		if !strings.HasSuffix(s, "...") {
			return
		}
		switch {
		case strings.HasPrefix(s, "fetching full audio"):
			stages = append(stages, "audio")
			if f.source.captions != 1 || f.modelCalls.Load() != 0 {
				t.Error("audio acquisition did not follow captions and precede detection")
			}
		case strings.HasPrefix(s, "running detection"):
			stages = append(stages, "detection")
			duration, err := render.Probe(context.Background(), source)
			if err != nil || duration <= 0 {
				t.Errorf("detection started without downloaded audio: %s, %v", duration, err)
			}
		case strings.HasPrefix(s, "encoding AAC"):
			stages = append(stages, "render")
			if _, err := os.Stat(source); err != nil {
				t.Errorf("source removed before rendering: %v", err)
			}
		}
	}
	ep, err := f.runner.Run(context.Background(), request(Render))
	if err != nil || ep.Stage != state.Rendered || f.modelCalls.Load() != 1 {
		t.Fatalf("render: %+v, %v", ep, err)
	}
	if !reflect.DeepEqual(stages, []string{"audio", "detection", "render"}) {
		t.Fatalf("unexpected stage order: %v", stages)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source retained after durable render: %v", err)
	}
}
