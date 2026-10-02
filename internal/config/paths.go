package config

import (
	"os"
	"path/filepath"
)

// DefaultConfigPath is where commands look for config.toml when no -config
// flag is given: SHEARCAST_CONFIG, then the XDG config directory.
func DefaultConfigPath() string {
	if path := os.Getenv("SHEARCAST_CONFIG"); path != "" {
		return path
	}
	return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), "shearcast", "config.toml")
}

// DefaultStatePath is where commands keep state.json when no -state flag is
// given: the XDG state directory.
func DefaultStatePath() string {
	return filepath.Join(xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state")), "shearcast", "state.json")
}

// xdgDir returns the XDG base directory named by env, or its default under the
// home directory. The spec says relative values are invalid and ignored.
func xdgDir(env, fallback string) string {
	if dir := os.Getenv(env); filepath.IsAbs(dir) {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fallback
	}
	return filepath.Join(home, fallback)
}
