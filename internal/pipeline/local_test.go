package pipeline

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/storage"
)

func TestLocalPublicationRetainsOneManagedCopyAndRecovers(t *testing.T) {
	f := newLifecycleFixture(t)
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	local, err := storage.NewFilesystem(dir, "http://192.168.1.20:8080")
	if err != nil {
		t.Fatal(err)
	}
	publisher := &failingLocalPublisher{Filesystem: local, failFeed: true}
	f.runner.Publisher = publisher
	f.audio(t)
	ctx := context.Background()
	req := request(Sync)
	ep, err := f.runner.Run(ctx, req)
	if err == nil || ep.Stage != state.Rendered {
		t.Fatalf("expected retryable publication: %+v %v", ep, err)
	}
	private := ep.RenderPath
	f.reopen(t)
	f.source.infoErr = errors.New("source unavailable")
	f.runner.NewClient = nil
	publisher.failFeed = false
	ep, err = f.runner.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if ep.RenderPath != filepath.Join(dir, "show", lifecycleID+".m4a") {
		t.Fatalf("checkpoint does not use served audio: %s", ep.RenderPath)
	}
	if _, err := os.Stat(private); !os.IsNotExist(err) {
		t.Fatal("duplicate managed audio retained")
	}
	if _, err := os.Stat(private + ".json"); err != nil {
		t.Fatal("private processing record lost", err)
	}
	if _, err := os.Stat(ep.RenderPath + ".json"); !os.IsNotExist(err) {
		t.Fatal("processing record in public directory")
	}
	f.reopen(t)
	if err := f.runner.Recover(ctx, req.Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runner.Run(ctx, request(Publish)); err != nil {
		t.Fatalf("republishing served artifact: %v", err)
	}
	if f.modelCalls.Load() != 1 {
		t.Fatal("publication repeated detection")
	}
	data, err := local.Get(ctx, "show/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	fd, err := feed.Parse(data)
	if err != nil || len(fd.Items) != 1 || fd.Items[0].AudioURL != "http://192.168.1.20:8080/show/abcdefghijk.m4a" {
		t.Fatalf("feed: %+v %v", fd, err)
	}
	if err := f.runner.RemoveEpisode(ctx, req.Channel, lifecycleID); err != nil {
		t.Fatal(err)
	}
	if _, err := local.Get(ctx, "show/"+lifecycleID+".m4a"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("served audio survived removal", err)
	}
	if _, err := os.Stat(private + ".json"); err != nil {
		t.Fatal("removal lost private record", err)
	}
}

type failingLocalPublisher struct {
	*storage.Filesystem
	failFeed bool
}

func TestLocalReplacementKeepsPublishedAudioAndCallerOwnedImports(t *testing.T) {
	f := newLifecycleFixture(t)
	local, err := storage.NewFilesystem(t.TempDir(), "http://lan.test")
	if err != nil {
		t.Fatal(err)
	}
	f.runner.Publisher = local
	f.audio(t)
	ctx := context.Background()
	_, err = f.runner.Run(ctx, request(Sync))
	if err != nil {
		t.Fatal(err)
	}
	original, err := local.Get(ctx, "show/"+lifecycleID+".m4a")
	if err != nil {
		t.Fatal(err)
	}
	// A failed replacement cannot replace the served audio or its published history.
	f.source.cuesErr = errors.New("captions unavailable")
	if _, err := f.runner.Run(ctx, request(Reprocess)); err == nil {
		t.Fatal("expected caption failure")
	}
	f.reopen(t)
	if err := f.runner.Recover(ctx, request(Sync).Channel); err != nil {
		t.Fatal(err)
	}
	stillPublished, err := local.Get(ctx, "show/"+lifecycleID+".m4a")
	if err != nil || string(stillPublished) != string(original) {
		t.Fatal("failed replacement changed publication")
	}
	f.source.cuesErr = nil
	f.audio(t)
	req := request(Reprocess)
	req.Channel.BitrateKbps = 64
	ep, err := f.runner.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := local.Get(ctx, ep.PublishedAudioKey)
	if err != nil || string(replacement) == string(original) {
		t.Fatal("replacement did not publish")
	}
	// Explicit import remains the caller's file even when the library is purged.
	importPath := filepath.Join(t.TempDir(), "import.m4a")
	if err := os.WriteFile(importPath, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	req = request(Publish)
	req.AudioPath = importPath
	_, err = f.runner.Run(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.runner.PurgeChannel(ctx, req.Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(importPath); err != nil {
		t.Fatal("purge deleted caller-owned import", err)
	}
	if _, err := local.Get(ctx, "show/feed.xml"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("feed survived purge")
	}
}

func TestLocalRecoveryFinishesCleanupAfterPublishedCheckpoint(t *testing.T) {
	f := newLifecycleFixture(t)
	local, err := storage.NewFilesystem(t.TempDir(), "http://lan.test")
	if err != nil {
		t.Fatal(err)
	}
	f.runner.Publisher = local
	f.audio(t)
	ctx := context.Background()
	ep, err := f.runner.Run(ctx, request(Sync))
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the crash window after the published state save, before cleanup.
	data, err := os.ReadFile(ep.RenderPath)
	if err != nil {
		t.Fatal(err)
	}
	managed := RenderPath(f.runner.Cache, request(Sync).Channel, lifecycleID)
	if err := os.WriteFile(managed, data, 0o600); err != nil {
		t.Fatal(err)
	}
	f.reopen(t)
	if err := f.runner.Recover(ctx, request(Sync).Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatal("recovery retained duplicate")
	}
	if _, err := os.Stat(ep.RenderPath); err != nil {
		t.Fatal("recovery lost served audio")
	}
}

func TestRenderCannotWriteProcessingRecordsBesideServedAudio(t *testing.T) {
	f := newLifecycleFixture(t)
	local, err := storage.NewFilesystem(t.TempDir(), "http://lan.test")
	if err != nil {
		t.Fatal(err)
	}
	f.runner.Publisher = local
	f.audio(t)
	ep, err := f.runner.Run(context.Background(), request(Sync))
	if err != nil {
		t.Fatal(err)
	}
	req := request(Render)
	req.RenderOptions.OutPath = ep.RenderPath
	f.audio(t)
	if _, err := f.runner.Run(context.Background(), req); err == nil {
		t.Fatal("render accepted public output")
	}
	if f.modelCalls.Load() != 1 {
		t.Fatal("public output rejection spent model tokens")
	}
	if _, err := os.Stat(ep.RenderPath + ".json"); !os.IsNotExist(err) {
		t.Fatal("render exposed processing record")
	}
}

func (p *failingLocalPublisher) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (string, error) {
	if p.failFeed && strings.HasSuffix(key, "/feed.xml") {
		return "", errors.New("feed unavailable")
	}
	return p.Filesystem.Put(ctx, key, r, size, contentType)
}

func TestPublishingAdaptersShareFeedAndLibraryBehavior(t *testing.T) {
	for _, backend := range []string{"filesystem", "r2"} {
		t.Run(backend, func(t *testing.T) {
			f := newLifecycleFixture(t)
			ctx := context.Background()
			var publisher Publisher
			var err error
			if backend == "filesystem" {
				publisher, err = storage.NewFilesystem(t.TempDir(), "http://podcasts.test")
			} else {
				publisher, err = storage.New(ctx, storage.Config{AccountID: "test", Bucket: "test", AccessKeyID: "test", SecretAccessKey: "test", PublicBaseURL: "http://podcasts.test", HTTPClient: localR2HTTP(t)})
			}
			if err != nil {
				t.Fatal(err)
			}
			f.runner.Publisher = publisher
			f.audio(t)
			ep, err := f.runner.Run(ctx, request(Sync))
			if err != nil {
				t.Fatal(err)
			}
			f.source.video.Title = "Refreshed title"
			f.runner.NewClient = nil
			if _, err := f.runner.Run(ctx, request(Sync)); err != nil {
				t.Fatal(err)
			}
			data, err := publisher.Get(ctx, "show/feed.xml")
			if err != nil {
				t.Fatal(err)
			}
			fd, err := feed.Parse(data)
			if err != nil || len(fd.Items) != 1 || fd.Items[0].ID != lifecycleID || fd.Items[0].Title != "Refreshed title" || fd.Items[0].AudioURL != ep.AudioURL {
				t.Fatalf("metadata refresh: %+v %v", fd, err)
			}
			if err := f.runner.RemoveEpisode(ctx, request(Sync).Channel, lifecycleID); err != nil {
				t.Fatal(err)
			}
			f.reopen(t)
			if _, err := f.runner.Run(ctx, request(Sync)); err != nil {
				t.Fatal(err)
			}
			data, err = publisher.Get(ctx, "show/feed.xml")
			if err != nil {
				t.Fatal(err)
			}
			fd, err = feed.Parse(data)
			if err != nil || len(fd.Items) != 0 {
				t.Fatal("removed episode reappeared", err)
			}
			if _, err := publisher.Get(ctx, "show/"+lifecycleID+".m4a"); !errors.Is(err, storage.ErrNotFound) {
				t.Fatal("published audio not removed", err)
			}
			if err := f.runner.PurgeChannel(ctx, request(Sync).Channel); err != nil {
				t.Fatal(err)
			}
			if _, err := publisher.Get(ctx, "show/feed.xml"); !errors.Is(err, storage.ErrNotFound) {
				t.Fatal("purge retained feed", err)
			}
		})
	}
}

// Exercise the actual R2 adapter against a local S3 wire protocol fixture.
func localR2HTTP(t *testing.T) *http.Client {
	t.Helper()
	objects := map[string][]byte{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PUT":
			var body io.Reader = r.Body
			if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
				body = httputil.NewChunkedReader(body)
			}
			data, err := io.ReadAll(body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			objects[r.URL.Path] = data
			w.Header().Set("ETag", `"fixture"`)
		case "GET":
			data, ok := objects[r.URL.Path]
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(404)
				io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
				return
			}
			w.Write(data)
		case "DELETE":
			delete(objects, r.URL.Path)
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	return &http.Client{Transport: redirectedTransport{base: server.Client().Transport, endpoint: endpoint}}
}

type redirectedTransport struct {
	base     http.RoundTripper
	endpoint *url.URL
}

func (t redirectedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = t.endpoint.Scheme, t.endpoint.Host
	return t.base.RoundTrip(clone)
}
