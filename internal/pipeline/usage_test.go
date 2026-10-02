package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/usage"
	"github.com/cwebley/shearcast/internal/youtube"
)

func TestFailedDetectionAndAudioKeepUsageAcrossRestart(t *testing.T) {
	for _, stage := range []string{"detection", "audio"} {
		t.Run(stage, func(t *testing.T) {
			f := newLifecycleFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantTokens := 0
			if stage == "detection" {
				f.audio(t)
				wantTokens = 100
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprint(w, `{"answers":{},"usage":{"input_tokens":100,"output_tokens":5,"cost":0.0000042}}`)
				}))
				defer srv.Close()
				f.runner.NewClient = func() (*jev.Client, error) { return jev.New(jev.Config{BaseURL: srv.URL, Model: usage.JevModel}), nil }
			} else {
				cancelAtAudioFetch(t, f, cancel)
			}
			ep, err := f.runner.Run(ctx, request(Render))
			if stage == "audio" && !errors.Is(err, context.Canceled) {
				t.Fatalf("audio fetch was not canceled: %v", err)
			}
			if err == nil || ep.Usage == nil || ep.Usage.Total.InputTokens != wantTokens || ep.Usage.Latest.FinishedAt.IsZero() {
				t.Fatalf("failed attempt lost usage: %+v %v", ep, err)
			}
			if f.runner.Usage.InputTokens != wantTokens || ep.Stage != state.Pending {
				t.Fatal("failure became successful render or lost invocation subtotal")
			}
			f.reopen(t)
			saved, _ := f.runner.State.Episode("show", lifecycleID)
			if saved.Usage.Total.InputTokens != wantTokens || saved.LastError == "" {
				t.Fatal("failed usage did not survive restart")
			}
		})
	}
}

func TestPublicationRetryAndFailedReplacementDoNotEraseOrDuplicateUsage(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	ctx := context.Background()
	f.store.beforePut = func(key string) error {
		if strings.HasSuffix(key, "feed.xml") {
			return errors.New("feed unavailable")
		}
		return nil
	}
	ep, err := f.runner.Run(ctx, request(Sync))
	if err == nil || ep.Usage.Total.InputTokens != 100 {
		t.Fatalf("missing publication failure usage: %+v %v", ep, err)
	}
	f.reopen(t)
	f.runner.Usage = usage.Summary{}
	f.store.beforePut = nil
	ep, err = f.runner.Run(ctx, request(Publish))
	if err != nil || f.runner.Usage.Attempts != 0 || ep.Usage.Total.InputTokens != 100 {
		t.Fatalf("retry counted historical usage: %+v %v", ep, err)
	}
	// A source-stage replacement failure preserves publication and prior usage.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cancelAtAudioFetch(t, f, cancel)
	ep, err = f.runner.Run(ctx, request(Render))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("audio fetch was not canceled: %v", err)
	}
	if !ep.HasPublished || ep.Usage.Total.InputTokens != 100 || ep.Usage.Latest.Usage.Attempts != 0 || f.runner.Usage.Attempts != 0 {
		t.Fatalf("bad replacement accounting: %+v %v", ep, err)
	}
}

