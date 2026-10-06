package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/chapters"
	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/render"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/storage"
)

func sourceChapters(f *lifecycleFixture) {
	f.source.video.Chapters = []chapters.Source{{Start: 0, Title: "Intro"}, {Start: 1, Title: "Middle"}, {Start: 2, Title: "End"}}
	f.source.video.Description = "Source prose\n0:00 Intro\n0:01 Middle\n0:02 End\nhttps://example.test/"
}

func TestRenderTimelineUsesAudioEOFInsteadOfMetadataDuration(t *testing.T) {
	for _, withChapters := range []bool{false, true} {
		f := newLifecycleFixture(t)
		if withChapters {
			sourceChapters(f)
		}
		f.source.video.Duration = 3.5
		f.audio(t) // the audio is three seconds
		ep, err := f.runner.Run(context.Background(), request(Sync))
		if err != nil {
			t.Fatal(err)
		}
		if ep.PublishedTimeline == nil || ep.PublishedTimeline.Keep[0].End > 3.1 || ep.Video.Duration != 3.5 {
			t.Fatalf("timeline did not use actual source EOF: %+v", ep)
		}
	}
}

func chapterFeed(t *testing.T, p Publisher) feed.Item {
	t.Helper()
	data, err := p.Get(context.Background(), "show/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	fd, err := feed.Parse(data)
	if err != nil || len(fd.Items) != 1 {
		t.Fatalf("feed %#v %v", fd, err)
	}
	return fd.Items[0]
}

func publicChapters(t *testing.T, p Publisher, item feed.Item) []chapters.Chapter {
	t.Helper()
	if item.ChaptersURL == "" {
		return item.Chapters
	}
	key := strings.TrimPrefix(item.ChaptersURL, p.PublicURL(""))
	data, err := p.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version  string
		Chapters []chapters.Chapter
	}
	if err := json.Unmarshal(data, &doc); err != nil || doc.Version != "1.2.0" {
		t.Fatalf("chapter JSON %s: %v", data, err)
	}
	return doc.Chapters
}

func TestChapterAdaptersRefreshAndRemove(t *testing.T) {
	for _, backend := range []string{"filesystem", "r2"} {
		for _, scheme := range []string{"http", "https"} {
			t.Run(backend+"/"+scheme, func(t *testing.T) {
				f := newLifecycleFixture(t)
				sourceChapters(f)
				var p Publisher
				var err error
				if backend == "filesystem" {
					p, err = storage.NewFilesystem(t.TempDir(), scheme+"://podcasts.test")
				} else {
					p, err = storage.New(context.Background(), storage.Config{AccountID: "test", Bucket: "test", AccessKeyID: "test", SecretAccessKey: "test", PublicBaseURL: scheme + "://podcasts.test", HTTPClient: localR2HTTP(t)})
				}
				if err != nil {
					t.Fatal(err)
				}
				f.runner.Publisher = p
				f.audio(t)
				ep, err := f.runner.Run(context.Background(), request(Sync))
				if err != nil {
					t.Fatal(err)
				}
				initial := chapterFeed(t, p)
				if len(publicChapters(t, p, initial)) != 3 || (initial.ChaptersURL != "") != (scheme == "https") {
					t.Fatalf("wrong representation: %+v", initial)
				}
				if ep.PublishedTimeline == nil || !ep.ChaptersEnabled {
					t.Fatal("published provenance missing")
				}
				f.reopen(t)
				f.runner.NewClient = nil
				f.source.video.Chapters[1].Title = "Corrected middle"
				f.source.video.Description = strings.ReplaceAll(f.source.video.Description, "Middle", "Corrected middle")
				for range 2 {
					if _, err := f.runner.Run(context.Background(), request(Sync)); err != nil {
						t.Fatal(err)
					}
					item := chapterFeed(t, p)
					list := publicChapters(t, p, item)
					if list[1].Title != "Corrected middle" || list[1].Start != 1 || item.AudioURL != initial.AudioURL || item.ID != initial.ID || item.Duration != initial.Duration {
						t.Fatalf("refresh changed identity/timing: %+v %#v", item, list)
					}
				}
				if f.modelCalls.Load() != 1 || f.source.captions != 1 {
					t.Fatal("refresh did model/caption work")
				}
				if initial.ChaptersURL != "" {
					if _, err := p.Get(context.Background(), strings.TrimPrefix(initial.ChaptersURL, p.PublicURL(""))); !errors.Is(err, storage.ErrNotFound) {
						t.Fatal("old revision retained", err)
					}
				}
				// Source removes all chapters. The next refresh must clear both forms.
				f.source.video.Chapters = nil
				f.source.video.Description = "Description without chapters"
				if _, err := f.runner.Run(context.Background(), request(Sync)); err != nil {
					t.Fatal(err)
				}
				item := chapterFeed(t, p)
				if item.ChaptersURL != "" || len(item.Chapters) != 0 || item.Description != f.source.video.Description {
					t.Fatalf("stale chapters: %+v", item)
				}
				sourceChapters(f)
				if _, err := f.runner.Run(context.Background(), request(Sync)); err != nil {
					t.Fatal(err)
				}
				ep, _ = f.runner.State.Episode("show", lifecycleID)
				if err := f.runner.RemoveEpisode(context.Background(), request(Sync).Channel, lifecycleID); err != nil {
					t.Fatal(err)
				}
				for _, key := range append(ep.ChapterKeys, ep.AudioKeys...) {
					if _, err := p.Get(context.Background(), key); !errors.Is(err, storage.ErrNotFound) {
						t.Fatalf("removal retained %s: %v", key, err)
					}
				}
			})
		}
	}
}

