package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/transcript"
	"github.com/cwebley/shearcast/internal/youtube"
)

func TestRemovalRecoveryExcludesEpisodeAndKeepsSmallRecords(t *testing.T) {
	for _, phase := range []string{"feed", "audio"} {
		t.Run(phase, func(t *testing.T) {
			f := newLifecycleFixture(t)
			f.audio(t)
			ctx := context.Background()
			ch := request(Sync).Channel
			ep, err := f.runner.Run(ctx, request(Sync))
			if err != nil {
				t.Fatal(err)
			}
			if phase == "feed" {
				f.store.beforePut = func(string) error { return errors.New("offline") }
			} else {
				f.store.beforeDelete = func(string) error {
					fd, err := feed.Parse(f.store.objects["show/feed.xml"])
					if err != nil || len(fd.Items) != 0 {
						t.Fatal("media deleted before feed removal")
					}
					return errors.New("delete failed")
				}
			}
			if err := f.runner.RemoveEpisode(ctx, ch, lifecycleID); err == nil {
				t.Fatal("expected failure")
			}
			f.reopen(t)
			pending, _ := f.runner.State.Episode("show", lifecycleID)
			if pending.Removal != Excluded || !pending.DeletePending || !pending.HasPublished || pending.PublishPending {
				t.Fatalf("lost exclusion: %+v", pending)
			}
			f.store.beforePut, f.store.beforeDelete = nil, nil
			f.source.infoErr = errors.New("source unavailable")
			f.runner.NewClient = nil
			if err := f.runner.Recover(ctx, ch); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, err := f.runner.Run(ctx, request(Sync)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(ep.RenderPath); !os.IsNotExist(err) {
				t.Fatal("managed audio retained")
			}
			if _, err := os.Stat(ep.RenderPath + ".json"); err != nil {
				t.Fatal("small record removed")
			}
			if _, ok := f.store.objects["show/"+lifecycleID+".m4a"]; ok {
				t.Fatal("published audio remains")
			}
			if f.modelCalls.Load() != 1 || f.source.refreshes != 0 {
				t.Fatal("removed episode was processed")
			}
			if _, err := f.runner.Run(ctx, request(Publish)); err == nil {
				t.Fatal("publish bypassed exclusion")
			}
		})
	}
}

func TestRestoreAndReprocessPreserveIdentityAndRetryCaptions(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	ctx := context.Background()
	ch := request(Sync).Channel
	ep, err := f.runner.Run(ctx, request(Sync))
	if err != nil {
		t.Fatal(err)
	}
	url := ep.AudioURL
	if err := f.runner.RemoveEpisode(ctx, ch, lifecycleID); err != nil {
		t.Fatal(err)
	}
	f.source.cuesErr = youtube.ErrCaptionsUnavailable
	ep, err = f.runner.Run(ctx, request(Restore))
	if err != nil || ep.Stage != state.Waiting {
		t.Fatalf("restore: %+v %v", ep, err)
	}
	f.reopen(t)
	f.source.cuesErr = nil
	f.audio(t)
	ep, err = f.runner.Run(ctx, request(Sync))
	if err != nil || ep.Stage != state.Published || ep.Removal != "" || ep.AudioURL != url {
		t.Fatalf("restore retry: %+v %v", ep, err)
	}
	f.audio(t)
	req := request(Reprocess)
	req.Channel.BitrateKbps = 64
	ep, err = f.runner.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := feed.Parse(f.store.objects["show/feed.xml"])
	if err != nil {
		t.Fatal(err)
	}
	if len(fd.Items) != 1 || fd.Items[0].ID != lifecycleID || fd.Items[0].AudioURL != ep.AudioURL || ep.AudioURL == url || f.modelCalls.Load() != 3 {
		t.Fatalf("identity or processing wrong: %+v", fd)
	}
	if ep.ReprocessPending || ep.PublishPending {
		t.Fatal("publication request not completed")
	}
}

func TestFailedRetentionAfterMetadataRefreshKeepsRemovalIntent(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	ctx := context.Background()
	if _, err := f.runner.Run(ctx, request(Sync)); err != nil {
		t.Fatal(err)
	}
	fd, err := feed.Parse(f.store.objects["show/feed.xml"])
	if err != nil {
		t.Fatal(err)
	}
	fd.Upsert(feed.Item{ID: "newest00001", PublishedAt: parseUploadDate("20260922"), AudioURL: f.store.PublicURL("show/newest00001.m4a")})
	data, _ := fd.XML()
	f.store.objects["show/feed.xml"] = data
	f.store.beforeDelete = func(string) error { return errors.New("offline") }
	req := request(Sync)
	req.Channel.Latest, req.Channel.Keep = 1, 1
	if _, err := f.runner.Run(ctx, req); err == nil {
		t.Fatal("expected failed deletion")
	}
	f.reopen(t)
	ep, _ := f.runner.State.Episode("show", lifecycleID)
	if ep.Removal != Pruned || !ep.DeletePending {
		t.Fatalf("processing error overwrote removal intent: %+v", ep)
	}
	f.store.beforeDelete = nil
	if err := f.runner.Recover(ctx, req.Channel); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionUsesChronologyAndDoesNotReprocessPrunedHistory(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	ch := request(Sync).Channel
	ch.Latest, ch.Keep = 1, 1
	fd := feed.Feed{SelfURL: f.store.PublicURL("show/feed.xml")}
	fd.Upsert(feed.Item{ID: "newest00001", PublishedAt: parseUploadDate("20260922"), AudioURL: f.store.PublicURL("show/newest00001.m4a")})
	fd.Upsert(feed.Item{ID: lifecycleID, PublishedAt: parseUploadDate("20260920"), AudioURL: f.store.PublicURL("show/" + lifecycleID + ".m4a")})
	data, err := fd.XML()
	if err != nil {
		t.Fatal(err)
	}
	f.store.objects["show/feed.xml"] = data
	// Feed-only legacy items must acquire durable history when pruned.
	if err := f.runner.EnforceRetention(ctx, ch); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	ep, _ := f.runner.State.Episode("show", lifecycleID)
	if ep.Removal != Pruned || !ep.HasPublished {
		t.Fatalf("pruned history lost: %+v", ep)
	}
	req := request(Sync)
	req.Channel = ch
	for i := 0; i < 2; i++ {
		if _, err := f.runner.Run(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if f.modelCalls.Load() != 0 || f.source.infos != 0 || f.source.refreshes != 0 {
		t.Fatal("pruned history accessed source")
	}
	req.Action = Restore
	if _, err := f.runner.Run(ctx, req); err == nil {
		t.Fatal("restored episode that would be immediately pruned")
	}
	if f.modelCalls.Load() != 0 || f.source.captions != 0 {
		t.Fatal("retention admission spent tokens")
	}
	// A previously unseen old upload is pruned before captions/detection too.
	f.source.video.ID = "older000001"
	req.Target = f.source.video.ID
	req.Action = Sync
	ep, err = f.runner.Run(ctx, req)
	if err != nil || ep.Removal != Pruned {
		t.Fatalf("old upload admission: %+v %v", ep, err)
	}
	if f.modelCalls.Load() != 0 || f.source.captions != 0 {
		t.Fatal("old upload performed paid processing")
	}
}

func TestPurgeRecoversFeedOnlyAndLocalEpisodesWhileDisabled(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	f.audio(t)
	ch := request(Sync).Channel
	ep, err := f.runner.Run(ctx, request(Render))
	if err != nil {
		t.Fatal(err)
	}
	fd := feed.Feed{SelfURL: f.store.PublicURL("show/feed.xml")}
	fd.Upsert(feed.Item{ID: "legacy00001", PublishedAt: parseUploadDate("20260920"), AudioURL: f.store.PublicURL("show/legacy00001.m4a")})
	data, _ := fd.XML()
	f.store.objects["show/feed.xml"] = data
	f.store.objects["show/legacy00001.m4a"] = []byte("audio")
	f.store.beforeDelete = func(string) error { return errors.New("offline") }
	if err := f.runner.PurgeChannel(ctx, ch); err == nil {
		t.Fatal("expected failed purge")
	}
	f.reopen(t)
	f.store.beforeDelete = nil
	ch.Disabled = true
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) {
		t.Fatal("disabled channel listed uploads")
		return nil, nil
	}
	if _, err := f.runner.SyncChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if f.runner.State.PurgePending(ch.Slug) || len(f.store.objects) != 0 {
		t.Fatal("purge incomplete")
	}
	if _, err := os.Stat(ep.RenderPath); !os.IsNotExist(err) {
		t.Fatal("local render survived purge")
	}
	for _, id := range []string{lifecycleID, "legacy00001"} {
		e, _ := f.runner.State.Episode(ch.Slug, id)
		if e.Removal != Excluded || e.DeletePending {
			t.Fatalf("%s: %+v", id, e)
		}
	}
}

type librarySource struct {
	videos                     map[string]youtube.Video
	errs                       map[string]error // metadata failures by id
	infos, refreshes, captions int
}

func (s *librarySource) Info(_ context.Context, id string) (*youtube.Video, error) {
	s.infos++
	if err := s.errs[id]; err != nil {
		return nil, err
	}
	v := s.videos[id]
	return &v, nil
}
func (s *librarySource) RefreshInfo(_ context.Context, id string) (*youtube.Video, error) {
	s.refreshes++
	if err := s.errs[id]; err != nil {
		return nil, err
	}
	v := s.videos[id]
	return &v, nil
}
func (s *librarySource) Cues(context.Context, *youtube.Video) ([]transcript.Cue, error) {
	s.captions++
	return []transcript.Cue{{Start: 0, End: 3, Text: "An episode about stars and how they form."}}, nil
}

func TestBoundedSyncRefreshesThreeAndProcessesOnlyTwo(t *testing.T) {
	f := newLifecycleFixture(t)
	f.runner.RefreshMetadata = true
	ctx := context.Background()
	ch := request(Sync).Channel
	ch.URL = "https://youtube.com/@show"
	ch.Latest, ch.Keep = 5, 10
	source := &librarySource{videos: map[string]youtube.Video{}}
	f.runner.Source = source
	fd := feed.Feed{SelfURL: f.store.PublicURL("show/feed.xml")}
	var uploads []youtube.Video
	f.audio(t)
	audio, err := os.ReadFile(f.runner.Cache.SourceAudioPath(lifecycleID))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("video%06d", i)
		v := youtube.Video{ID: id, Title: id, Duration: 3, UploadDate: fmt.Sprintf("202609%02d", 23-i)}
		source.videos[id] = v
		uploads = append(uploads, v)
		if i >= 2 && i < 5 {
			fd.Upsert(feed.Item{ID: id, PublishedAt: parseUploadDate(v.UploadDate), AudioURL: f.store.PublicURL("show/" + id + ".m4a")})
			if err := f.runner.State.MarkProcessed(ch.Slug, id); err != nil {
				t.Fatal(err)
			}
		}
		if i < 2 {
			path := f.runner.Cache.SourceAudioPath(id)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, audio, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	data, _ := fd.XML()
	f.store.objects["show/feed.xml"] = data
	f.runner.ListUploads = func(_ context.Context, _ string, limit int) ([]youtube.Video, error) {
		if limit != 5 {
			t.Fatalf("unbounded selection: %d", limit)
		}
		return uploads, nil
	}
	for i := 0; i < 2; i++ {
		outcomes, err := f.runner.SyncChannel(ctx, ch)
		if err != nil || len(outcomes) != 5 {
			t.Fatalf("sync: %v %v", outcomes, err)
		}
		f.reopen(t)
	}
	if f.modelCalls.Load() != 2 || source.captions != 2 || source.refreshes != 8 || f.store.audioPuts != 2 {
		t.Fatalf("unexpected work: models=%d captions=%d refreshes=%d uploads=%d", f.modelCalls.Load(), source.captions, source.refreshes, f.store.audioPuts)
	}
	if len(f.runner.State.Episodes(ch.Slug)) != 5 {
		t.Fatal("processed outside selection")
	}
}

func TestPlanIsReadOnlyAndUsesHistoryForCostAndBitrateForStorage(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	f.audio(t)
	ch := request(Sync).Channel
	ch.URL = "https://youtube.com/@show"
	if _, err := f.runner.Run(ctx, request(Sync)); err != nil {
		t.Fatal(err)
	}
	f.source.video.ID = "newvideo001"
	f.source.video.Duration = 3600
	f.source.video.UploadDate = "20260923"
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) {
		return []youtube.Video{f.source.video}, nil
	}
	before, err := os.ReadFile(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	objects := map[string][]byte{}
	for key, data := range f.store.objects {
		objects[key] = append([]byte(nil), data...)
	}
	ch.BitrateKbps = 64
	p, err := f.runner.Plan(ctx, ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Episodes) != 1 || p.Episodes[0].Status != "new" || !p.CostKnown || p.HistorySamples != 1 || p.ModelCostHigh <= p.ModelCostLow || p.UploadBytes != 29664000 {
		t.Fatalf("bad plan: %+v", p)
	}
	ch.BitrateKbps = 128
	p2, err := f.runner.Plan(ctx, ch)
	if err != nil {
		t.Fatal(err)
	}
	if p2.UploadBytes != 2*p.UploadBytes {
		t.Fatal("storage ignores bitrate")
	}
	after, _ := os.ReadFile(f.statePath)
	if string(before) != string(after) || !reflect.DeepEqual(objects, f.store.objects) || f.modelCalls.Load() != 1 || f.source.captions != 1 {
		t.Fatal("dry run mutated state/media or ran processing")
	}
	// A different rule set has no comparable token history.
	f.runner.Config = config.Default()
	ch.Rules = []string{"sponsor"}
	p, err = f.runner.Plan(ctx, ch)
	if err != nil || p.CostKnown || p.HistorySamples != 0 {
		t.Fatalf("invented estimate: %+v %v", p, err)
	}
}

func TestMissingPublicationDateRefreshesOnRetry(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	f.source.video.UploadDate = ""
	if _, err := f.runner.Run(ctx, request(Sync)); err == nil {
		t.Fatal("accepted missing date")
	}
	if f.modelCalls.Load() != 0 || f.source.captions != 0 {
		t.Fatal("missing metadata spent tokens")
	}
	f.reopen(t)
	f.source.video.UploadDate = "20260920"
	f.audio(t)
	ep, err := f.runner.Run(ctx, request(Sync))
	if err != nil || ep.Stage != state.Published {
		t.Fatalf("retry: %+v %v", ep, err)
	}
	if f.source.refreshes != 1 {
		t.Fatal("retry did not refresh incomplete metadata")
	}
}

func TestSameDaySelectionRetainsNewestUploadRatherThanLowestID(t *testing.T) {
	for _, precise := range []bool{false, true} {
		t.Run(fmt.Sprint(precise), func(t *testing.T) {
			f := newLifecycleFixture(t)
			ctx := context.Background()
			f.audio(t)
			ch := request(Sync).Channel
			ch.URL = "https://youtube.com/@show"
			ch.Latest, ch.Keep = 1, 1
			fd := feed.Feed{SelfURL: f.store.PublicURL("show/feed.xml")}
			older := parseUploadDate("20260920")
			if precise {
				older = older.Add(3600 * 1e9)
				f.source.video.Timestamp = older.Add(3600 * 1e9).Unix()
			}
			fd.Upsert(feed.Item{ID: "aaaaaaa0001", PublishedAt: older, AudioURL: f.store.PublicURL("show/aaaaaaa0001.m4a")})
			data, _ := fd.XML()
			f.store.objects["show/feed.xml"] = data
			f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) {
				return []youtube.Video{f.source.video}, nil
			}
			if _, err := f.runner.SyncChannel(ctx, ch); err != nil {
				t.Fatal(err)
			}
			parsed, err := feed.Parse(f.store.objects["show/feed.xml"])
			if err != nil {
				t.Fatal(err)
			}
			if len(parsed.Items) != 1 || parsed.Items[0].ID != lifecycleID {
				t.Fatalf("newest selected upload lost: %+v", parsed)
			}
			f.reopen(t)
			if _, err := f.runner.SyncChannel(ctx, ch); err != nil {
				t.Fatal(err)
			}
			if f.modelCalls.Load() != 1 {
				t.Fatal("repeat sync reprocessed same-day upload")
			}
		})
	}
}

func TestPlanCountsRetryUploadEvenWhenLaterPruned(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	f.audio(t)
	ch := request(Sync).Channel
	ch.URL = "https://youtube.com/@show"
	ch.Latest, ch.Keep = 1, 1
	f.store.beforePut = func(string) error { return errors.New("offline") }
	ep, err := f.runner.Run(ctx, request(Sync))
	if err == nil || ep.Stage != state.Rendered {
		t.Fatal("expected publication failure")
	}
	f.store.beforePut = nil
	newVideo := youtube.Video{ID: "newvideo001", Duration: 3600, UploadDate: "20260923"}
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) { return []youtube.Video{newVideo}, nil }
	p, err := f.runner.Plan(ctx, ch)
	if err != nil {
		t.Fatal(err)
	}
	var retry *PlannedEpisode
	for i := range p.Episodes {
		if p.Episodes[i].ID == lifecycleID {
			retry = &p.Episodes[i]
		}
	}
	if retry == nil || retry.Status != "ready_to_publish_then_pruned" || retry.EstimatedBytes != ep.AudioBytes || p.UploadBytes != p.RetainedBytes+ep.AudioBytes {
		t.Fatalf("retry upload missing: %+v, episodes %+v", p, p.Episodes)
	}
}

func TestRestoreRefreshesIncompleteMetadataEvenWhileExcluded(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	ch := request(Sync).Channel
	if err := f.runner.RemoveEpisode(ctx, ch, lifecycleID); err != nil {
		t.Fatal(err)
	}
	f.source.video.UploadDate = ""
	if _, err := f.runner.Run(ctx, request(Restore)); err == nil {
		t.Fatal("accepted undated restoration")
	}
	f.reopen(t)
	f.source.video.UploadDate = "20260920"
	f.audio(t)
	ep, err := f.runner.Run(ctx, request(Restore))
	if err != nil || ep.Stage != state.Published {
		t.Fatalf("restore remained stuck: %+v %v", ep, err)
	}
	if f.source.refreshes != 2 || f.source.infos != 0 {
		t.Fatal("restore reused stale cached metadata")
	}
}

func TestDateOnlyPublicationRetryUsesListingAfterDeferredAdmission(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	f.audio(t)
	ch := request(Sync).Channel
	ch.URL = "https://youtube.com/@show"
	ch.Latest, ch.Keep = 1, 1
	ep, err := f.runner.Run(ctx, request(Render))
	if err != nil {
		t.Fatal(err)
	}
	ep.PublishPending = true
	if err := f.runner.State.Save(ch.Slug, lifecycleID, ep); err != nil {
		t.Fatal(err)
	}
	fd := feed.Feed{SelfURL: f.store.PublicURL("show/feed.xml")}
	fd.Upsert(feed.Item{ID: "aaaaaaa0001", PublishedAt: parseUploadDate("20260920"), AudioURL: f.store.PublicURL("show/aaaaaaa0001.m4a")})
	data, _ := fd.XML()
	f.store.objects["show/feed.xml"] = data
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) {
		return []youtube.Video{f.source.video}, nil
	}
	f.source.infoErr = errors.New("offline")
	f.runner.NewClient = nil
	p, err := f.runner.Plan(ctx, ch)
	if err != nil || p.UploadBytes != ep.AudioBytes {
		t.Fatalf("plan missed deferred retry: %+v %v", p, err)
	}
	if _, err := f.runner.SyncChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if f.store.audioPuts != 1 || f.modelCalls.Load() != 1 {
		t.Fatal("retry did not publish exactly once without detection")
	}
	got, err := feed.Parse(f.store.objects["show/feed.xml"])
	if err != nil || len(got.Items) != 1 || got.Items[0].ID != lifecycleID {
		t.Fatalf("wrong retention: %+v %v", got, err)
	}
}

func TestOverfullSameDayFeedResolvesChronologyBeforePruning(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	ch := request(Sync).Channel
	ch.URL = "https://youtube.com/@show"
	ch.Latest, ch.Keep = 1, 1
	// Represents either an old manually ordered feed after lowering keep, or
	// interrupted publication before its retention write completed.
	fd := feed.Feed{SelfURL: f.store.PublicURL("show/feed.xml")}
	for _, id := range []string{"aaaaaaa0001", lifecycleID} {
		fd.Upsert(feed.Item{ID: id, PublishedAt: parseUploadDate("20260920"), AudioURL: f.store.PublicURL("show/" + id + ".m4a")})
		if err := f.runner.State.MarkProcessed(ch.Slug, id); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := fd.XML()
	f.store.objects["show/feed.xml"] = data
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) {
		return []youtube.Video{f.source.video}, nil
	}
	p, err := f.runner.Plan(ctx, ch)
	if err != nil || !reflect.DeepEqual(p.PruneIDs, []string{"aaaaaaa0001"}) {
		t.Fatalf("plan guessed chronology: %+v %v", p, err)
	}
	if _, err := f.runner.SyncChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	got, err := feed.Parse(f.store.objects["show/feed.xml"])
	if err != nil || len(got.Items) != 1 || got.Items[0].ID != lifecycleID {
		t.Fatalf("newer episode pruned: %+v %v", got, err)
	}
	if f.modelCalls.Load() != 0 {
		t.Fatal("metadata and retention used model")
	}
}

func TestPlanPreservesTimestampOfRetryOutsideSelectedWindow(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	f.audio(t)
	ch := request(Sync).Channel
	ch.URL = "https://youtube.com/@show"
	ch.Latest, ch.Keep = 1, 1
	f.source.video.Timestamp = parseUploadDate("20260920").Add(2 * 3600 * 1e9).Unix()
	ep, err := f.runner.Run(ctx, request(Render))
	if err != nil {
		t.Fatal(err)
	}
	ep.PublishPending = true
	if err := f.runner.State.Save(ch.Slug, lifecycleID, ep); err != nil {
		t.Fatal(err)
	}
	fd := feed.Feed{SelfURL: f.store.PublicURL("show/feed.xml")}
	fd.Upsert(feed.Item{ID: "aaaaaaa0001", PublishedAt: parseUploadDate("20260920").Add(3600 * 1e9), AudioURL: f.store.PublicURL("show/aaaaaaa0001.m4a")})
	data, _ := fd.XML()
	f.store.objects["show/feed.xml"] = data
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) { return nil, nil }
	f.source.infoErr = errors.New("offline")
	p, err := f.runner.Plan(ctx, ch)
	if err != nil || p.UploadBytes != ep.AudioBytes || len(p.Episodes) != 1 || p.Episodes[0].Status != "ready_to_publish" {
		t.Fatalf("timestamp lost: %+v %v", p, err)
	}
}

