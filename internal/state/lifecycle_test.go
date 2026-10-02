package state

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLegacyStateMigratesWithoutLosingHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := []byte(`{"processed":{"a":["one","two","one"],"b":["one"]}}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, legacy) {
		t.Fatalf("Open changed original state: %s, %v", data, err)
	}
	if !s.IsProcessed("a", "two") || !s.IsProcessed("b", "one") {
		t.Fatal("legacy history missing")
	}
	if err := s.Save("a", "new", Episode{Stage: Rendered, RenderPath: "/local/output.m4a", LastError: "upload failed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.IsProcessed("a", "one") || !s.IsProcessed("a", "two") || !s.IsProcessed("b", "one") {
		t.Fatal("migration lost published history")
	}
	e, ok := s.Episode("a", "new")
	if !ok || e.Stage != Rendered || e.LastError != "upload failed" {
		t.Fatalf("checkpoint lost: %+v", e)
	}
}

func TestStateLockExcludesOtherProcessesAndReleasesOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	check := func(want string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestStateLockChild$")
		cmd.Env = append(os.Environ(), "SHEARCAST_LOCK_TEST="+path, "SHEARCAST_LOCK_WANT="+want)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child lock check: %v\n%s", err, out)
		}
	}
	check("locked")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	check("open")
	check("open") // child process exit also releases its lock
}

func TestStateLockChild(t *testing.T) {
	path := os.Getenv("SHEARCAST_LOCK_TEST")
	if path == "" {
		return
	}
	s, err := Open(path)
	if os.Getenv("SHEARCAST_LOCK_WANT") == "locked" {
		if !errors.Is(err, ErrLocked) {
			t.Fatalf("got %v, want locked", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = s // deliberately leave open: process exit must release the lock
}

func TestFailedSaveDoesNotAdvanceMemoryCheckpoint(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Save("a", "id", Episode{Stage: Rendered}); err != nil {
		t.Fatal(err)
	}
	s.path = t.TempDir() // cannot replace a directory with a JSON file
	if err := s.Save("a", "id", Episode{Stage: Published}); err == nil {
		t.Fatal("expected persistence failure")
	}
	e, _ := s.Episode("a", "id")
	if e.Stage != Rendered {
		t.Fatalf("failed save advanced stage: %s", e.Stage)
	}
}

func TestUnknownStateVersionIsPreservedAndUnlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	data := []byte(`{"version":99,"episodes":{}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("accepted future state")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, got) {
		t.Fatal("future state changed")
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}
