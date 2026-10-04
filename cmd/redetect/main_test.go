package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/youtube"
)

func TestRedetectRejectsUnmatchedSelectionBeforeLoadingCredentials(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "present.json"), []byte(`{"video_id":"present","channel":"show"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	previousFlags, previousArgs := flag.CommandLine, os.Args
	defer func() { flag.CommandLine, os.Args = previousFlags, previousArgs }()
	flag.CommandLine = flag.NewFlagSet("redetect", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = []string{"redetect", "-config", filepath.Join(root, "missing-config"), "-out", filepath.Join(root, "output"),
		"-labels", root, "-video", "present,absent"}
	err := run()
	if err == nil || !strings.Contains(err.Error(), "selectors match no labels: absent") {
		t.Fatalf("want unmatched selection error before credential loading, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "output")); !os.IsNotExist(err) {
		t.Fatal("invalid selection created output")
	}
}

func TestRedetectRejectsIncompleteCacheBeforeStartingCollection(t *testing.T) {
	for _, tc := range []struct {
		name, info, captions string
	}{
		{"missing metadata", "", "WEBVTT\n\n00:00:00.000 --> 00:00:30.000\nAn ordinary passage of the program's narration.\n"},
		{"invalid metadata", `{"id":`, ""},
		{"wrong identity", `{"id":"dQw4w9WgXcQ","title":"Wrong video","duration":60,"chapters":[]}`, ""},
		{"missing duration", `{"id":"SI2IggZ3Fac","title":"No duration","chapters":[]}`, ""},
		{"missing captions", `{"id":"SI2IggZ3Fac","title":"A video","duration":60,"chapters":[]}`, ""},
		{"empty captions", `{"id":"SI2IggZ3Fac","title":"A video","duration":60,"chapters":[]}`, "WEBVTT\n\n"},
		{"malformed captions", `{"id":"SI2IggZ3Fac","title":"A video","duration":60,"chapters":[]}`, "not a caption track"},
		{"out-of-order captions", `{"id":"SI2IggZ3Fac","title":"A video","duration":60,"chapters":[]}`, "WEBVTT\n\n00:00:30.000 --> 00:00:40.000\nA later passage of the program's narration.\n\n00:00:00.000 --> 00:00:10.000\nAn earlier passage of the program's narration.\n"},
		{"out-of-order repeated captions", `{"id":"SI2IggZ3Fac","title":"A video","duration":60,"chapters":[]}`, "WEBVTT\n\n00:00:30.000 --> 00:00:40.000\nA repeated passage of the program's narration.\n\n00:00:00.000 --> 00:00:10.000\nA repeated passage of the program's narration.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(path, data string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(root, "labels", "a.json"), `{"video_id":"dQw4w9WgXcQ","channel":"show"}`)
			write(filepath.Join(root, "labels", "b.json"), `{"video_id":"SI2IggZ3Fac","channel":"show"}`)
			write(filepath.Join(root, "cache", "dQw4w9WgXcQ", "info.json"), `{"id":"dQw4w9WgXcQ","title":"Ready episode","duration":60,"chapters":[]}`)
			write(filepath.Join(root, "cache", "dQw4w9WgXcQ", "video.en.vtt"), "WEBVTT\n\n00:00:00.000 --> 00:00:30.000\nAn ordinary passage of the program's narration.\n")
			if tc.info != "" {
				write(filepath.Join(root, "cache", "SI2IggZ3Fac", "info.json"), tc.info)
			}
			track := filepath.Join(root, "cache", "SI2IggZ3Fac", "video.en.vtt")
			if tc.captions != "" {
				write(track, tc.captions)
			}
			log := filepath.Join(root, "yt-dlp-called")
			bin := filepath.Join(root, "yt-dlp")
			write(bin, "#!/bin/sh\ntouch \""+log+"\"\nexit 1\n")
			if err := os.Chmod(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			previousBinary := youtube.Binary
			youtube.Binary = bin
			t.Cleanup(func() { youtube.Binary = previousBinary })
			previousFlags, previousArgs := flag.CommandLine, os.Args
			defer func() { flag.CommandLine, os.Args = previousFlags, previousArgs }()
			flag.CommandLine = flag.NewFlagSet("redetect", flag.ContinueOnError)
			flag.CommandLine.SetOutput(io.Discard)
			os.Args = []string{"redetect", "-config", filepath.Join(root, "missing-config"), "-out", filepath.Join(root, "output"),
				"-labels", filepath.Join(root, "labels"), "-cache", filepath.Join(root, "cache")}
			err := run()
			if err == nil || !strings.Contains(err.Error(), "cache-only preflight") || !strings.Contains(err.Error(), "SI2IggZ3Fac") {
				t.Fatalf("want cache-only failure identifying the unready episode before configuration/model access, got %v", err)
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("cache-only collection invoked yt-dlp")
			}
			if _, err := os.Stat(filepath.Join(root, "output")); !os.IsNotExist(err) {
				t.Fatal("unready input created experiment output")
			}
			if tc.captions != "" {
				data, err := os.ReadFile(track)
				if err != nil || string(data) != tc.captions {
					t.Fatalf("cache-only validation changed cached captions: %q %v", data, err)
				}
			}
		})
	}
}

func TestRedetectChecksUsableCacheWithoutCredentialsOrCollection(t *testing.T) {
	root := t.TempDir()
	for path, data := range map[string]string{
		"labels/a.json":                  `{"video_id":"dQw4w9WgXcQ","channel":"show"}`,
		"cache/dQw4w9WgXcQ/info.json":    `{"id":"dQw4w9WgXcQ","title":"Ready episode","duration":60,"chapters":[]}`,
		"cache/dQw4w9WgXcQ/video.en.vtt": "WEBVTT\n\n00:00:00.000 --> 00:00:30.000\nAn ordinary passage of the program's narration.\n\n00:00:20.000 --> 00:00:40.000\nAn overlapping caption with later text is valid.\n",
	} {
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENROUTER_KEY", "")
	previousBinary := youtube.Binary
	youtube.Binary = filepath.Join(root, "nonexistent-yt-dlp")
	t.Cleanup(func() { youtube.Binary = previousBinary })
	previousFlags, previousArgs := flag.CommandLine, os.Args
	defer func() { flag.CommandLine, os.Args = previousFlags, previousArgs }()
	flag.CommandLine = flag.NewFlagSet("redetect", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = []string{"redetect", "-check-cache", "-config", filepath.Join(root, "missing-config"),
		"-labels", filepath.Join(root, "labels"), "-cache", filepath.Join(root, "cache")}
	if err := run(); err != nil {
		t.Fatalf("usable cache check should succeed without yt-dlp, credentials, config or output: %v", err)
	}
}
