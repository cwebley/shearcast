package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/render"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/storage"
	"github.com/cwebley/shearcast/internal/worklimit"
	"github.com/cwebley/shearcast/internal/youtube"
)

func TestConcurrentChannelSyncSharesModelLimitAndDurableUsage(t *testing.T) {
	f := newLifecycleFixture(t)
	dir := t.TempDir()
	f.runner.Cache.AudioDir = filepath.Join(dir, "sources", "show")
	f.audio(t)
	audio, err := os.ReadFile(f.runner.Cache.SourceAudioPath(lifecycleID))
	if err != nil {
		t.Fatal(err)
	}
	// The encoder wrapper refuses overlapping invocations. Both channels select
	// the same video, so this also exercises isolated audio working directories.
	ffmpeg, err := exec.LookPath(render.Binary)
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "ffmpeg")
	script := fmt.Sprintf("#!/bin/sh\nmkdir %q || exit 42\nsleep 0.15\n%q \"$@\"\nstatus=$?\nrmdir %q\nexit $status\n", filepath.Join(dir, "encoding"), ffmpeg, filepath.Join(dir, "encoding"))
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	oldBinary := render.Binary
	render.Binary = wrapper
	t.Cleanup(func() { render.Binary = oldBinary })
	other := *f.runner
	other.Cache.AudioDir = filepath.Join(dir, "sources", "other")
	other.Source = &testSource{video: f.source.video}
	path := other.Cache.SourceAudioPath(lifecycleID)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, audio, 0600); err != nil {
		t.Fatal(err)
	}
	pub, err := storage.NewFilesystem(filepath.Join(dir, "public"), "http://example.test")
	if err != nil {
		t.Fatal(err)
	}
	f.runner.Publisher = NewSyncPublisher(pub)
	other.Publisher = f.runner.Publisher
	var active, maximum, calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, n) {
				break
			}
		}
		calls.Add(1)
		var req jev.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		time.Sleep(50 * time.Millisecond)
		answers := map[string]jev.Answer{}
		for id := range req.Questions {
			answers[id] = jev.Answer{Type: "choice", Choice: "content", Probabilities: map[string]float64{"content": 1, "sponsor": 0, "selfpromo": 0, "credits": 0}}
		}
		json.NewEncoder(w).Encode(jev.Response{Answers: answers, Usage: jev.Usage{InputTokens: 100}})
	}))
	defer srv.Close()
	newClient := func() (*jev.Client, error) {
		return jev.New(jev.Config{BaseURL: srv.URL, Model: "typesafe/jev-1.13"}), nil
	}
	f.runner.NewClient, other.NewClient = newClient, newClient
	list := func(context.Context, string, int) ([]youtube.Video, error) {
		return []youtube.Video{f.source.video}, nil
	}
	f.runner.ListUploads, other.ListUploads = list, list
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx, stop := worklimit.With(ctx, 2, 1, 1)
	defer stop()
	// Hold the model slot until both channels actually reach detection. No HTTP
	// request should be dispatched while this invocation-wide slot is occupied.
	release, err := worklimit.Acquire(ctx, worklimit.Model)
	if err != nil {
		t.Fatal(err)
	}
	detecting := make(chan struct{}, 2)
	progress := func(msg string) {
		if strings.Contains(msg, "running detection passes") && strings.HasSuffix(msg, "...") {
			detecting <- struct{}{}
		}
	}
	f.runner.Progress, other.Progress = progress, progress
	if err := f.runner.State.StartSync([]string{"show", "other"}); err != nil {
		t.Fatal(err)
	}
	g, ctx := errgroup.WithContext(ctx)
	for _, r := range []*Runner{f.runner, &other} {
		ch := request(Sync).Channel
		if r == &other {
			ch.Slug = "other"
		}
		ch.URL = "https://youtube.com/@show"
		g.Go(func() error { _, err := r.SyncChannel(ctx, ch); return err })
	}
	for i := 0; i < 2; i++ {
		select {
		case <-detecting:
		case <-ctx.Done():
			release()
			t.Fatal("channels did not reach detection")
		}
	}
	select {
	case <-time.After(50 * time.Millisecond):
	case <-ctx.Done():
		release()
		t.Fatal(ctx.Err())
	}
	if calls.Load() != 0 {
		release()
		t.Fatal("queued model work bypassed the shared limit")
	}
	release()
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
	if maximum.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("model requests: maximum=%d calls=%d", maximum.Load(), calls.Load())
	}
	if err := f.runner.State.FinishSync(nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := state.ReadSnapshot(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LastRun.Usage.Attempts != 2 || snapshot.LastRun.Usage.InputTokens != 200 {
		t.Fatalf("concurrent usage lost: %+v", snapshot.LastRun.Usage)
	}
	for _, slug := range []string{"show", "other"} {
		ep, _ := f.runner.State.Episode(slug, lifecycleID)
		if ep.Stage != state.Published || ep.Usage.Total.InputTokens != 100 {
			t.Fatalf("%s publication/usage: %+v", slug, ep)
		}
		if _, err := os.Stat(filepath.Join(dir, "sources", slug, lifecycleID, "audio.m4a")); !os.IsNotExist(err) {
			t.Fatalf("%s source cleanup: %v", slug, err)
		}
		ch := config.Channel{Slug: slug}
		if _, err := pub.Get(context.Background(), ch.Slug+"/feed.xml"); err != nil {
			t.Fatal(err)
		}
	}
}
