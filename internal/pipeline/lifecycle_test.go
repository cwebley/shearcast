package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/fileutil"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/render"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/storage"
	"github.com/cwebley/shearcast/internal/transcript"
	"github.com/cwebley/shearcast/internal/youtube"
)

const lifecycleID = "abcdefghijk"

type memoryPublisher struct {
	objects      map[string][]byte
	audioPuts    int
	beforePut    func(string) error
	beforeDelete func(string) error
}

func (p *memoryPublisher) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	if p.beforePut != nil {
		if err := p.beforePut(key); err != nil {
			return "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	if int64(len(data)) != size {
		return "", fmt.Errorf("incorrect size")
	}
	p.objects[key] = data
	if strings.HasSuffix(key, ".m4a") {
		p.audioPuts++
	}
	return p.PublicURL(key), nil
}
func (p *memoryPublisher) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, ok := p.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return data, nil
}
func (p *memoryPublisher) PublicURL(key string) string { return "https://example.test/" + key }

func (p *memoryPublisher) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.beforeDelete != nil {
		if err := p.beforeDelete(key); err != nil {
			return err
		}
	}
	delete(p.objects, key)
	return nil
}

type testSource struct {
	video                      youtube.Video
	infoErr, cuesErr           error
	infos, refreshes, captions int
}

func (s *testSource) Info(context.Context, string) (*youtube.Video, error) {
	s.infos++
	v := s.video
	return &v, s.infoErr
}
func (s *testSource) RefreshInfo(context.Context, string) (*youtube.Video, error) {
	s.refreshes++
	v := s.video
	return &v, s.infoErr
}
func (s *testSource) Cues(context.Context, *youtube.Video) ([]transcript.Cue, error) {
	s.captions++
	return []transcript.Cue{{Start: 0, End: 3, Text: "An episode about stars and how they form."}}, s.cuesErr
}

type lifecycleFixture struct {
	runner     *Runner
	source     *testSource
	store      *memoryPublisher
	statePath  string
	modelCalls atomic.Int32
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	if _, err := exec.LookPath(render.Binary); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	f := &lifecycleFixture{statePath: filepath.Join(dir, "state.json"),
		source: &testSource{video: youtube.Video{ID: lifecycleID, Title: "Original &amp; title", Description: "Original description", Duration: 3, UploadDate: "20260920", Thumbnail: "https://example.test/art.jpg"}},
		store:  &memoryPublisher{objects: map[string][]byte{}},
	}
	st, err := state.Open(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.modelCalls.Add(1)
		var req jev.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		answers := map[string]jev.Answer{}
		for id := range req.Questions {
			answers[id] = jev.Answer{Type: "choice", Choice: "content", Probabilities: map[string]float64{"content": 1, "sponsor": 0, "selfpromo": 0, "credits": 0}}
		}
		json.NewEncoder(w).Encode(jev.Response{Answers: answers, Usage: jev.Usage{InputTokens: 100}})
	}))
	f.runner = &Runner{Config: config.Default(), Cache: youtube.Cache{Dir: filepath.Join(dir, "cache")}, State: st,
		Source: f.source, Publisher: f.store, NewClient: func() (*jev.Client, error) {
			return jev.New(jev.Config{BaseURL: srv.URL, Model: "typesafe/jev-1.13"}), nil
		},
	}
	t.Cleanup(func() { f.runner.State.Close(); srv.Close() })
	return f
}