func TestPlanShowsBrokenRetryOutsideSelectedWindow(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	f.audio(t)
	ch := request(Sync).Channel
	ch.URL = "https://youtube.com/@show"
	ep, err := f.runner.Run(ctx, request(Render))
	if err != nil {
		t.Fatal(err)
	}
	ep.PublishPending = true
	if err := f.runner.State.Save(ch.Slug, lifecycleID, ep); err != nil {
		t.Fatal(err)
	}
	// Sync's recovery fails on this artifact; the plan must say so.
	if err := os.WriteFile(ep.RenderPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) { return nil, nil }
	p, err := f.runner.Plan(ctx, ch)
	if err != nil || len(p.Episodes) != 1 || p.Episodes[0].Status != "blocked_artifact" {
		t.Fatalf("broken retry hidden: %+v %v", p, err)
	}
}

func TestCompletedRetryAwaitingRetentionDoesNotRefreshUnavailableSource(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	f.audio(t)
	ch := request(Sync).Channel
	ch.URL = "https://youtube.com/@show"
	ch.Latest, ch.Keep = 2, 3
	audio, err := os.ReadFile(f.runner.Cache.SourceAudioPath(lifecycleID))
	if err != nil {
		t.Fatal(err)
	}
	videos := []youtube.Video{
		{ID: lifecycleID, Duration: 3, UploadDate: "20260921"},
		{ID: "selected001", Duration: 3, UploadDate: "20260922"},
		{ID: "selected002", Duration: 3, UploadDate: "20260923"},
	}
	for i, v := range videos {
		f.source.video = v
		if i > 0 {
			path := f.runner.Cache.SourceAudioPath(v.ID)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, audio, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		req := request(Render)
		req.Target = v.ID
		ep, err := f.runner.Run(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			ep.PublishPending = true
			if err := f.runner.State.Save(ch.Slug, v.ID, ep); err != nil {
				t.Fatal(err)
			}
		}
	}
	fd := feed.Feed{SelfURL: f.store.PublicURL("show/feed.xml")}
	for _, id := range []string{"oldvideo001", "oldvideo002", "oldvideo003"} {
		fd.Upsert(feed.Item{ID: id, PublishedAt: parseUploadDate("20260920"), AudioURL: f.store.PublicURL("show/" + id + ".m4a")})
	}
	data, _ := fd.XML()
	f.store.objects["show/feed.xml"] = data
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) {
		return []youtube.Video{videos[2], videos[1]}, nil
	}
	f.source.infoErr = errors.New("source unavailable")
	f.runner.NewClient = nil
	if _, err := f.runner.SyncChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if f.source.refreshes != 0 || f.modelCalls.Load() != 3 || f.store.audioPuts != 3 {
		t.Fatalf("completed retry did extra work: refreshes=%d models=%d uploads=%d", f.source.refreshes, f.modelCalls.Load(), f.store.audioPuts)
	}
	ep, _ := f.runner.State.Episode(ch.Slug, lifecycleID)
	if ep.Removal != "" || !ep.HasPublished || ep.LastError != "" {
		t.Fatalf("retry did not complete with a clean retained checkpoint: %+v", ep)
	}
}
