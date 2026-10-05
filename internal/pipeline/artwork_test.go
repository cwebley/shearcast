package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/youtube"
)

func TestSyncReplacesEpisodeArtworkWithTheChannelsOwn(t *testing.T) {
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// A feed from before channel artwork: its show image is an old episode's.
	old := &feed.Feed{Title: "Show", ImageURL: "https://i.ytimg.com/old-episode.jpg",
		Items: []feed.Item{{ID: lifecycleID, Title: "Ep", AudioURL: "https://x/a.m4a", ImageURL: "https://i.ytimg.com/old-episode.jpg"}}}
	data, err := old.XML()
	if err != nil {
		t.Fatal(err)
	}
	pub := &memoryPublisher{objects: map[string][]byte{"show/feed.xml": data}}
	r := Runner{State: st, Cache: youtube.Cache{Dir: filepath.Join(dir, "cache")}, Publisher: pub,
		ListUploads: func(context.Context, string, int) ([]youtube.Video, error) { return nil, nil },
		ChannelArtwork: func(_ context.Context, url string) (string, error) {
			return "https://yt3.example/avatar=s0", nil
		},
	}
	ch := config.Channel{Slug: "show", URL: "https://youtube.com/@show"}
	read := func() *feed.Feed {
		t.Helper()
		got, err := feed.Parse(pub.objects["show/feed.xml"])
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if _, err := r.SyncChannel(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	got := read()
	if got.ImageURL != "https://yt3.example/avatar=s0" {
		t.Fatalf("show artwork: %q", got.ImageURL)
	}
	if len(got.Items) != 1 || got.Items[0].ImageURL != "https://i.ytimg.com/old-episode.jpg" {
		t.Fatalf("episode artwork must be untouched: %+v", got.Items)
	}

	// Config wins over the avatar.
	ch.Image = "https://example.com/mine.png"
	if _, err := r.SyncChannel(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	if got := read().ImageURL; got != ch.Image {
		t.Fatalf("configured artwork: %q", got)
	}

	// A failed lookup keeps what the feed has.
	ch.Image = ""
	r.ChannelArtwork = func(context.Context, string) (string, error) { return "", errors.New("offline") }
	if _, err := r.SyncChannel(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	if got := read().ImageURL; got != "https://example.com/mine.png" {
		t.Fatalf("failed lookup changed artwork: %q", got)
	}
}
