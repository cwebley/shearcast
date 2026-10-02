package youtube

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordArgs installs fakeListing with body, after it appends each call's
// arguments as one line to the returned file.
func recordArgs(t *testing.T, body string) string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "args")
	fakeListing(t, `echo "$*" >> '`+log+`'
`+body)
	return log
}

func TestInfoRequestsOnlyTheCanonicalVideo(t *testing.T) {
	log := recordArgs(t, `echo '{"id":"41hft__gwvY","title":"A video"}'`)
	if _, err := Info(context.Background(), "https://www.youtube.com/watch?v=41hft__gwvY&list=PLabc&index=3"); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(log)
	if !strings.Contains(string(args), "--no-playlist") || !strings.HasSuffix(strings.TrimSpace(string(args)), " https://www.youtube.com/watch?v=41hft__gwvY") {
		t.Fatalf("args: %s", args)
	}
}

func TestCacheRefusesMetadataForAnotherVideo(t *testing.T) {
	// The extractor's id is never trusted as a path: it must be the id asked for.
	recordArgs(t, `echo '{"id":"../../victim","title":"x"}'`)
	dir := t.TempDir()
	cache := Cache{Dir: filepath.Join(dir, "cache")}
	if _, err := cache.Info(context.Background(), "41hft__gwvY"); err == nil {
		t.Fatal("accepted a different returned id")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("wrote outside the request: %v", entries)
	}
	if _, err := Info(context.Background(), "https://example.test/x.mp4?v=41hft__gwvY"); err == nil {
		t.Fatal("accepted a non-YouTube URL")
	}
}

func TestCaptionRequestsDisableTheOtherSubtitleMode(t *testing.T) {
	// No track is written, so both requests run.
	log := recordArgs(t, `exit 0`)
	Captions(context.Background(), "https://www.youtube.com/watch?v=41hft__gwvY", t.TempDir())
	data, _ := os.ReadFile(log)
	calls := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(calls) != 2 ||
		!strings.Contains(calls[0], "--write-subs --no-write-auto-subs --sub-langs en ") ||
		!strings.Contains(calls[1], "--write-auto-subs --no-write-subs --sub-langs en-orig ") {
		t.Fatalf("caption requests: %q", calls)
	}
}

func TestDownloadSurvivesOversizedOutputLines(t *testing.T) {
	// One 2 MiB line, more than the scanner holds and more than a pipe buffers.
	fakeListing(t, `head -c 2097152 /dev/zero | tr '\0' x
echo
for a in "$@"; do out="$a"; done
prev=""
for a in "$@"; do [ "$prev" = "-o" ] && touch "$a"; prev="$a"; done
`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out := filepath.Join(t.TempDir(), "audio.m4a")
	if err := DownloadAudio(ctx, "https://www.youtube.com/watch?v=41hft__gwvY", out, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
}