func stageChapterCut(t *testing.T, f *lifecycleFixture, path string) {
	t.Helper()
	f.audio(t)
	keep := []render.Range{{Start: 0, End: 1}, {Start: 2, End: 3}}
	if err := render.Cut(context.Background(), f.runner.Cache.SourceAudioPath(lifecycleID), keep, path, render.CutOptions{Crossfade: .05}); err != nil {
		t.Fatal(err)
	}
	duration, err := render.Probe(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	hash, size, err := audioDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := RenderRecord{Version: 2, VideoID: lifecycleID, Channel: "show", Keep: keep, Cut: render.CutOptions{Crossfade: .05}, Source: f.source.video.Clone(), DurationSeconds: duration.Seconds(), AudioSHA256: hash, AudioBytes: size}
	if err := writeRenderRecord(path+".json", rec); err != nil {
		t.Fatal(err)
	}
}

func TestFailedReplacementKeepsAudioAndChaptersTogether(t *testing.T) {
	f := newLifecycleFixture(t)
	sourceChapters(f)
	f.audio(t)
	ctx := context.Background()
	old, err := f.runner.Run(ctx, request(Sync))
	if err != nil {
		t.Fatal(err)
	}
	oldItem := chapterFeed(t, f.store)
	oldAudio := append([]byte(nil), f.store.objects[old.PublishedAudioKey]...)
	path := filepath.Join(t.TempDir(), "replacement.m4a")
	stageChapterCut(t, f, path)
	f.store.beforePut = func(key string) error {
		if strings.HasSuffix(key, "feed.xml") {
			return errors.New("feed failed")
		}
		return nil
	}
	req := request(Publish)
	req.AudioPath = path
	ep, err := f.runner.Run(ctx, req)
	if err == nil || ep.PendingPublication == nil || len(ep.ChapterKeys) != 2 {
		t.Fatalf("missing retry journal: %+v %v", ep, err)
	}
	if !reflect.DeepEqual(chapterFeed(t, f.store), oldItem) || string(f.store.objects[old.PublishedAudioKey]) != string(oldAudio) || ep.PublishedTimeline.Duration != old.PublishedTimeline.Duration {
		t.Fatal("failed feed write changed live audio or chapters")
	}
	f.reopen(t)
	f.store.beforePut = nil
	f.source.infoErr = errors.New("source must not be needed")
	f.runner.NewClient = nil
	if err := f.runner.Recover(ctx, req.Channel); err != nil {
		t.Fatal(err)
	}
	ep, err = f.runner.Run(ctx, request(Publish))
	if err != nil {
		t.Fatal(err)
	}
	item := chapterFeed(t, f.store)
	list := publicChapters(t, f.store, item)
	if len(list) != 2 || list[1].Title != "End" || list[1].Start != .95 || item.AudioURL == oldItem.AudioURL || ep.PublishedTimeline.Duration >= 2 {
		t.Fatalf("replacement: %+v %#v", ep, list)
	}
	if _, ok := f.store.objects[old.PublishedAudioKey]; ok {
		t.Fatal("old audio retained")
	}
	if len(ep.AudioKeys) != 1 || len(ep.ChapterKeys) != 1 || ep.PendingPublication != nil || f.modelCalls.Load() != 1 {
		t.Fatalf("bad checkpoint %+v", ep)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("caller-owned replacement removed")
	}
}

func TestMetadataRefreshUsesPublishedTimelineDuringLocalRerender(t *testing.T) {
	f := newLifecycleFixture(t)
	sourceChapters(f)
	path := RenderPath(f.runner.Cache, request(Publish).Channel, lifecycleID)
	stageChapterCut(t, f, path)
	ep, err := f.runner.Run(context.Background(), request(Publish))
	if err != nil {
		t.Fatal(err)
	}
	published := ep.PublishedTimeline.Clone()
	f.audio(t)
	if _, err := f.runner.Run(context.Background(), request(Render)); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	f.runner.NewClient = nil
	f.source.video.Chapters[2].Title = "New ending title"
	f.source.video.Description = strings.ReplaceAll(f.source.video.Description, "End", "New ending title")
	for range 2 {
		ep, err = f.runner.Run(context.Background(), request(Sync))
		if err != nil {
			t.Fatal(err)
		}
		list := publicChapters(t, f.store, chapterFeed(t, f.store))
		if len(list) != 2 || list[1].Start != .95 || list[1].Title != "New ending title" || !reflect.DeepEqual(ep.PublishedTimeline, published) {
			t.Fatalf("pending timeline leaked: %+v %#v", ep, list)
		}
	}
}

func TestLegacyAndUnprovenImportsDoNotInventTimeline(t *testing.T) {
	f := newLifecycleFixture(t)
	sourceChapters(f)
	f.audio(t)
	ctx := context.Background()
	req := request(Publish)
	req.AudioPath = filepath.Join(t.TempDir(), "import.m4a")
	data, err := os.ReadFile(f.runner.Cache.SourceAudioPath(lifecycleID))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(req.AudioPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ep, err := f.runner.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	item := chapterFeed(t, f.store)
	if item.ChaptersURL != "" || len(item.Chapters) != 0 || !strings.Contains(item.Description, "original video") || ep.PublishedTimeline != nil {
		t.Fatalf("import fabricated provenance %+v", item)
	}
	// Simulate a pre-feature publication. Refresh keeps the existing behavior,
	// even if an unrelated record now happens to exist beside a local render.
	ep.ChaptersEnabled = false
	ep.PublishedTimeline = nil
	if err := f.runner.State.Save("show", lifecycleID, ep); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.Run(ctx, request(Sync)); err != nil {
		t.Fatal(err)
	}
	item = chapterFeed(t, f.store)
	if item.Description != f.source.video.Description || item.ChaptersURL != "" || len(item.Chapters) != 0 {
		t.Fatalf("legacy refresh migrated chapters: %+v", item)
	}
	if f.modelCalls.Load() != 0 {
		t.Fatal("metadata used model")
	}
}

func TestChapterDeletionFailureRecoversAfterFeedRemoval(t *testing.T) {
	f := newLifecycleFixture(t)
	sourceChapters(f)
	f.audio(t)
	ep, err := f.runner.Run(context.Background(), request(Sync))
	if err != nil {
		t.Fatal(err)
	}
	f.store.beforeDelete = func(key string) error {
		if strings.HasSuffix(key, ".chapters.json") {
			return errors.New("delete failed")
		}
		return nil
	}
	if err := f.runner.PurgeChannel(context.Background(), request(Sync).Channel); err == nil {
		t.Fatal("expected cleanup failure")
	}
	fd, err := feed.Parse(f.store.objects["show/feed.xml"])
	if err != nil || len(fd.Items) != 0 {
		t.Fatal("feed still references removed artifact")
	}
	f.reopen(t)
	f.store.beforeDelete = nil
	if err := f.runner.Recover(context.Background(), request(Sync).Channel); err != nil {
		t.Fatal(err)
	}
	for _, key := range append(ep.ChapterKeys, ep.AudioKeys...) {
		if _, ok := f.store.objects[key]; ok {
			t.Fatalf("purge retained %s", key)
		}
	}
	current, _ := f.runner.State.Episode("show", lifecycleID)
	if current.DeletePending || current.Stage != state.Published || !current.HasPublished {
		t.Fatalf("history lost: %+v", current)
	}
}

func TestFailedChapterRefreshPreservesLiveRevisionAndRecovers(t *testing.T) {
	for _, failedKey := range []string{".chapters.json", "feed.xml"} {
		t.Run(failedKey, func(t *testing.T) {
			f := newLifecycleFixture(t)
			sourceChapters(f)
			f.audio(t)
			ctx := context.Background()
			if _, err := f.runner.Run(ctx, request(Sync)); err != nil {
				t.Fatal(err)
			}
			before := chapterFeed(t, f.store)
			f.source.video.Chapters[1].Title = "Correction"
			f.source.video.Description = strings.ReplaceAll(f.source.video.Description, "Middle", "Correction")
			f.store.beforePut = func(key string) error {
				if strings.HasSuffix(key, failedKey) {
					return errors.New("publication unavailable")
				}
				return nil
			}
			if _, err := f.runner.Run(ctx, request(Sync)); err == nil {
				t.Fatal("expected publication error")
			}
			if !reflect.DeepEqual(chapterFeed(t, f.store), before) || publicChapters(t, f.store, before)[1].Title != "Middle" {
				t.Fatal("failed refresh changed live chapters")
			}
			f.reopen(t)
			f.store.beforePut = nil
			f.runner.NewClient = nil
			if err := f.runner.Recover(ctx, request(Sync).Channel); err != nil {
				t.Fatal(err)
			}
			ep, _ := f.runner.State.Episode("show", lifecycleID)
			if len(ep.ChapterKeys) != 1 {
				t.Fatalf("abandoned revision not reclaimed: %v", ep.ChapterKeys)
			}
			if _, err := f.runner.Run(ctx, request(Sync)); err != nil {
				t.Fatal(err)
			}
			if publicChapters(t, f.store, chapterFeed(t, f.store))[1].Title != "Correction" || f.modelCalls.Load() != 1 {
				t.Fatal("refresh retry failed")
			}
		})
	}
}

type lostFeedResponse struct {
	Publisher
	lost bool
}

func (p *lostFeedResponse) Put(ctx context.Context, key string, reader io.Reader, size int64, kind string) (string, error) {
	url, err := p.Publisher.Put(ctx, key, reader, size, kind)
	if err == nil && strings.HasSuffix(key, "feed.xml") && !p.lost {
		p.lost = true
		return "", errors.New("response lost after successful feed write")
	}
	return url, err
}

func TestRecoveryAdoptsTimelineAfterLostFeedResponse(t *testing.T) {
	f := newLifecycleFixture(t)
	sourceChapters(f)
	f.audio(t)
	ctx := context.Background()
	if _, err := f.runner.Run(ctx, request(Sync)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "replacement.m4a")
	stageChapterCut(t, f, path)
	f.runner.Publisher = NewSyncPublisher(&lostFeedResponse{Publisher: f.store})
	req := request(Publish)
	req.AudioPath = path
	ep, err := f.runner.Run(ctx, req)
	if err == nil || ep.PublishedTimeline.Duration < 2.9 {
		t.Fatalf("expected stale state checkpoint: %+v %v", ep, err)
	}
	if len(publicChapters(t, f.store, chapterFeed(t, f.store))) != 2 {
		t.Fatal("fixture did not publish replacement")
	}
	f.reopen(t)
	f.source.infoErr = errors.New("source unavailable")
	f.runner.NewClient = nil
	if err := f.runner.Recover(ctx, req.Channel); err != nil {
		t.Fatal(err)
	}
	ep, _ = f.runner.State.Episode("show", lifecycleID)
	if ep.PublishedTimeline.Duration >= 2 || ep.PublishedAudioKey != ep.PendingPublication.AudioKey {
		t.Fatalf("feed-confirmed revision not adopted: %+v", ep)
	}
	if _, err := f.runner.Run(ctx, request(Publish)); err != nil {
		t.Fatal(err)
	}
	if f.modelCalls.Load() != 1 {
		t.Fatal("recovery repeated detection")
	}
}

func TestFailedSameByteImportDoesNotReplacePublishedTimeline(t *testing.T) {
	f := newLifecycleFixture(t)
	sourceChapters(f)
	f.audio(t)
	ctx := context.Background()
	old, err := f.runner.Run(ctx, request(Sync))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "identical-without-record.m4a")
	if err := os.WriteFile(path, f.store.objects[old.PublishedAudioKey], 0o600); err != nil {
		t.Fatal(err)
	}
	f.store.beforePut = func(key string) error {
		if strings.HasSuffix(key, "feed.xml") {
			return errors.New("feed write failed")
		}
		return nil
	}
	req := request(Publish)
	req.AudioPath = path
	if _, err := f.runner.Run(ctx, req); err == nil {
		t.Fatal("expected feed failure")
	}
	f.reopen(t)
	f.store.beforePut = nil
	if err := f.runner.Recover(ctx, req.Channel); err != nil {
		t.Fatal(err)
	}
	ep, _ := f.runner.State.Episode("show", lifecycleID)
	if !reflect.DeepEqual(ep.PublishedTimeline, old.PublishedTimeline) {
		t.Fatal("unchanged enclosure falsely confirmed pending provenance")
	}
}

func TestRetentionRemovesChapterRevisions(t *testing.T) {
	f := newLifecycleFixture(t)
	sourceChapters(f)
	f.audio(t)
	ctx := context.Background()
	ep, err := f.runner.Run(ctx, request(Sync))
	if err != nil {
		t.Fatal(err)
	}
	fd, err := feed.Parse(f.store.objects["show/feed.xml"])
	if err != nil {
		t.Fatal(err)
	}
	fd.Upsert(feed.Item{ID: "newest00001", PublishedAt: parseUploadDate("20260922")})
	f.store.objects["show/feed.xml"], err = fd.XML()
	if err != nil {
		t.Fatal(err)
	}
	ch := request(Sync).Channel
	ch.Keep, ch.Latest = 1, 1
	if err := f.runner.EnforceRetention(ctx, ch); err != nil {
		t.Fatal(err)
	}
	for _, key := range append(ep.ChapterKeys, ep.AudioKeys...) {
		if _, ok := f.store.objects[key]; ok {
			t.Fatalf("retention retained %s", key)
		}
	}
	current, _ := f.runner.State.Episode("show", lifecycleID)
	if current.Removal != Pruned || current.DeletePending {
		t.Fatalf("bad retention state: %+v", current)
	}
}