func (f *lifecycleFixture) audio(t *testing.T) {
	t.Helper()
	path := f.runner.Cache.SourceAudioPath(lifecycleID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(render.Binary, "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=3", "-c:a", "aac", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
}
func (f *lifecycleFixture) reopen(t *testing.T) {
	t.Helper()
	if err := f.runner.State.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	f.runner.State = st
}
func request(action Action) EpisodeRequest {
	return EpisodeRequest{Action: action, Target: lifecycleID, Channel: config.Channel{Slug: "show", Name: "Show", NoWeights: true}}
}

func TestPublishFailureResumesAfterRestartWithoutSourceOrModel(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	f.store.beforePut = func(key string) error {
		if _, err := os.Stat(f.runner.Cache.SourceAudioPath(lifecycleID)); !os.IsNotExist(err) {
			t.Error("source still exists when publication starts")
		}
		if strings.HasSuffix(key, "feed.xml") {
			return errors.New("feed upload failed")
		}
		return nil
	}
	episode, err := f.runner.Run(context.Background(), request(Sync))
	if err == nil || episode.Stage != state.Rendered {
		t.Fatalf("expected retryable render, got %+v, %v", episode, err)
	}
	if f.store.audioPuts != 1 || f.modelCalls.Load() != 1 {
		t.Fatal("first attempt did not reach partial publication")
	}
	checkpoint, _ := f.runner.State.Episode("show", lifecycleID)
	if checkpoint.LastError == "" || checkpoint.AudioSHA256 == "" {
		t.Fatal("missing durable failure checkpoint")
	}
	before, err := os.Stat(episode.RenderPath)
	if err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	f.source.infoErr, f.source.cuesErr = errors.New("YouTube unavailable"), errors.New("captions unavailable")
	f.runner.NewClient = func() (*jev.Client, error) {
		t.Error("model requested on publish retry")
		return nil, errors.New("no key")
	}
	f.store.beforePut = nil
	if ids := f.runner.ReadyToPublish(request(Sync).Channel); len(ids) != 1 || ids[0] != lifecycleID {
		t.Fatalf("pending publication not discoverable: %v", ids)
	}
	episode, err = f.runner.Run(context.Background(), request(Sync))
	if err != nil || episode.Stage != state.Published {
		t.Fatalf("resume: %+v, %v", episode, err)
	}
	after, err := os.Stat(episode.RenderPath)
	if err != nil || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("retry rewrote rendered audio")
	}
	if f.modelCalls.Load() != 1 || f.source.captions != 1 || f.source.infos != 1 {
		t.Fatal("retry fetched source or used the model")
	}
	f.reopen(t)
	if !f.runner.State.IsProcessed("show", lifecycleID) {
		t.Fatal("published checkpoint did not survive restart")
	}
}

func TestSidecarRecoversCrashBeforeRenderedCheckpoint(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	ep, err := f.runner.Run(context.Background(), request(Render))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the checkpoint present immediately before detection. The durable
	// audio and sidecar have finished, but the rendered state save never ran.
	f.audio(t)
	if err := f.runner.State.Save("show", lifecycleID, state.Episode{Stage: state.Pending, RenderPath: ep.RenderPath, RenderID: ep.RenderID}); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	f.source.infoErr = errors.New("offline")
	if err := f.runner.Recover(context.Background(), request(Sync).Channel); err != nil {
		t.Fatal(err)
	}
	if ids := f.runner.ReadyToPublish(request(Sync).Channel); len(ids) != 0 {
		t.Fatal("local-only sidecar recovery queued publication")
	}
	recovered, _ := f.runner.State.Episode("show", lifecycleID)
	if recovered.Stage != state.Rendered {
		t.Fatal("sidecar recovery did not complete the checkpoint")
	}
	if _, err := os.Stat(f.runner.Cache.SourceAudioPath(lifecycleID)); !os.IsNotExist(err) {
		t.Fatal("sidecar recovery did not remove source")
	}
	f.runner.NewClient = func() (*jev.Client, error) { t.Fatal("repeated paid detection"); return nil, nil }
	ep, err = f.runner.Run(context.Background(), request(Sync))
	if err != nil || ep.Stage != state.Published || ep.Video == nil {
		t.Fatalf("sidecar recovery: %+v, %v", ep, err)
	}
}

func TestWaitingCaptionsAreRetriedAndFailuresStayDistinct(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		stage     state.Stage
		wantError bool
	}{
		{"missing", youtube.ErrCaptionsUnavailable, state.Waiting, false},
		{"rate limit", errors.New("HTTP 429"), state.Pending, true},
		{"malformed", errors.New("malformed VTT"), state.Pending, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.source.cuesErr = tc.err
			f.runner.Progress = func(s string) {
				if strings.HasPrefix(s, "fetching full audio") {
					t.Error("attempted audio acquisition before usable captions")
				}
			}
			ep, err := f.runner.Run(context.Background(), request(Sync))
			if (err != nil) != tc.wantError || ep.Stage != tc.stage {
				t.Fatalf("got %+v, %v", ep, err)
			}
			if f.modelCalls.Load() != 0 || f.store.audioPuts != 0 {
				t.Fatal("missing/failed captions produced audio")
			}
			if len(f.runner.ReadyToPublish(request(Sync).Channel)) != 0 {
				t.Fatal("waiting episode bypassed selection window")
			}
			f.reopen(t)
			f.source.cuesErr = nil
			f.runner.Progress = nil
			f.audio(t)
			ep, err = f.runner.Run(context.Background(), request(Sync))
			if err != nil || ep.Stage != state.Published {
				t.Fatalf("retry: %+v, %v", ep, err)
			}
		})
	}
}

