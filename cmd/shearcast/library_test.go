package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/state"
)

func TestManagementCommandsShareStateLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte("[[channels]]\nslug='show'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, action := range []string{"remove", "restore", "reprocess"} {
		if err := runEpisode(context.Background(), []string{action, "abcdefghijk", "-channel", "show", "-config", cfg, "-state", path}); !errors.Is(err, state.ErrLocked) {
			t.Fatalf("episode %s: %v", action, err)
		}
	}
	for _, action := range []string{"add", "update", "remove"} {
		if err := runChannel(context.Background(), []string{action, "show", "-config", cfg, "-state", path}); !errors.Is(err, state.ErrLocked) {
			t.Fatalf("channel %s: %v", action, err)
		}
	}
}

func TestChannelStopAndResumePreserveHistoryWithoutStorageCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	cfgPath := filepath.Join(dir, "config.toml")
	args := []string{"-config", cfgPath, "-state", path}
	run := func(command ...string) {
		t.Helper()
		if err := runChannel(context.Background(), append(command, args...)); err != nil {
			t.Fatal(err)
		}
	}
	run("add", "show", "-url", "https://youtube.com/@show", "-name", "A show", "-latest", "3", "-keep", "6")
	st, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkProcessed("show", "abcdefghijk"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	run("update", "show", "-bitrate-kbps", "64")
	run("remove", "show")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	ch := cfg.Channels[0]
	if !ch.Disabled || ch.Latest != 3 || ch.Keep != 6 || ch.BitrateKbps != 64 {
		t.Fatalf("channel changed unexpectedly: %+v", ch)
	}
	run("update", "show", "-disabled=false")
	st, err = state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if !st.IsProcessed("show", "abcdefghijk") {
		t.Fatal("channel removal lost history")
	}
}

func TestPurgeIntentSurvivesFailedConfigWrite(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	cfgPath := filepath.Join(dir, "config.toml")
	st, err := state.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	// The config vanished before the stop could be saved. The purge request
	// must still survive, so a later sync can finish both the stop and deletion.
	if err := stopChannel(cfgPath, "show", st, true); err == nil {
		t.Fatal("expected missing-channel config failure")
	}
	st.Close()
	st, err = state.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if !st.PurgePending("show") {
		t.Fatal("lost purge request before config update")
	}
	if err := os.WriteFile(cfgPath, []byte("[[channels]]\nslug='show'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stopChannel(cfgPath, "show", st, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Channels[0].Disabled || !st.PurgePending("show") {
		t.Fatal("recovery failed to preserve stop and purge")
	}
}
