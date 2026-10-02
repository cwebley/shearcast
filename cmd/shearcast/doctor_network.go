package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/storage"
)

// GET /api/v1/key authenticates without invoking the decisions model. Response
// bodies are deliberately never printed: they contain account/key metadata.
func checkOpenRouterKey(ctx context.Context, client *http.Client, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/key", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("OpenRouter authentication returned HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data *struct {
			Management bool       `json:"is_management_key"`
			Expires    *time.Time `json:"expires_at"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil || body.Data == nil {
		return fmt.Errorf("OpenRouter returned an unrecognized key response")
	}
	if body.Data.Management {
		return fmt.Errorf("OpenRouter key is management-only; processing needs an inference key")
	}
	if body.Data.Expires != nil && body.Data.Expires.Before(time.Now()) {
		return fmt.Errorf("OpenRouter key has expired")
	}
	return nil
}

func feedExpected(s *state.Snapshot, slug string) bool {
	for _, ep := range s.Episodes[slug] {
		if ep.HasPublished && ep.Removal == "" {
			return true
		}
	}
	return false
}

func checkStoredFeed(r *doctorReport, slug string, s *state.Snapshot, data []byte, err error) bool {
	if errors.Is(err, storage.ErrNotFound) && !feedExpected(s, slug) {
		r.line("WARN", slug+" stored feed", "absent; no active publication recorded, normal for an empty or purged library")
		return false
	}
	if err == nil {
		_, err = feed.Parse(data)
	}
	r.check(slug+" stored feed", "readable RSS", err)
	return err == nil
}

func checkPublicFeed(ctx context.Context, r *doctorReport, client *http.Client, slug, address string, expected bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if !r.check(slug+" feed request", address, err) {
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		r.check(slug+" public feed", "", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && !expected {
		r.line("WARN", slug+" public feed", "HTTP 404; feed not yet known to exist, normal before first publication")
		return
	}
	if resp.StatusCode != http.StatusOK {
		r.line("FAIL", slug+" public feed", fmt.Sprintf("HTTP %d; check server, routing and base URL", resp.StatusCode))
		return
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err == nil && len(data) > 4<<20 {
		err = fmt.Errorf("feed exceeds diagnostic size limit of 4 MiB")
	}
	if err != nil {
		r.check(slug+" public feed", "", err)
		return
	}
	fd, err := feed.Parse(data)
	if !r.check(slug+" public feed", "RSS reachable from this machine; client-side reachability unverified", err) {
		return
	}
	if len(fd.Items) == 0 {
		r.line("SKIP", slug+" media", "feed has no enclosures to check")
		return
	}
	address = fd.Items[0].AudioURL
	u, err := url.Parse(address)
	if err == nil && (u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" || u.User != nil) {
		err = fmt.Errorf("enclosure must be an HTTP(S) URL without credentials")
	}
	if !r.check(slug+" sample enclosure", address, err) {
		return
	}
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		req, err := http.NewRequestWithContext(ctx, method, address, nil)
		if err != nil {
			r.check(slug+" media", "", err)
			return
		}
		if method == http.MethodGet {
			req.Header.Set("Range", "bytes=0-0")
		}
		resp, err := client.Do(req)
		if err != nil {
			r.check(slug+" media "+method, "", err)
			continue
		}
		if method == http.MethodHead {
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("HEAD returned HTTP %d", resp.StatusCode)
			}
		} else if resp.StatusCode != http.StatusPartialContent || !strings.HasPrefix(resp.Header.Get("Content-Range"), "bytes 0-0/") {
			err = fmt.Errorf("one-byte range not honored; HTTP %d", resp.StatusCode)
		} else {
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, 2))
			if readErr != nil {
				err = readErr
			} else if len(data) != 1 {
				err = fmt.Errorf("range response did not contain one byte")
			}
		}
		resp.Body.Close()
		r.check(slug+" media "+method, "sample enclosure supports "+method, err)
	}
}