// cancelAtAudioFetch cancels when the runner starts fetching source audio. The
// downloader is a stub, so a missed cancellation fails rather than reaching
// the network.
func cancelAtAudioFetch(t *testing.T, f *lifecycleFixture, cancel context.CancelFunc) {
	t.Helper()
	old := youtube.Binary
	t.Cleanup(func() { youtube.Binary = old })
	youtube.Binary = filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(youtube.Binary, []byte("#!/bin/sh\necho 'stub downloader reached' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.runner.Progress = func(line string) {
		// Runner prefixes each message with its channel and episode.
		if _, msg, _ := strings.Cut(line, lifecycleID+": "); strings.HasPrefix(msg, "fetching full audio") {
			cancel()
		}
	}
}

func TestSyncUsageIncludesSuccessfulAndFailedChannelsAndNoWorkRun(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) {
		return []youtube.Video{f.source.video}, nil
	}
	ch := request(Sync).Channel
	ch.URL = "https://youtube.com/@show"
	check(f.runner.State.StartSync([]string{"show", "other"}))
	outcomes, err := f.runner.SyncChannel(ctx, ch)
	check(err)
	if len(outcomes) != 1 || outcomes[0].Usage.InputTokens != 100 {
		t.Fatal("missing successful episode subtotal")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"answers":{},"usage":{"input_tokens":200,"output_tokens":20,"cost":0}}`)
	}))
	defer srv.Close()
	f.runner.NewClient = func() (*jev.Client, error) { return jev.New(jev.Config{BaseURL: srv.URL, Model: usage.JevModel}), nil }
	ch.Slug = "other"
	f.audio(t)
	outcomes, err = f.runner.SyncChannel(ctx, ch)
	if err == nil || len(outcomes) != 1 || outcomes[0].Usage.InputTokens != 200 {
		t.Fatal("missing failed episode subtotal")
	}
	check(f.runner.State.FinishSync(err))
	f.reopen(t)
	snapshot, err := state.ReadSnapshot(f.statePath)
	check(err)
	if snapshot.LastRun.Usage.InputTokens != 300 || snapshot.Sync["show"].Latest.Usage.InputTokens != 100 || snapshot.Sync["other"].Latest.Usage.InputTokens != 200 || snapshot.LastRun.Result != "failed" {
		t.Fatalf("wrong sync totals: %+v", snapshot.LastRun)
	}
	// A metadata-only scoped run records known zero, not historical episode cost.
	check(f.runner.State.StartSync([]string{"show"}))
	ch.Slug = "show"
	_, err = f.runner.SyncChannel(ctx, ch)
	check(err)
	check(f.runner.State.FinishSync(nil))
	snapshot, err = state.ReadSnapshot(f.statePath)
	check(err)
	if snapshot.LastRun.Usage == nil || snapshot.LastRun.Usage.Attempts != 0 || snapshot.Episodes["show"][lifecycleID].Usage.Total.InputTokens != 100 || snapshot.Sync["other"].Latest.Usage.InputTokens != 200 {
		t.Fatal("scoped no-work sync changed unrelated history")
	}
}

func TestPlanningExcludesIncompleteUsageAndUnknownModelRate(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	ctx := context.Background()
	ep, err := f.runner.Run(ctx, request(Render))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(ep.RenderPath + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var record RenderRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record.Usage.MissingUsage = 1
	if err := writeRenderRecord(ep.RenderPath+".json", record); err != nil {
		t.Fatal(err)
	}
	f.source.video.ID, f.source.video.Duration = "newvideo001", 3600
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) {
		return []youtube.Video{f.source.video}, nil
	}
	ch := config.Channel{Slug: "show", URL: "https://youtube.com/@show", NoWeights: true}
	p, err := f.runner.Plan(ctx, ch)
	if err != nil || p.CostKnown || p.HistorySamples != 0 {
		t.Fatalf("partial history was priced: %+v %v", p, err)
	}
	record.Usage.MissingUsage = 0
	record.Model = "other/model"
	if err := writeRenderRecord(ep.RenderPath+".json", record); err != nil {
		t.Fatal(err)
	}
	f.runner.Config.Jev.Model = record.Model
	p, err = f.runner.Plan(ctx, ch)
	if err != nil || p.CostKnown || p.HistorySamples != 1 || p.Pricing != nil {
		t.Fatalf("unknown model borrowed Jev rate: %+v %v", p, err)
	}
}

type recoveringUsageSource struct {
	Source
	beforeInfo func(string)
}

func (s recoveringUsageSource) Info(ctx context.Context, id string) (*youtube.Video, error) {
	s.beforeInfo(id)
	v, err := s.Source.Info(ctx, id)
	if v != nil {
		v.ID = id
	}
	return v, err
}

func TestUsageCheckpointFailureStopsRemainingEpisodes(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	const nextID = "newvideo001"
	var requests atomic.Int32
	restore := func() {
		if info, err := os.Stat(f.statePath); err == nil && info.IsDir() {
			if err := os.Remove(f.statePath); err != nil {
				t.Error(err)
				return
			}
			if err := os.Rename(f.statePath+".saved", f.statePath); err != nil {
				t.Error(err)
			}
		}
	}
	defer restore()
	f.runner.Source = recoveringUsageSource{Source: f.source, beforeInfo: func(id string) {
		if id == nextID {
			restore()
		}
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			// The request-start checkpoint is saved. Make the result checkpoint
			// fail, with a recoverable filesystem error rather than a dead store.
			if err := os.Rename(f.statePath, f.statePath+".saved"); err != nil {
				t.Error(err)
			}
			if err := os.Mkdir(f.statePath, 0700); err != nil {
				t.Error(err)
			}
		}
		fmt.Fprint(w, `{"answers":{},"usage":{"input_tokens":100,"output_tokens":5,"cost":0.001}}`)
	}))
	defer srv.Close()
	f.runner.NewClient = func() (*jev.Client, error) { return jev.New(jev.Config{BaseURL: srv.URL, Model: usage.JevModel}), nil }
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) {
		return []youtube.Video{f.source.video, {ID: nextID, Duration: 3, UploadDate: "20260921"}}, nil
	}
	ch := request(Sync).Channel
	ch.URL = "https://youtube.com/@show"
	outcomes, err := f.runner.SyncChannel(context.Background(), ch)
	if !errors.Is(err, usage.ErrCheckpoint) || len(outcomes) != 1 || requests.Load() != 1 {
		t.Fatalf("continued after accounting failure: requests=%d outcomes=%d error=%v", requests.Load(), len(outcomes), err)
	}
	if f.runner.Usage.InputTokens != 100 || f.runner.Usage.ProviderCost != .001 {
		t.Fatal("lost in-memory observations after failed write")
	}
}
