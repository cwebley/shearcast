package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/youtube"
)

func TestSyncSourceHelper(t *testing.T) {
	if os.Getenv("SHEARCAST_SYNC_HELPER") != "1" {
		return
	}
	args := os.Args
	url := args[len(args)-1]
	slug := "a"
	if strings.Contains(url, "@b") || strings.Contains(url, "bbbbbbbbbbb") {
		slug = "b"
	}
	id := strings.Repeat(slug, 11)
	if slug == "a" && os.Getenv("SHEARCAST_SYNC_HELPER_WAITING") == "1" {
		id = "ccccccccccc"
	}
	dir := os.Getenv("SHEARCAST_SYNC_HELPER_DIR")
	if !strings.Contains(strings.Join(args, " "), "--flat-playlist") {
		if os.Getenv("SHEARCAST_SYNC_HELPER_METADATA") == "1" {
			os.WriteFile(filepath.Join(dir, "metadata-"+slug), nil, 0600)
			json.NewEncoder(os.Stdout).Encode(youtube.Video{ID: id, Title: "Updated episode", UploadDate: "20260923", Duration: 3600})
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "unexpected full metadata request")
		os.Exit(3)
	}
	if strings.Contains(strings.Join(args, " "), "--playlist-items 0") {
		fmt.Fprintln(os.Stdout, `{"thumbnails":[{"id":"avatar_uncropped","url":"http://example.test/show.jpg"}]}`)
		os.Exit(0)
	}
	if err := os.WriteFile(filepath.Join(dir, "entered-"+slug), nil, 0600); err != nil {
		os.Exit(4)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, a := os.Stat(filepath.Join(dir, "entered-a"))
		_, b := os.Stat(filepath.Join(dir, "entered-b"))
		if a == nil && b == nil {
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "other channel never started before this channel finished")
			os.Exit(5)
		}
		time.Sleep(5 * time.Millisecond)
	}
	json.NewEncoder(os.Stdout).Encode(youtube.Video{ID: id, Title: "Episode"})
	os.Exit(0)
}

type syncFixture struct {
	dir, configPath, statePath, cacheDir, publicDir string
}

func (f syncFixture) args() []string {
	return []string{"-config", f.configPath, "-state", f.statePath, "-cache", f.cacheDir}
}

func concurrentSyncFixture(t *testing.T) syncFixture {
	t.Helper()
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHEARCAST_SYNC_HELPER", "1")
	t.Setenv("SHEARCAST_SYNC_HELPER_DIR", dir)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(script, []byte(fmt.Sprintf("#!/bin/sh\nexec %q -test.run='^TestSyncSourceHelper$' -- \"$@\"\n", binary)), 0700); err != nil {
		t.Fatal(err)
	}
	old := youtube.Binary
	youtube.Binary = script
	t.Cleanup(func() { youtube.Binary = old })
	public := filepath.Join(dir, "public")
	configPath := filepath.Join(dir, "config.toml")
	configText := fmt.Sprintf("[publishing]\nbackend = 'filesystem'\ndirectory = %q\nbase_url = 'http://example.test'\n", public)
	path := filepath.Join(dir, "state.json")
	st, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.BindPublishing(state.PublishingDestination{Backend: "filesystem", Location: public, BaseURL: "http://example.test"}, false); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"a", "b"} {
		configText += fmt.Sprintf("\n[[channels]]\nslug = %q\nname = %q\nurl = 'https://youtube.com/@%s'\n", slug, slug, slug)
		id := strings.Repeat(slug, 11)
		v := youtube.Video{ID: id, Title: "Episode", UploadDate: "20260923", Duration: 3600}
		if err := st.Save(slug, id, state.Episode{Stage: state.Published, HasPublished: true, Video: &v}); err != nil {
			t.Fatal(err)
		}
		fd := feed.Feed{Title: slug, Description: slug, SelfURL: "http://example.test/" + slug + "/feed.xml", ImageURL: "http://example.test/show.jpg", Items: []feed.Item{{ID: id, Title: v.Title, AudioURL: "http://example.test/" + slug + "/" + id + ".m4a", PublishedAt: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC), Duration: time.Hour}}}
		data, err := fd.XML()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(public, slug), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(public, slug, "feed.xml"), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	return syncFixture{dir: dir, configPath: configPath, statePath: path, cacheDir: filepath.Join(dir, "cache"), publicDir: public}
}

