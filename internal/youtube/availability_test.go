package youtube

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeListing installs a yt-dlp that runs body with its arguments intact; the
// body also sees the requested --playlist-end as $end.
func fakeListing(t *testing.T, body string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "yt-dlp")
	script := `#!/bin/sh
end=0 prev=""
for a in "$@"; do
  [ "$prev" = "--playlist-end" ] && end="$a"
  prev="$a"
done
` + body
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := Binary
	Binary = bin
	t.Cleanup(func() { Binary = old })
}

func TestListReachesPastInaccessibleUploads(t *testing.T) {
	// Newest first, as the channel's videos tab lists them. Flat listings
	// report public videos with a null availability.
	fakeListing(t, `n=0
for line in \
  '{"id":"members0001","availability":"subscriber_only"}' \
  '{"id":"premiere001","availability":null,"live_status":"is_upcoming"}' \
  '{"id":"private0001","availability":"private"}' \
  '{"id":"public00001","availability":null}' \
  '{"id":"unlisted001","availability":"unlisted"}' \
  '{"id":"public00002","availability":"public"}'; do
  n=$((n+1))
  [ "$n" -gt "$end" ] && break
  echo "$line"
done
`)
	list, err := List(context.Background(), "https://youtube.com/@example", 2)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ id, skip string }{
		{"members0001", "members-only"},
		{"premiere001", "upcoming"},
		{"private0001", "private"},
		{"public00001", ""},
		{"unlisted001", ""},
	}
	if len(list) != len(want) {
		t.Fatalf("listing = %+v, want %d entries ending at the second accessible upload", list, len(want))
	}
	for i, w := range want {
		if list[i].ID != w.id || list[i].Unavailable() != w.skip {
			t.Fatalf("entry %d = %s (%q), want %s (%q)", i, list[i].ID, list[i].Unavailable(), w.id, w.skip)
		}
	}
}

func TestInfoClassifiesInaccessibleVideos(t *testing.T) {
	for _, tc := range []struct {
		name, stderr string
		unavailable  bool
	}{
		{"members-only", "ERROR: [youtube] BHPDsGVciDk: Join this channel to get access to members-only content like this video, and other exclusive perks.", true},
		{"private", "ERROR: [youtube] abcdefghijk: Private video. Sign in if you have been granted access to this video", true},
		{"premiere", "ERROR: [youtube] abcdefghijk: Premieres in 3 hours", true},
		{"scheduled live", "ERROR: [youtube] abcdefghijk: This live event will begin in 2 days.", true},
		{"rate limited", "ERROR: [youtube] abcdefghijk: HTTP Error 429: Too Many Requests", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeListing(t, "printf '%s\\n' '"+tc.stderr+"' >&2\nexit 1\n")
			_, err := Info(context.Background(), "https://www.youtube.com/watch?v=abcdefghijk")
			if err == nil || errors.Is(err, ErrUnavailable) != tc.unavailable {
				t.Fatalf("wrong classification: %v", err)
			}
		})
	}
}

func TestInfoAcceptsBareIDStartingWithDash(t *testing.T) {
	// yt-dlp reads a bare "-xJOQXFk3hI" as an option and then finds no URL.
	fakeListing(t, `for a in "$@"; do
  case "$a" in https://*) url="$a" ;; esac
done
[ -z "$url" ] && { echo "yt-dlp: error: You must provide at least one URL." >&2; exit 2; }
echo '{"id":"-xJOQXFk3hI"}'
`)
	v, err := Info(context.Background(), "-xJOQXFk3hI")
	if err != nil || v.ID != "-xJOQXFk3hI" {
		t.Fatalf("info: %+v, %v", v, err)
	}
}