func TestPublishedMetadataRefreshKeepsGUIDAndEnclosure(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	ep, err := f.runner.Run(context.Background(), request(Sync))
	if err != nil {
		t.Fatal(err)
	}
	original, err := feed.Parse(f.store.objects["show/feed.xml"])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ep.RenderPath); err != nil {
		t.Fatal(err)
	}
	f.source.video.Title = "Updated &amp; title"
	f.source.video.Description = "New source description"
	f.source.video.UploadDate = "" // preserve the existing source date
	f.source.cuesErr = errors.New("captions inaccessible")
	f.runner.NewClient = func() (*jev.Client, error) { t.Fatal("metadata refresh called model"); return nil, nil }
	req := request(Sync)
	req.Channel.Title = "New show title"
	_, err = f.runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := feed.Parse(f.store.objects["show/feed.xml"])
	if err != nil {
		t.Fatal(err)
	}
	a, b := original.Items[0], updated.Items[0]
	if b.ID != a.ID || b.AudioURL != a.AudioURL || b.AudioBytes != a.AudioBytes || b.Duration != a.Duration || !b.PublishedAt.Equal(a.PublishedAt) {
		t.Fatalf("refresh changed episode identity/media: %+v -> %+v", a, b)
	}
	if b.Title != "Updated & title" || updated.Title != "New show title" {
		t.Fatalf("metadata not refreshed: %+v", updated)
	}
	if f.source.refreshes != 1 || f.source.captions != 1 || f.store.audioPuts != 1 {
		t.Fatal("refresh fetched captions or republished audio")
	}
}

func TestExplicitPublishRecordsHistoryWithoutCaptionsOrModel(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	legacy := filepath.Join(t.TempDir(), "old-render.m4a")
	data, err := os.ReadFile(f.runner.Cache.SourceAudioPath(lifecycleID))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, data, 0o600); err != nil {
		t.Fatal(err)
	}
	f.source.cuesErr = errors.New("must not fetch captions")
	f.runner.NewClient = nil
	req := request(Publish)
	req.AudioPath = legacy
	ep, err := f.runner.Run(context.Background(), req)
	if err != nil || ep.Stage != state.Published {
		t.Fatalf("manual publish: %+v, %v", ep, err)
	}
	if !f.runner.State.IsProcessed("show", lifecycleID) || f.source.captions != 0 || f.modelCalls.Load() != 0 {
		t.Fatal("manual publication bookkeeping/dependencies are incorrect")
	}
}

