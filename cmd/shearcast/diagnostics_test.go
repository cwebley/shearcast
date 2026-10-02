package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/storage"
)

func diagnosticFixture(t *testing.T) (dir string, args []string) {
	t.Helper()
	dir = t.TempDir()
	text := "[publishing]\nbackend='filesystem'\ndirectory='public'\nbase_url='http://127.0.0.1:8080'\n[[channels]]\nslug='show'\nurl='https://youtube.com/@show'\nno_weights=true\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"OPENROUTER_API_KEY", "OPENROUTER_KEY", "R2_ACCOUNT_ID", "R2_BUCKET", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY", "R2_PUBLIC_BASE_URL"} {
		t.Setenv(name, "")
	}
	return dir, []string{"-config", filepath.Join(dir, "config.toml"), "-state", filepath.Join(dir, "state.json")}
}

func TestStatusReadsBusyLibraryOfflineAndPreservesErrors(t *testing.T) {
	dir, args := diagnosticFixture(t)
	st, err := state.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(st.StartSync([]string{"show"}))
	check(st.StartChannelSync("show"))
	check(st.FinishChannelSync("show", false, errors.New("listing failed")))
	check(st.FinishSync(errors.New("show failed")))
	check(st.Save("show", "local000001", state.Episode{Stage: state.Rendered}))
	check(st.Save("show", "retry000001", state.Episode{Stage: state.Rendered, PublishPending: true, LastError: "upload failed"}))
	check(st.Save("show", "wait0000001", state.Episode{Stage: state.Waiting, PublishPending: true}))
	check(st.Save("show", "replace0001", state.Episode{Stage: state.Pending, HasPublished: true, LastError: "replacement failed"}))
	check(st.Save("show", "delete00001", state.Episode{Stage: state.Published, Removal: "excluded", DeletePending: true}))
	check(st.SetPurgePending("show", true))
	before, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	t.Setenv("PATH", "")
	var out bytes.Buffer
	check(statusCommand(args, &out))
	for _, text := range []string{"listing failed", "upload failed", "waiting captions", "local render; not queued", "previous publication recorded", "deletion pending", "channel purge pending", "last successful sync: unknown", "availability unchecked"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("missing %q in:\n%s", text, out.String())
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("status changed state")
	}
	if _, err := os.Stat(filepath.Join(dir, "public")); !os.IsNotExist(err) {
		t.Fatal("status created publication directory")
	}
}

func TestStatusMissingHistoryAndCorruption(t *testing.T) {
	dir, args := diagnosticFixture(t)
	var out bytes.Buffer
	if err := statusCommand(args, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "State file absent") {
		t.Fatal(out.String())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("status created files")
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := statusCommand(args, io.Discard); err == nil {
		t.Fatal("status accepted unsupported state")
	}
}

func TestDoctorLocalServingNeedsNoProcessingDependenciesOrState(t *testing.T) {
	dir, args := diagnosticFixture(t)
	t.Setenv("PATH", "")
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`invalid`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	args = append(args, "-cache", filepath.Join(dir, "cache"), "-operation", "serve")
	if err := doctorCommand(context.Background(), args, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, text := range []string{"OpenRouter key", "R2 settings", "ffmpeg", "ffprobe", "yt-dlp"} {
		if strings.Contains(out.String(), text) {
			t.Fatalf("serving checked processing dependency %s", text)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatal("doctor created state lock or directories")
	}
}

func TestDoctorLocalNeverContactsServicesAndReportsProcessingFailures(t *testing.T) {
	dir, args := diagnosticFixture(t)
	t.Setenv("PATH", "")
	previous := http.DefaultTransport
	http.DefaultTransport = diagnosticTransport(func(req *http.Request) (*http.Response, error) {
		t.Errorf("unexpected network request: %s", req.URL)
		return nil, errors.New("network disabled")
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	var out bytes.Buffer
	err := doctorCommand(context.Background(), append(args, "-cache", filepath.Join(dir, "cache"), "-operation", "sync"), &out)
	if err == nil {
		t.Fatal("missing dependencies passed")
	}
	for _, text := range []string{"FAIL yt-dlp", "FAIL ffmpeg", "FAIL ffprobe", "FAIL OpenRouter key", "reachability unchecked"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("missing %q in:\n%s", text, out.String())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("doctor created files")
	}
}

func TestDoctorPublicationRetryChecksWithoutModelOrSource(t *testing.T) {
	dir, args := diagnosticFixture(t)
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ffprobe"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	path := filepath.Join(dir, "audio.m4a")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Save("show", "abcdefghijk", state.Episode{Stage: state.Rendered, PublishPending: true, RenderPath: path, AudioSHA256: "recorded"}); err != nil {
		t.Fatal(err)
	}
	args = append(args, "-cache", filepath.Join(dir, "cache"), "-operation", "publish")
	var out bytes.Buffer
	if err := doctorCommand(context.Background(), args, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "OpenRouter") || strings.Contains(out.String(), "yt-dlp") {
		t.Fatal("retry required source/model")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := doctorCommand(context.Background(), args, io.Discard); err == nil {
		t.Fatal("missing retry audio passed")
	}
}

func TestDoctorRejectsDestinationChangeBeforeWriteProbe(t *testing.T) {
	dir, args := diagnosticFixture(t)
	st, err := state.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.BindPublishing(state.PublishingDestination{Backend: "r2", Location: "account/bucket", BaseURL: "http://127.0.0.1:8080"}, false); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = doctorCommand(context.Background(), append(args, "-cache", filepath.Join(dir, "cache"), "-operation", "publish", "-write-probe"), &out)
	if err == nil || !strings.Contains(out.String(), "publishing destination changed") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "public")); !os.IsNotExist(err) {
		t.Fatal("probe wrote into changed destination")
	}
}

func TestDoctorNetworkChecksRSSHeadAndRanges(t *testing.T) {
	dir, args := diagnosticFixture(t)
	var handler http.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { handler.ServeHTTP(w, req) }))
	defer server.Close()
	store, err := storage.NewFilesystem(filepath.Join(dir, "public"), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	handler = store.Handler()
	fd := feed.Feed{Title: "Example", Items: []feed.Item{{ID: "abcdefghijk", Title: "Episode", PublishedAt: time.Now(), AudioURL: server.URL + "/show/abcdefghijk.m4a", AudioBytes: 5, Duration: time.Second}}}
	data, err := fd.XML()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), "show/feed.xml", bytes.NewReader(data), int64(len(data)), "application/rss+xml"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), "show/abcdefghijk.m4a", strings.NewReader("audio"), 5, "audio/mp4"); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.toml")
	text, _ := os.ReadFile(configPath)
	text = bytes.ReplaceAll(text, []byte("http://127.0.0.1:8080"), []byte(server.URL))
	if err := os.WriteFile(configPath, text, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := doctorCommand(context.Background(), append(args, "-operation", "serve", "-network", "-cache", filepath.Join(dir, "cache")), &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, text := range []string{"PASS show public feed", "PASS show media HEAD", "PASS show media GET"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("missing %q\n%s", text, out.String())
		}
	}
}

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestDoctorOpenRouterAuthNeverInvokesModelOrPrintsResponse(t *testing.T) {
	for _, code := range []int{200, 401, 500} {
		client := &http.Client{Transport: diagnosticTransport(func(req *http.Request) (*http.Response, error) {
			if req.Method != "GET" || req.URL.String() != "https://openrouter.ai/api/v1/key" || req.Header.Get("Authorization") != "Bearer secret-key" {
				t.Fatalf("unexpected auth probe: %+v", req)
			}
			return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(`{"data":{"label":"secret-key"},"message":"secret-key"}`)), Header: http.Header{}}, nil
		})}
		err := checkOpenRouterKey(context.Background(), client, "secret-key")
		if (err == nil) != (code == 200) {
			t.Fatalf("HTTP %d: %v", code, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret-key") {
			t.Fatal("leaked response body")
		}
	}
}

