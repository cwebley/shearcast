package storage_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/storage"
)

func TestFilesystemWriteProbeCleansUpAndNeverCreatesLibrary(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "public")
	s, err := storage.NewFilesystem(root, "http://example.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeWrite(context.Background()); err == nil {
		t.Fatal("probe should require existing directory")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("probe created publication directory")
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeWrite(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal("probe left temporary data")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.ProbeWrite(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

type probeTransport func(*http.Request) (*http.Response, error)

func (f probeTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestR2WriteProbeUsesUniqueKeysAndCleansUpAfterCanceledUpload(t *testing.T) {
	for _, cancelUpload := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		var uploaded, deleted string
		var content string
		client := &http.Client{Transport: probeTransport(func(req *http.Request) (*http.Response, error) {
			if !strings.Contains(req.URL.Path, "/.shearcast-doctor/") || strings.HasSuffix(req.URL.Path, "feed.xml") {
				t.Fatalf("probe touched library object: %s", req.URL.Path)
			}
			code, body := 200, ""
			switch req.Method {
			case "PUT":
				uploaded = req.URL.Path
				var reader io.Reader = req.Body
				if strings.Contains(req.Header.Get("Content-Encoding"), "aws-chunked") {
					reader = httputil.NewChunkedReader(reader)
				}
				data, err := io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				content = string(data)
				if cancelUpload {
					cancel()
					return nil, context.Canceled
				}
			case "GET":
				body = content
			case "DELETE":
				if req.Context().Err() != nil {
					t.Fatal("cleanup inherited canceled context")
				}
				deleted, code = req.URL.Path, 204
			default:
				t.Fatalf("unexpected %s", req.Method)
			}
			return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		s, err := storage.New(ctx, storage.Config{HTTPClient: client, AccountID: "test", Bucket: "bucket", AccessKeyID: "test", SecretAccessKey: "test", PublicBaseURL: "https://example.test"})
		if err != nil {
			t.Fatal(err)
		}
		err = s.ProbeWrite(ctx)
		cancel()
		if (err != nil) != cancelUpload {
			t.Fatalf("probe: %v", err)
		}
		if uploaded == "" || deleted != uploaded {
			t.Fatalf("probe not cleaned: uploaded=%s deleted=%s", uploaded, deleted)
		}
	}
}
