package state

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotMissingAndLegacyStateNeverWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing", "state.json")
	snapshot, err := ReadSnapshot(path)
	if err != nil || snapshot.Exists || len(snapshot.Episodes) != 0 {
		t.Fatalf("empty snapshot: %+v, %v", snapshot, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("snapshot created files")
	}
	path = filepath.Join(dir, "legacy.json")
	legacy := []byte(`{"processed":{"show":["abcdefghijk"]}}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err = ReadSnapshot(path)
	if err != nil || !snapshot.Episodes["show"]["abcdefghijk"].HasPublished || len(snapshot.Sync) != 0 {
		t.Fatalf("legacy snapshot: %+v, %v", snapshot, err)
	}
	after, _ := os.ReadFile(path)
	entries, _ = os.ReadDir(dir)
	if !bytes.Equal(legacy, after) || len(entries) != 1 {
		t.Fatal("snapshot migrated state or created a lock")
	}
}

func TestSnapshotDuringWriterAndSyncHistoryAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	read := func() *Snapshot {
		t.Helper()
		snapshot, err := ReadSnapshot(path)
		check(err)
		return snapshot
	}
	check(s.StartSync([]string{"show", "other"}))
	check(s.StartChannelSync("show"))
	if got := read().Sync["show"].Latest; got.Result != "unfinished" || !got.FinishedAt.IsZero() {
		t.Fatalf("unfinished attempt: %+v", got)
	}
	check(s.FinishChannelSync("show", false, nil))
	success := read().Sync["show"].LastSuccess
	if success.IsZero() {
		t.Fatal("missing successful completion")
	}
	check(s.StartChannelSync("other"))
	check(s.FinishChannelSync("other", false, errors.New("listing failed")))
	check(s.FinishSync(errors.New("one channel failed")))
	check(s.Close())
	s, err = Open(path)
	check(err)
	check(s.StartSync([]string{"show"}))
	check(s.StartChannelSync("show"))
	check(s.FinishChannelSync("show", true, nil))
	check(s.FinishSync(nil))
	snapshot := read()
	if snapshot.Sync["show"].Latest.Result != "skipped" || !snapshot.Sync["show"].LastSuccess.Equal(success) || snapshot.Sync["other"].Latest.Error != "listing failed" || len(snapshot.LastRun.Channels) != 1 {
		t.Fatalf("history lost: %+v", snapshot)
	}
	check(s.StartChannelSync("show"))
	check(s.FinishChannelSync("show", false, errors.New("publication failed")))
	if !read().Sync["show"].LastSuccess.Equal(success) {
		t.Fatal("failure advanced success time")
	}
}

func TestSnapshotRejectsCorruptionWithoutCreatingLock(t *testing.T) {
	for _, data := range []string{`null`, `{`, `{"version":99}`, `{"episodes":{"show":{"id":{"stage":"bogus"}}}}`, `{"sync":{"show":{"latest":{"result":"success"}}}}`} {
		dir := t.TempDir()
		path := filepath.Join(dir, "state.json")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSnapshot(path); err == nil {
			t.Fatalf("accepted %s", data)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Fatal("failed snapshot created a lock")
		}
	}
}
