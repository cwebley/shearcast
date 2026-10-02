package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeYTDLP installs a stand-in yt-dlp that logs each invocation, answers
// metadata requests, and writes a caption track wherever -o points.
func fakeYTDLP(t *testing.T) (calls func() int) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := `#!/bin/sh
echo "$@" >> "` + log + `"
out=""
while [ $# -gt 0 ]; do
  case "$1" in
    --dump-single-json) meta=1 ;;
    -o) shift; out="$1" ;;
  esac
  shift
done
if [ -n "$meta" ]; then
  echo '{"id":"dQw4w9WgXcQ","title":"A video","duration":212}'
  exit 0
fi
dest=$(printf '%s' "$out" | sed 's/%(id)s\.%(ext)s/dQw4w9WgXcQ.en.vtt/')
printf 'WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nhello\n' > "$dest"
`
	bin := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := Binary
	Binary = bin
	t.Cleanup(func() { Binary = old })

	return func() int {
		data, err := os.ReadFile(log)
		if err != nil {
			return 0
		}
		return strings.Count(string(data), "\n")
	}
}

func TestCacheRetainsChapterShapeAndRefreshesOldMetadata(t *testing.T) {
	calls := fakeYTDLP(t)
	c := Cache{Dir: t.TempDir()}
	id := "dQw4w9WgXcQ"
	if err := os.MkdirAll(c.videoDir(id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.videoDir(id), "info.json"), []byte(`{"id":"dQw4w9WgXcQ","title":"Old cache"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := c.Info(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if calls() != 1 {
		t.Fatalf("old cache should refresh once: %d", calls())
	}
	var video Video
	if err := json.Unmarshal([]byte(`{"id":"dQw4w9WgXcQ","chapters":[{"start_time":0,"end_time":5.5,"title":"Opening"},{"start_time":5.5,"title":"End"}]}`), &video); err != nil {
		t.Fatal(err)
	}
	if err := c.writeInfo(&video); err != nil {
		t.Fatal(err)
	}
	got, err := c.Info(context.Background(), id)
	if err != nil || len(got.Chapters) != 2 || got.Chapters[0].End == nil || *got.Chapters[0].End != 5.5 || got.Chapters[1].End != nil || calls() != 1 {
		t.Fatalf("chapter cache: %+v %v", got, err)
	}
}

func TestCacheFetchesOnce(t *testing.T) {
	calls := fakeYTDLP(t)
	ctx := context.Background()
	cache := Cache{Dir: t.TempDir()}
	url := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"

	if cache.Cached("dQw4w9WgXcQ") {
		t.Fatal("empty cache reports a hit")
	}
	for run := 0; run < 3; run++ {
		v, err := cache.Info(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		if v.Title != "A video" || v.Duration != 212 {
			t.Fatalf("run %d: metadata = %+v", run, v)
		}
		path, err := cache.Captions(ctx, v)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(path) != "dQw4w9WgXcQ.en.vtt" {
			t.Fatalf("run %d: track = %s", run, path)
		}
	}
	if got := calls(); got != 2 {
		t.Errorf("yt-dlp ran %d times over three runs, want 2 (metadata once, captions once)", got)
	}
	if !cache.Cached("dQw4w9WgXcQ") {
		t.Error("cache does not report a hit after fetching")
	}
}

func TestCacheIgnoresTruncatedInfo(t *testing.T) {
	calls := fakeYTDLP(t)
	cache := Cache{Dir: t.TempDir()}
	dir := filepath.Join(cache.Dir, "dQw4w9WgXcQ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "info.json"), []byte(`{"id":"dQw4`), 0o644); err != nil {
		t.Fatal(err)
	}

	v, err := cache.Info(context.Background(), "dQw4w9WgXcQ")
	if err != nil {
		t.Fatal(err)
	}
	if v.Title != "A video" {
		t.Errorf("title = %q, want refetched metadata", v.Title)
	}
	if calls() != 1 {
		t.Errorf("yt-dlp ran %d times, want 1 refetch", calls())
	}
}

func TestVideoID(t *testing.T) {
	cases := map[string]string{
		"SI2IggZ3Fac": "SI2IggZ3Fac",
		"https://www.youtube.com/watch?v=41hft__gwvY":            "41hft__gwvY",
		"https://www.youtube.com/watch?v=sXRPkBKqp-A&t=90s":      "sXRPkBKqp-A",
		"https://youtu.be/uoEffocWP4A":                           "uoEffocWP4A",
		"https://www.youtube.com/shorts/nPLKfvr_y4E":             "nPLKfvr_y4E",
		"https://www.youtube.com/live/pf7Yxsrt0Qw":               "pf7Yxsrt0Qw",
		"https://www.youtube.com/@historyoftheuniverse":          "",
		"https://www.youtube.com/watch?v=short":                  "",
		"not an id":                                              "",
		"youtube.com/watch?v=41hft__gwvY":                        "41hft__gwvY",
		"https://m.youtube.com/watch?v=41hft__gwvY":              "41hft__gwvY",
		"https://example.test/%2e%2e%2fvictim.mp4?v=abcdefghijk": "",
		"https://example.test/shorts/nPLKfvr_y4E":                "",
		"https://notyoutu.be/uoEffocWP4A":                        "",
	}
	for in, want := range cases {
		if got := VideoID(in); got != want {
			t.Errorf("VideoID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCaptionAbsenceAndTransportFailureAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		missing      bool
	}{
		{"missing", "#!/bin/sh\nexit 0\n", true},
		{"rate limited", "#!/bin/sh\nprintf 'HTTP Error 429' >&2\nexit 1\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "yt-dlp")
			if err := os.WriteFile(bin, []byte(tc.script), 0o755); err != nil {
				t.Fatal(err)
			}
			old := Binary
			Binary = bin
			t.Cleanup(func() { Binary = old })
			dir := t.TempDir()
			_, err := Captions(context.Background(), "https://youtube.com/watch?v=dQw4w9WgXcQ", dir)
			if err == nil || errors.Is(err, ErrCaptionsUnavailable) != tc.missing {
				t.Fatalf("wrong error classification: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("download staging remains: %v, %v", entries, err)
			}
		})
	}
}

func TestMetadataRefreshDoesNotFetchCaptions(t *testing.T) {
	calls := fakeYTDLP(t)
	c := Cache{Dir: t.TempDir()}
	ctx := context.Background()
	if _, err := c.Info(ctx, "dQw4w9WgXcQ"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RefreshInfo(ctx, "dQw4w9WgXcQ"); err != nil {
		t.Fatal(err)
	}
	if calls() != 2 {
		t.Fatalf("calls = %d, want two metadata requests", calls())
	}
	if _, ok := cachedTrack(c.videoDir("dQw4w9WgXcQ")); ok {
		t.Fatal("metadata refresh downloaded captions")
	}
}

func TestMalformedCaptionCacheCanRecover(t *testing.T) {
	calls := fakeYTDLP(t)
	c := Cache{Dir: t.TempDir()}
	v := &Video{ID: "dQw4w9WgXcQ"}
	dir := c.videoDir(v.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, v.ID+".en.vtt"), []byte("WEBVTT\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Cues(context.Background(), v); err == nil || errors.Is(err, ErrCaptionsUnavailable) {
		t.Fatalf("empty caption file should be malformed, got %v", err)
	}
	cues, err := c.Cues(context.Background(), v)
	if err != nil || len(cues) == 0 || calls() != 1 {
		t.Fatalf("did not refetch malformed captions: %v, %v, %d calls", cues, err, calls())
	}
}
