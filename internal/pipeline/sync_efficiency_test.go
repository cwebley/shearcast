package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/youtube"
)

type countingPublisher struct {
	*memoryPublisher
	gets, puts int
}

func (p *countingPublisher) Get(ctx context.Context, key string) ([]byte, error) {
	p.gets++
	return p.memoryPublisher.Get(ctx, key)
}

func (p *countingPublisher) Put(ctx context.Context, key string, data io.Reader, size int64, kind string) (string, error) {
	p.puts++
	return p.memoryPublisher.Put(ctx, key, data, size, kind)
}

func unchangedSyncFixture(t *testing.T) (*Runner, config.Channel, *testSource, *countingPublisher) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ch := config.Channel{Slug: "show", Name: "Show", URL: "https://youtube.com/@show"}
	v := youtube.Video{ID: lifecycleID, Title: "Episode", UploadDate: "20260923", Duration: 3600, Thumbnail: "https://example.test/episode.jpg"}
	pub := &countingPublisher{memoryPublisher: &memoryPublisher{objects: map[string][]byte{}}}
	key := "show/" + lifecycleID + ".0000000000000000000000000000000000000000000000000000000000000000.m4a"
	fd := feed.Feed{Title: ch.Name, Description: ch.Name, SelfURL: pub.PublicURL("show/feed.xml"), ImageURL: "https://example.test/show.jpg", Items: []feed.Item{{
		ID: v.ID, Title: v.Title, ImageURL: v.Thumbnail, AudioURL: pub.PublicURL(key), PublishedAt: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC), Duration: time.Hour,
	}}}
	data, err := fd.XML()
	if err != nil {
		t.Fatal(err)
	}
	pub.objects["show/feed.xml"] = data
	if err := st.Save(ch.Slug, v.ID, state.Episode{Stage: state.Published, HasPublished: true, Video: &v, AudioKeys: []string{key}}); err != nil {
		t.Fatal(err)
	}
	source := &testSource{video: v}
	r := &Runner{State: st, Cache: youtube.Cache{Dir: filepath.Join(dir, "cache")}, Publisher: pub, Source: source,
		ListUploads:    func(context.Context, string, int) ([]youtube.Video, error) { return []youtube.Video{v}, nil },
		ChannelArtwork: func(context.Context, string) (string, error) { t.Error("unexpected artwork lookup"); return "", nil },
	}
	return r, ch, source, pub
}

func TestSyncChannelLeavesUnchangedPublishedMetadataAlone(t *testing.T) {
	r, ch, source, pub := unchangedSyncFixture(t)
	before := string(pub.objects["show/feed.xml"])
	outcomes, err := r.SyncChannel(context.Background(), ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || !outcomes[0].Episode.HasPublished {
		t.Fatalf("outcomes: %+v", outcomes)
	}
	if source.infos != 0 || source.refreshes != 0 || source.captions != 0 {
		t.Fatalf("unchanged published episode fetched source: %+v", source)
	}
	if pub.puts != 0 || string(pub.objects["show/feed.xml"]) != before {
		t.Fatal("unchanged sync rewrote publication")
	}
}

func TestSyncChannelReadsFeedOnceForWholePublishedWindow(t *testing.T) {
	r, ch, _, pub := unchangedSyncFixture(t)
	ch.Latest, ch.Keep = 6, 6
	fd, err := feed.Parse(pub.objects["show/feed.xml"])
	if err != nil {
		t.Fatal(err)
	}
	ep, _ := r.State.Episode(ch.Slug, lifecycleID)
	uploads := []youtube.Video{{ID: lifecycleID, Title: "Episode"}}
	for i := 1; i < 6; i++ {
		id := fmt.Sprintf("video%06d", i)
		item := fd.Items[0]
		item.ID = id
		key := ch.Slug + "/" + id + ".0000000000000000000000000000000000000000000000000000000000000000.m4a"
		item.AudioURL = pub.PublicURL(key)
		fd.Items = append(fd.Items, item)
		copy := ep
		copy.AudioKeys = []string{key}
		if err := r.State.Save(ch.Slug, id, copy); err != nil {
			t.Fatal(err)
		}
		uploads = append(uploads, youtube.Video{ID: id, Title: "Episode"})
	}
	fd.SelfURL = pub.PublicURL(ch.Slug + "/feed.xml")
	pub.objects["show/feed.xml"], err = fd.XML()
	if err != nil {
		t.Fatal(err)
	}
	r.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) { return uploads, nil }
	for attempt := 0; attempt < 2; attempt++ {
		pub.gets, pub.puts = 0, 0
		outcomes, err := r.SyncChannel(context.Background(), ch)
		if err != nil {
			t.Fatal(err)
		}
		if len(outcomes) != 6 || pub.gets != 1 || pub.puts != 0 {
			t.Fatalf("unchanged window: %d outcomes, %d feed reads, %d writes", len(outcomes), pub.gets, pub.puts)
		}
	}
}

