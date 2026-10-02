package storage_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/storage"
	"github.com/cwebley/shearcast/internal/subscribe"
)

func TestLocalHTTPFeedsDownloadsAndSeeking(t *testing.T) {
	s, err := storage.NewFilesystem(t.TempDir(), "http://lan.test/podcasts")
	if err != nil {
		t.Fatal(err)
	}
	for key, body := range map[string]string{"show/feed.xml": "<rss/>", "show/abcdefghijk.m4a": "0123456789"} {
		if _, err := s.Put(context.Background(), key, strings.NewReader(body), int64(len(body)), ""); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	for _, tc := range []struct {
		method, path, byteRange, body, contentType string
		status                                     int
	}{
		{"GET", "/podcasts/show/feed.xml", "", "<rss/>", "application/rss+xml", 200},
		{"GET", "/podcasts/show/abcdefghijk.m4a", "", "0123456789", "audio/mp4", 200},
		{"HEAD", "/podcasts/show/abcdefghijk.m4a", "", "", "audio/mp4", 200},
		{"GET", "/podcasts/show/abcdefghijk.m4a", "bytes=2-5", "2345", "audio/mp4", 206},
		{"GET", "/podcasts/show/abcdefghijk.m4a", "bytes=-3", "789", "audio/mp4", 206},
	} {
		req, _ := http.NewRequest(tc.method, server.URL+tc.path, nil)
		req.Header.Set("Range", tc.byteRange)
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil || res.StatusCode != tc.status || string(data) != tc.body || res.Header.Get("Content-Type") != tc.contentType {
			t.Fatalf("%+v: status %d headers %v body %q error %v", tc, res.StatusCode, res.Header, data, err)
		}
		if tc.byteRange == "bytes=2-5" && res.Header.Get("Content-Range") != "bytes 2-5/10" {
			t.Fatal("incorrect Content-Range", res.Header)
		}
		if res.Header.Get("ETag") == "" || res.Header.Get("Cache-Control") != "no-cache" {
			t.Fatal("missing revalidation headers")
		}
	}
	req, _ := http.NewRequest("GET", server.URL+"/podcasts/show/abcdefghijk.m4a", nil)
	req.Header.Set("Range", "bytes=100-")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("invalid range: %d", res.StatusCode)
	}
}

func TestLocalHTTPServesSubscriptionPage(t *testing.T) {
	s, err := storage.NewFilesystem(t.TempDir(), "http://lan.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{subscribe.PageKey, subscribe.OPMLKey} {
		if _, err := s.Put(context.Background(), key, strings.NewReader("x"), 1, ""); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	for path, want := range map[string]string{
		"/" + subscribe.PageKey: subscribe.PageType,
		"/" + subscribe.OPMLKey: subscribe.OPMLType,
	} {
		res, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != want {
			t.Errorf("%s: status %d, type %q; want 200, %q", path, res.StatusCode, res.Header.Get("Content-Type"), want)
		}
	}
}

func TestLocalHTTPOnlyServesPublishedFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := storage.NewFilesystem(dir, "http://lan.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "show"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.json", "show/abcdefghijk.m4a.json", "show/.publish-secret"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../state.json", filepath.Join(dir, "show/abcdefghijk.m4a")); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	for _, path := range []string{"/", "/show/", "/state.json", "/show/abcdefghijk.m4a.json", "/show/.publish-secret", "/show/abcdefghijk.m4a", "/show/%2e%2e/state.json"} {
		res, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Fatalf("%s: %d", path, res.StatusCode)
		}
	}
	req, _ := http.NewRequest("DELETE", server.URL+"/show/feed.xml", nil)
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 405 {
		t.Fatalf("write request: %d", res.StatusCode)
	}
}