func syncOutput(t *testing.T, ctx context.Context, args []string) (string, error) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = file
	defer func() { os.Stderr = old; file.Close() }()
	runErr := runSync(ctx, args)
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data), runErr
}

func TestSyncRunsIndependentChannelsConcurrently(t *testing.T) {
	fixture := concurrentSyncFixture(t)
	args := fixture.args()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := syncOutput(t, ctx, args)
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, output)
	}
	for _, want := range []string{"a: complete in", "b: complete in", "1 unchanged", "sync: complete in"} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q:\n%s", want, output)
		}
	}
}

func TestSyncCancellationStopsQueuedYouTubeWork(t *testing.T) {
	fixture := concurrentSyncFixture(t)
	args := fixture.args()
	args = append(args, "-youtube-jobs", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			_, a := os.Stat(filepath.Join(os.Getenv("SHEARCAST_SYNC_HELPER_DIR"), "entered-a"))
			_, b := os.Stat(filepath.Join(os.Getenv("SHEARCAST_SYNC_HELPER_DIR"), "entered-b"))
			if a == nil || b == nil {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	output, err := syncOutput(t, ctx, args)
	<-finished
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v\n%s", err, output)
	}
	_, a := os.Stat(filepath.Join(os.Getenv("SHEARCAST_SYNC_HELPER_DIR"), "entered-a"))
	_, b := os.Stat(filepath.Join(os.Getenv("SHEARCAST_SYNC_HELPER_DIR"), "entered-b"))
	if (a == nil) == (b == nil) {
		t.Fatalf("expected exactly one source process to start: a=%v b=%v", a, b)
	}
	if strings.Contains(output, "subscription page:") {
		t.Fatal("canceled invocation published subscriptions")
	}
}

func TestSyncExplicitMetadataRefresh(t *testing.T) {
	fixture := concurrentSyncFixture(t)
	args := fixture.args()
	t.Setenv("SHEARCAST_SYNC_HELPER_METADATA", "1")
	output, err := syncOutput(t, context.Background(), append(args, "-refresh-metadata"))
	if err != nil {
		t.Fatalf("refresh: %v\n%s", err, output)
	}
	for _, slug := range []string{"a", "b"} {
		data, err := os.ReadFile(filepath.Join(fixture.publicDir, slug, "feed.xml"))
		if err != nil {
			t.Fatal(err)
		}
		fd, err := feed.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		if fd.Items[0].Title != "Updated episode" {
			t.Fatalf("%s metadata was not refreshed", slug)
		}
	}
	if !strings.Contains(output, "metadata refreshed") || !strings.Contains(output, "0 requests; no new model work") {
		t.Fatalf("refresh output:\n%s", output)
	}
}

func TestNormalSyncStillRetriesWaitingCaptions(t *testing.T) {
	fixture := concurrentSyncFixture(t)
	args := fixture.args()
	t.Setenv("SHEARCAST_SYNC_HELPER_METADATA", "1")
	t.Setenv("SHEARCAST_SYNC_HELPER_WAITING", "1")
	st, err := state.Open(fixture.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save("a", "ccccccccccc", state.Episode{Stage: state.Waiting}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := syncOutput(t, context.Background(), args)
	if err != nil {
		t.Fatalf("waiting retry: %v\n%s", err, output)
	}
	snapshot, err := state.ReadSnapshot(fixture.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Episodes["a"]["ccccccccccc"].Stage != state.Waiting || !strings.Contains(output, "fetching captions") || !strings.Contains(output, "1 waiting") {
		t.Fatalf("waiting episode was not retried:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(fixture.dir, "metadata-b")); !os.IsNotExist(err) {
		t.Fatalf("published episode fetched metadata: %v", err)
	}
}
