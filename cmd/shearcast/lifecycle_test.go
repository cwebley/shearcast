package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cwebley/shearcast/internal/state"
)

func TestMutatingCommandsShareStateLock(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[[channels]]\nslug = 'show'\nurl = 'https://www.youtube.com/@example'\nno_weights = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	st, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, tc := range []struct {
		name   string
		run    func(context.Context, []string) error
		target bool
	}{
		{"render", runRender, true}, {"publish", runPublish, true}, {"sync", runSync, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"-config", configPath, "-state", path, "-channel", "show", "-cache", filepath.Join(dir, "cache")}
			if tc.target {
				args = append(args, "abcdefghijk")
			}
			if err := tc.run(context.Background(), args); !errors.Is(err, state.ErrLocked) {
				t.Fatalf("got %v, want state lock error", err)
			}
		})
	}
}
