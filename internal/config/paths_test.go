package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPathsFollowXDG(t *testing.T) {
	t.Setenv("SHEARCAST_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	if got, want := DefaultConfigPath(), "/xdg/config/shearcast/config.toml"; got != want {
		t.Errorf("config: got %s, want %s", got, want)
	}
	if got, want := DefaultStatePath(), "/xdg/state/shearcast/state.json"; got != want {
		t.Errorf("state: got %s, want %s", got, want)
	}
}

func TestDefaultPathsFallBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHEARCAST_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	if got, want := DefaultConfigPath(), filepath.Join(home, ".config/shearcast/config.toml"); got != want {
		t.Errorf("config: got %s, want %s", got, want)
	}
	if got, want := DefaultStatePath(), filepath.Join(home, ".local/state/shearcast/state.json"); got != want {
		t.Errorf("state: got %s, want %s", got, want)
	}
}

// XDG requires absolute paths; a relative value is ignored.
func TestRelativeXDGIsIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHEARCAST_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if got, want := DefaultConfigPath(), filepath.Join(home, ".config/shearcast/config.toml"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestShearcastConfigOverridesXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/config")
	t.Setenv("SHEARCAST_CONFIG", "/elsewhere/mine.toml")
	if got, want := DefaultConfigPath(), "/elsewhere/mine.toml"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// Paths inside the config are relative to the config's folder, so a command
// run from any directory finds the same files.
func TestConfiguredWeightsResolveFromConfigFolder(t *testing.T) {
	t.Chdir(t.TempDir())
	path := writeConfig(t, "[jev]\nweights = \"fits/weights.json\"\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(filepath.Dir(path), "fits", "weights.json"); cfg.Jev.Weights != want {
		t.Errorf("got %s, want %s", cfg.Jev.Weights, want)
	}
}

// With no weights configured, weights.json beside the config is used if it
// exists. A fresh install has none and runs on the plain predicate.
func TestDefaultWeightsAreOptional(t *testing.T) {
	path := writeConfig(t, "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Jev.Weights != "" {
		t.Errorf("no weights file: got %q, want none", cfg.Jev.Weights)
	}

	beside := filepath.Join(filepath.Dir(path), "weights.json")
	if err := os.WriteFile(beside, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg, err = Load(path); err != nil {
		t.Fatal(err)
	}
	if cfg.Jev.Weights != beside {
		t.Errorf("weights beside config: got %q, want %s", cfg.Jev.Weights, beside)
	}
}