func TestChannelRendersAreIndependentAndCorruptionDoesNotSpendAgain(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	a, err := f.runner.Run(context.Background(), request(Render))
	if err != nil {
		t.Fatal(err)
	}
	f.audio(t)
	req := request(Render)
	req.Channel.Slug, req.Channel.BitrateKbps = "other", 64
	b, err := f.runner.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if a.RenderPath == b.RenderPath {
		t.Fatal("channels share an artifact")
	}
	if _, err := os.Stat(a.RenderPath); err != nil {
		t.Fatal("second render removed first channel output")
	}
	if err := os.WriteFile(a.RenderPath, []byte("corrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := f.modelCalls.Load()
	if _, err := f.runner.Run(context.Background(), request(Sync)); err == nil {
		t.Fatal("accepted corrupted render")
	}
	if f.modelCalls.Load() != before || f.store.audioPuts != 0 {
		t.Fatal("corruption triggered paid reprocessing or publication")
	}
}

func TestCancellationDuringPublicationKeepsRetryableRender(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.store.beforePut = func(key string) error {
		if strings.HasSuffix(key, "feed.xml") {
			cancel()
		}
		return nil
	}
	ep, err := f.runner.Run(ctx, request(Sync))
	if !errors.Is(err, context.Canceled) || ep.Stage != state.Rendered {
		t.Fatalf("cancellation lost checkpoint: %+v, %v", ep, err)
	}
	f.reopen(t)
	f.store.beforePut = nil
	f.runner.NewClient = func() (*jev.Client, error) { t.Fatal("model called after interrupted publication"); return nil, nil }
	ep, err = f.runner.Run(context.Background(), request(Sync))
	if err != nil || ep.Stage != state.Published {
		t.Fatalf("canceled publication did not resume: %+v, %v", ep, err)
	}
}

func TestLegacyPublishedStateRefreshesWithoutRerendering(t *testing.T) {
	f := newLifecycleFixture(t)
	if err := f.runner.State.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.statePath, []byte(`{"processed":{"show":["abcdefghijk"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	fd := feed.Feed{Title: "Old show", SelfURL: "https://example.test/show/feed.xml"}
	fd.Upsert(feed.Item{ID: lifecycleID, Title: "Old title", PublishedAt: parseUploadDate("20260920"), AudioURL: "https://example.test/retained.m4a", AudioBytes: 1234})
	data, err := fd.XML()
	if err != nil {
		t.Fatal(err)
	}
	f.store.objects["show/feed.xml"] = data
	f.runner.NewClient = nil
	ep, err := f.runner.Run(context.Background(), request(Sync))
	if err != nil || ep.Stage != state.Published || ep.AudioURL != "https://example.test/retained.m4a" {
		t.Fatalf("legacy migration: %+v, %v", ep, err)
	}
	if f.modelCalls.Load() != 0 || f.source.captions != 0 || f.store.audioPuts != 0 {
		t.Fatal("migration rerendered existing content")
	}
}

func TestMissingPublishedFeedDoesNotTriggerPaidReprocessing(t *testing.T) {
	f := newLifecycleFixture(t)
	if err := f.runner.State.MarkProcessed("show", lifecycleID); err != nil {
		t.Fatal(err)
	}
	ep, err := f.runner.Run(context.Background(), request(Sync))
	if err == nil || ep.Stage != state.Published || f.modelCalls.Load() != 0 || f.store.audioPuts != 0 {
		t.Fatalf("missing feed reprocessed history: %+v, %v", ep, err)
	}
}

func TestOutputCannotAliasTheSource(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	req := request(Render)
	req.RenderOptions.OutPath = f.runner.Cache.SourceAudioPath(lifecycleID)
	if _, err := f.runner.Run(context.Background(), req); err == nil {
		t.Fatal("accepted source as rendered output")
	}
	if _, err := os.Stat(req.RenderOptions.OutPath); err != nil {
		t.Fatal("source was deleted")
	}
	if f.modelCalls.Load() != 0 {
		t.Fatal("invalid output spent model tokens")
	}
}

func TestManualRenderDoesNotQueueUnselectedPublication(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	ep, err := f.runner.Run(context.Background(), request(Render))
	if err != nil || ep.Stage != state.Rendered {
		t.Fatalf("render: %+v, %v", ep, err)
	}
	if ids := f.runner.ReadyToPublish(request(Sync).Channel); len(ids) != 0 {
		t.Fatalf("local-only render queued a publication: %v", ids)
	}
}

func TestInterruptedInstallationRecoversOnEitherSideOfAudioRename(t *testing.T) {
	for _, tc := range []struct{ renamed, publish bool }{{false, false}, {false, true}, {true, false}, {true, true}} {
		t.Run(fmt.Sprintf("audio-renamed-%v-publish-%v", tc.renamed, tc.publish), func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.audio(t)
			ep, err := f.runner.Run(context.Background(), request(Render))
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
			staged := filepath.Join(filepath.Dir(ep.RenderPath), stagePrefix(ep.RenderPath)+"interrupted.m4a")
			if err := os.Rename(ep.RenderPath, staged); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(ep.RenderPath + ".json"); err != nil {
				t.Fatal(err)
			}
			journal, err := json.Marshal(renderInstall{Staged: filepath.Base(staged), Record: record})
			if err != nil {
				t.Fatal(err)
			}
			if err := fileutil.WriteAtomic(ep.RenderPath+".install.json", journal, 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.renamed {
				if err := os.Rename(staged, ep.RenderPath); err != nil {
					t.Fatal(err)
				}
			}
			f.audio(t) // the source still exists at this interruption point
			if err := f.runner.State.Save("show", lifecycleID, state.Episode{Stage: state.Pending, PublishPending: tc.publish, RenderPath: ep.RenderPath, RenderID: ep.RenderID}); err != nil {
				t.Fatal(err)
			}
			f.reopen(t)
			f.source.infoErr = errors.New("offline")
			f.runner.NewClient = nil
			if err := f.runner.Recover(context.Background(), request(Sync).Channel); err != nil {
				t.Fatal(err)
			}
			ids := f.runner.ReadyToPublish(request(Sync).Channel)
			if (len(ids) == 1) != tc.publish {
				t.Fatalf("wrong publication eligibility after recovery: %v", ids)
			}
			recovered, _ := f.runner.State.Episode("show", lifecycleID)
			if recovered.Stage != state.Rendered || recovered.AudioSHA256 == "" {
				t.Fatalf("recovered install not checkpointed: %+v", recovered)
			}
			if _, err := os.Stat(f.runner.Cache.SourceAudioPath(lifecycleID)); !os.IsNotExist(err) {
				t.Fatal("recovered render retained source audio")
			}
			if tc.publish {
				ep, err = f.runner.Run(context.Background(), request(Sync))
				if err != nil || ep.Stage != state.Published {
					t.Fatalf("resuming installation: %+v, %v", ep, err)
				}
			} else if len(f.store.objects) != 0 {
				t.Fatal("local-only render was published during recovery")
			}
			if f.modelCalls.Load() != 1 {
				t.Fatal("installation recovery repeated detection")
			}
			if _, err := os.Stat(ep.RenderPath + ".install.json"); !os.IsNotExist(err) {
				t.Fatal("completed installation journal remains")
			}
		})
	}
}

func TestFailedManualRerenderPreservesPublicationHistory(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	ep, err := f.runner.Run(context.Background(), request(Sync))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ep.RenderPath); err != nil {
		t.Fatal(err)
	}
	f.source.cuesErr = errors.New("caption request failed")
	if _, err := f.runner.Run(context.Background(), request(Render)); err == nil {
		t.Fatal("expected failed manual replacement")
	}
	f.reopen(t)
	if !f.runner.State.IsProcessed("show", lifecycleID) {
		t.Fatal("manual replacement lost published history")
	}
	if err := f.runner.Recover(context.Background(), request(Sync).Channel); err != nil {
		t.Fatal(err)
	}
	f.runner.NewClient = nil
	ep, err = f.runner.Run(context.Background(), request(Sync))
	if err != nil || !ep.HasPublished || ep.LastError == "" || f.modelCalls.Load() != 1 || f.store.audioPuts != 1 {
		t.Fatalf("sync retried a failed manual replacement: %+v, %v", ep, err)
	}
}

func TestMissingSourceCannotBeAliasedThroughSymlinkedParent(t *testing.T) {
	f := newLifecycleFixture(t)
	if err := os.MkdirAll(f.runner.Cache.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "cache-alias")
	if err := os.Symlink(f.runner.Cache.Dir, alias); err != nil {
		t.Fatal(err)
	}
	req := request(Render)
	req.RenderOptions.OutPath = filepath.Join(alias, lifecycleID, "audio.m4a")
	if _, err := f.runner.Run(context.Background(), req); err == nil {
		t.Fatal("accepted a future source alias")
	}
	if f.modelCalls.Load() != 0 || f.source.infos != 0 {
		t.Fatal("invalid output started processing")
	}
}

func TestRecoveryReclaimsAbandonedWorkWithoutDeletingCompletedFiles(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	ep, err := f.runner.Run(context.Background(), request(Render))
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(filepath.Dir(f.runner.Cache.SourceAudioPath(lifecycleID)), ".download-abandoned")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "source.webm.part"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(filepath.Dir(ep.RenderPath), stagePrefix(ep.RenderPath)+"abandoned.m4a")
	if err := os.WriteFile(staged, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	if err := f.runner.Recover(context.Background(), request(Sync).Channel); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{work, staged} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("abandoned work remains: %s", path)
		}
	}
	for _, path := range []string{ep.RenderPath, ep.RenderPath + ".json"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("completed artifact removed: %s", path)
		}
	}
}

func TestOlderSidecarDoesNotQueuePaidWorkOutsideSelection(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	ep, err := f.runner.Run(context.Background(), request(Render))
	if err != nil {
		t.Fatal(err)
	}
	ep.Stage, ep.PublishPending, ep.RenderID = state.Pending, true, "interrupted-new-attempt"
	ep.AudioSHA256 = ""
	if err := f.runner.State.Save("show", lifecycleID, ep); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	if err := f.runner.Recover(context.Background(), request(Sync).Channel); err != nil {
		t.Fatal(err)
	}
	if ids := f.runner.ReadyToPublish(request(Sync).Channel); len(ids) != 0 {
		t.Fatalf("old sidecar queued unfinished processing: %v", ids)
	}
	current, _ := f.runner.State.Episode("show", lifecycleID)
	if current.Stage != state.Pending || f.modelCalls.Load() != 1 {
		t.Fatal("recovery rerendered an unfinished attempt")
	}
}

func TestRenderAfterLegacyImportUsesChannelOutputByDefault(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	legacy := filepath.Join(t.TempDir(), "legacy.m4a")
	data, err := os.ReadFile(f.runner.Cache.SourceAudioPath(lifecycleID))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, data, 0o600); err != nil {
		t.Fatal(err)
	}
	req := request(Publish)
	req.AudioPath = legacy
	if _, err := f.runner.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	f.audio(t)
	ep, err := f.runner.Run(context.Background(), request(Render))
	if err != nil {
		t.Fatal(err)
	}
	if ep.RenderPath == legacy {
		t.Fatal("default rerender overwrote explicitly imported file")
	}
	got, err := os.ReadFile(legacy)
	if err != nil || string(got) != string(data) {
		t.Fatal("legacy output changed")
	}
}

func TestRecoveryKeepsPublishedAudioWhenFeedIsMissing(t *testing.T) {
	f := newLifecycleFixture(t)
	p, err := storage.NewFilesystem(t.TempDir(), "http://podcasts.test")
	if err != nil {
		t.Fatal(err)
	}
	f.runner.Publisher = p
	f.audio(t)
	ctx := context.Background()
	ep, err := f.runner.Run(ctx, request(Sync))
	if err != nil {
		t.Fatal(err)
	}
	// Losing the feed is not evidence that its episodes are obsolete.
	if err := p.Delete(ctx, "show/feed.xml"); err != nil {
		t.Fatal(err)
	}
	if err := f.runner.Recover(ctx, request(Sync).Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Get(ctx, ep.PublishedAudioKey); err != nil {
		t.Fatalf("recovery deleted published audio: %v", err)
	}
}

func TestMissingLocalReplacementDoesNotBlockSync(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	ctx := context.Background()
	if _, err := f.runner.Run(ctx, request(Sync)); err != nil {
		t.Fatal(err)
	}
	f.audio(t)
	req := request(Render)
	req.RenderOptions.OutPath = filepath.Join(t.TempDir(), "replacement.m4a")
	ep, err := f.runner.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ep.RenderPath); err != nil {
		t.Fatal(err)
	}
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) {
		return []youtube.Video{f.source.video}, nil
	}
	ch := req.Channel
	ch.URL = "https://youtube.com/@show"
	if _, err := f.runner.SyncChannel(ctx, ch); err != nil {
		t.Fatalf("unpublished replacement blocked the committed publication's sync: %v", err)
	}
	saved, _ := f.runner.State.Episode("show", lifecycleID)
	if !saved.HasPublished || saved.LastError == "" {
		t.Fatalf("replacement failure not recorded on the published episode: %+v", saved)
	}
}