func TestSyncChannelExplicitRefreshUpdatesMetadataWithoutReplacingAudio(t *testing.T) {
	r, ch, source, pub := unchangedSyncFixture(t)
	r.RefreshMetadata, r.ChannelArtwork = true, nil
	source.video.Title = "Updated title"
	before, _ := feed.Parse(pub.objects["show/feed.xml"])
	if _, err := r.SyncChannel(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	after, _ := feed.Parse(pub.objects["show/feed.xml"])
	if source.refreshes != 1 || after.Items[0].Title != "Updated title" || after.Items[0].AudioURL != before.Items[0].AudioURL || after.Items[0].Duration != before.Items[0].Duration {
		t.Fatalf("refresh changed publication incorrectly: %+v", after.Items[0])
	}
	pub.puts = 0
	if _, err := r.SyncChannel(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	if source.refreshes != 2 || pub.puts != 0 {
		t.Fatalf("identical forced refresh rewrote feed: %d refreshes, %d writes", source.refreshes, pub.puts)
	}
}

func TestSyncChannelChangedListingTitleRefreshesMetadata(t *testing.T) {
	r, ch, source, pub := unchangedSyncFixture(t)
	source.video.Title = "Corrected episode title"
	r.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) { return []youtube.Video{source.video}, nil }
	outcomes, err := r.SyncChannel(context.Background(), ch)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := feed.Parse(pub.objects["show/feed.xml"])
	if err != nil {
		t.Fatal(err)
	}
	if source.refreshes != 1 || !outcomes[0].Refreshed || fd.Items[0].Title != source.video.Title {
		t.Fatalf("changed title did not refresh metadata: %+v", outcomes)
	}
}

func TestSyncChannelAppliesLocalFeedSettingsWithoutRefreshingSource(t *testing.T) {
	r, ch, source, pub := unchangedSyncFixture(t)
	ch.Title, ch.Description = "Renamed show", "New description"
	if _, err := r.SyncChannel(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	fd, err := feed.Parse(pub.objects["show/feed.xml"])
	if err != nil {
		t.Fatal(err)
	}
	if fd.Title != ch.Title || fd.Description != ch.Description || source.refreshes != 0 {
		t.Fatalf("local feed settings not applied: %+v", fd)
	}
}

func TestSyncChannelRefreshReusesAlreadyPublishedChapterRevision(t *testing.T) {
	f := newLifecycleFixture(t)
	sourceChapters(f)
	f.audio(t)
	ctx := context.Background()
	if _, err := f.runner.Run(ctx, request(Sync)); err != nil {
		t.Fatal(err)
	}
	pub := &countingPublisher{memoryPublisher: f.store}
	f.runner.Publisher, f.runner.RefreshMetadata = pub, true
	ch := request(Sync).Channel
	ch.URL = "https://youtube.com/@show"
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) {
		return []youtube.Video{f.source.video}, nil
	}
	if _, err := f.runner.SyncChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if pub.puts != 0 || pub.gets != 1 {
		t.Fatalf("identical chapter refresh made %d writes and %d reads", pub.puts, pub.gets)
	}
}

func TestSyncChannelReusesLegacyCachedAudioInIsolatedWorkspace(t *testing.T) {
	f := newLifecycleFixture(t)
	f.audio(t)
	legacy := f.runner.Cache.SourceAudioPath(lifecycleID)
	f.runner.Cache.AudioDir = filepath.Join(f.runner.Cache.Dir, "sources", "show")
	// Reusing an older cached download must work without contacting YouTube.
	old := youtube.Binary
	binary, err := exec.LookPath("false")
	if err != nil {
		t.Fatal(err)
	}
	youtube.Binary = binary
	t.Cleanup(func() { youtube.Binary = old })
	ch := request(Sync).Channel
	ch.URL = "https://youtube.com/@show"
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) {
		return []youtube.Video{f.source.video}, nil
	}
	outcomes, err := f.runner.SyncChannel(context.Background(), ch)
	if err != nil || len(outcomes) != 1 || outcomes[0].Episode.Stage != state.Published {
		t.Fatalf("legacy cached source: %+v %v", outcomes, err)
	}
	for _, path := range []string{legacy, f.runner.Cache.SourceAudioPath(lifecycleID)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("source survived cleanup at %s: %v", path, err)
		}
	}
}
