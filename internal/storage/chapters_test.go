package storage_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/storage"
)

func TestChapterHTTPAllowlistAndContentType(t *testing.T) {
	s, err := storage.NewFilesystem(t.TempDir(), "https://podcasts.test/podcasts")
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	public := "show/abcdefghijk." + digest + ".chapters.json"
	audio := "show/abcdefghijk." + digest + ".m4a"
	for _, key := range []string{public, audio, "show/abcdefghijk.chapters.json", "show/abcdefghijk.m4a.json", "show/abcdefghijk." + digest + ".json", "show/abcdefghijk.short.chapters.json"} {
		if _, err := s.Put(context.Background(), key, strings.NewReader("{}"), 2, ""); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	for _, key := range []string{public, audio, "show/abcdefghijk.chapters.json", "show/abcdefghijk.m4a.json", "show/abcdefghijk." + digest + ".json", "show/abcdefghijk.short.chapters.json"} {
		for _, method := range []string{"GET", "HEAD"} {
			req, _ := http.NewRequest(method, srv.URL+"/podcasts/"+key, nil)
			res, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if key != public && key != audio {
				if res.StatusCode != 404 {
					t.Fatalf("served private path %s", key)
				}
				continue
			}
			wantType := "application/json+chapters"
			if key == audio {
				wantType = "audio/mp4"
			}
			if res.StatusCode != 200 || res.Header.Get("Content-Type") != wantType || res.Header.Get("Cache-Control") != "no-cache" || method == "HEAD" && len(body) != 0 {
				t.Fatalf("%s %s: %d %v %s", method, key, res.StatusCode, res.Header, body)
			}
		}
	}
}
