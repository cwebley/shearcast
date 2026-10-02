package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/config"
)

// xdgHome points the XDG config and state directories at fresh temp dirs, as
// on a machine where Shearcast has never run.
func xdgHome(t *testing.T) (configPath, statePath string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SHEARCAST_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	return filepath.Join(root, "config", "shearcast", "config.toml"),
		filepath.Join(root, "state", "shearcast", "state.json")
}

// A first channel add, run from anywhere with no path flags, creates the
// config and state under the XDG directories.
func TestChannelAddWithoutFlagsCreatesXDGFiles(t *testing.T) {
	configPath, statePath := xdgHome(t)
	t.Chdir(t.TempDir())

	if err := runChannel(context.Background(), []string{"add", "show", "-url", "https://youtube.com/@show", "-name", "A show"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Channels) != 1 || cfg.Channels[0].Slug != "show" {
		t.Fatalf("channels in %s: %+v", configPath, cfg.Channels)
	}
	if _, err := os.Stat(statePath + ".lock"); err != nil {
		t.Errorf("state lock not under the XDG state dir: %v", err)
	}
	if entries, _ := os.ReadDir("."); len(entries) != 0 {
		t.Errorf("wrote into the working directory: %v", entries)
	}
}

func TestSyncWithoutConfigSaysToRunInit(t *testing.T) {
	configPath, _ := xdgHome(t)
	err := runSync(context.Background(), []string{"-dry-run"})
	if err == nil || !strings.Contains(err.Error(), configPath) || !strings.Contains(err.Error(), "shearcast init") {
		t.Fatalf("got %v, want an error naming %s and suggesting shearcast init", err, configPath)
	}
}

// init writes a starter config and .env into the XDG config folder, keeps the
// .env private, and never overwrites either file.
func TestInitWritesStarterFilesOnce(t *testing.T) {
	configPath, _ := xdgHome(t)
	if err := runInit(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(configPath); err != nil {
		t.Fatalf("starter config does not load: %v", err)
	}
	envPath := filepath.Join(filepath.Dir(configPath), ".env")
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf(".env mode %v, want 0600", info.Mode().Perm())
	}

	edited := []byte("[[channels]]\nslug = 'mine'\n")
	if err := os.WriteFile(configPath, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runInit(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second init: got %v, want an already-exists error", err)
	}
	if got, _ := os.ReadFile(configPath); string(got) != string(edited) {
		t.Errorf("second init changed the config:\n%s", got)
	}
}

// doctor names the files this invocation uses, before any check that can fail,
// and still runs when there is no config yet.
func TestDoctorShowsPathsEvenWithoutConfig(t *testing.T) {
	configPath, statePath := xdgHome(t)
	cache := filepath.Join(t.TempDir(), "cache")
	var out bytes.Buffer
	err := doctorCommand(context.Background(), []string{"-cache", cache}, &out)
	if err == nil {
		t.Fatal("doctor passed with no config")
	}
	for _, want := range []string{configPath, statePath, cache, "shearcast init"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}
