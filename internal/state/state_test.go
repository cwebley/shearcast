package state

import (
	"path/filepath"
	"testing"
)

func TestOpenMissingFileStartsEmpty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if s.IsProcessed("hotu", "abc") {
		t.Error("empty store reports a hit")
	}
}

func TestMarkProcessedPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.MarkProcessed("hotu", "abc123"); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	if !reopened.IsProcessed("hotu", "abc123") {
		t.Error("mark did not persist across reopen")
	}
	if reopened.IsProcessed("hotu", "other") {
		t.Error("false positive for an unmarked id")
	}
	if reopened.IsProcessed("primetime", "abc123") {
		t.Error("marks must be scoped per channel, not global")
	}
}

func TestMarkProcessedTwiceIsHarmless(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	s.MarkProcessed("hotu", "abc")
	s.MarkProcessed("hotu", "abc")
	if !s.IsProcessed("hotu", "abc") {
		t.Error("expected abc to be marked processed")
	}
}
