package youtube

import (
	"context"
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
		"https://www.youtube.com/watch?v=41hft__gwvY":       "41hft__gwvY",
		"https://www.youtube.com/watch?v=sXRPkBKqp-A&t=90s": "sXRPkBKqp-A",
		"https://youtu.be/uoEffocWP4A":                      "uoEffocWP4A",
		"https://www.youtube.com/shorts/nPLKfvr_y4E":        "nPLKfvr_y4E",
		"https://www.youtube.com/live/pf7Yxsrt0Qw":          "pf7Yxsrt0Qw",
		"https://www.youtube.com/@historyoftheuniverse":     "",
		"https://www.youtube.com/watch?v=short":             "",
		"not an id":                                         "",
	}
	for in, want := range cases {
		if got := VideoID(in); got != want {
			t.Errorf("VideoID(%q) = %q, want %q", in, got, want)
		}
	}
}