func TestDoctorRejectsMalformedRSSBeforeWriteProbe(t *testing.T) {
	for _, data := range []string{`<html/>`, `<rss/>`, `<rss><channel/><channel/></rss>`} {
		if _, err := feed.Parse([]byte(data)); err == nil {
			t.Fatalf("accepted malformed RSS %s", data)
		}
	}
	if _, err := feed.Parse([]byte(`<rss><channel><title>Empty feed</title></channel></rss>`)); err != nil {
		t.Fatalf("rejected empty channel: %v", err)
	}
	for _, name := range []string{"R2_ACCOUNT_ID", "R2_BUCKET", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY"} {
		t.Setenv(name, "fixture")
	}
	t.Setenv("R2_PUBLIC_BASE_URL", "https://public.example.test")
	writes := 0
	client := &http.Client{Transport: diagnosticTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" {
			writes++
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`<rss/>`))}, nil
	})}
	var out bytes.Buffer
	r := &doctorReport{out: &out}
	checkPublishing(context.Background(), r, config.Default(), []config.Channel{{Slug: "show"}}, &state.Snapshot{}, "publish", true, true, client, time.Second)
	if writes != 0 || r.failures == 0 || !strings.Contains(out.String(), "publishing prerequisites failed") {
		t.Fatalf("writes=%d failures=%d\n%s", writes, r.failures, out.String())
	}
}

func TestSyncInvocationPersistsPartialFailureAndScopedHistory(t *testing.T) {
	dir, args := diagnosticFixture(t)
	configPath := filepath.Join(dir, "config.toml")
	text, _ := os.ReadFile(configPath)
	text = append(text, []byte("\n[[channels]]\nslug='broken'\nurl='https://youtube.com/@broken'\n\n[[channels]]\nslug='disabled'\ndisabled=true\n")...)
	if err := os.WriteFile(configPath, text, 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$*\" in *broken*) printf 'listing failed' >&2; exit 1;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "yt-dlp"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	args = append(args, "-cache", filepath.Join(dir, "cache"))
	if err := runSync(context.Background(), args); err == nil {
		t.Fatal("expected partial failure")
	}
	snapshot, err := state.ReadSnapshot(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LastRun.Result != "failed" || snapshot.Sync["show"].Latest.Result != "success" || snapshot.Sync["broken"].Latest.Result != "failed" || snapshot.Sync["disabled"].Latest.Result != "skipped" || !snapshot.Sync["disabled"].LastSuccess.IsZero() {
		t.Fatalf("bad histories: %+v", snapshot.Sync)
	}
	broken := snapshot.Sync["broken"]
	if err := runSync(context.Background(), append(args, "-channel", "show")); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = state.ReadSnapshot(filepath.Join(dir, "state.json"))
	if snapshot.LastRun.Result != "success" || fmt.Sprint(snapshot.LastRun.Channels) != "[show]" || !reflect.DeepEqual(snapshot.Sync["broken"], broken) {
		t.Fatal("scoped run changed other history")
	}
	before, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	if err := runSync(context.Background(), append(args, "-channel", "show", "-dry-run")); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("dry run recorded a real sync")
	}
}
