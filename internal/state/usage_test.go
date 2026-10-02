package state

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/cwebley/shearcast/internal/usage"
)

func TestUsageCheckpointsAreAtomicIdempotentAndPreserveHistoricalRates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(s.Save("show", "id", Episode{Stage: Pending}))
	old, _ := s.Episode("show", "id")
	check(s.StartSync([]string{"show"}))
	check(s.StartChannelSync("show"))
	check(s.BeginUsage("show", "id", "one", usage.JevModel, true))
	observed := usage.Summary{Attempts: 1, Calls: 1}
	input, output, cost := 1000000, 20, .05
	observed.Observe(&input, &output, &cost, usage.ModelRate(usage.JevModel))
	check(s.CheckpointUsage("show", "id", "one", observed, false))
	check(s.CheckpointUsage("show", "id", "one", observed, true))
	check(s.Save("show", "id", old)) // stale lifecycle value cannot erase usage
	snapshot, err := ReadSnapshot(path)
	check(err)
	if snapshot.LastRun.Usage.InputTokens != input || snapshot.Sync["show"].Latest.Usage.InputTokens != input || snapshot.Episodes["show"]["id"].Usage.Total.InputTokens != input {
		t.Fatal("checkpoint duplicated or lost usage")
	}
	check(s.FinishChannelSync("show", false, nil))
	check(s.FinishSync(nil))
	check(s.Close())
	s, err = Open(path)
	check(err)
	check(s.BeginUsage("show", "id", "two", usage.JevModel, false))
	newRate := *usage.ModelRate(usage.JevModel)
	newRate.InputPerMillion, newRate.VerifiedAt = .084, "2026-10-01"
	second := usage.Summary{Attempts: 1, Calls: 1}
	second.Observe(&input, &output, &cost, &newRate)
	check(s.CheckpointUsage("show", "id", "two", second, true))
	snapshot, err = ReadSnapshot(path)
	check(err)
	h := snapshot.Episodes["show"]["id"].Usage
	if h.Total.InputTokens != 2000000 || len(h.Total.Calculations) != 2 || h.Total.Calculations[0].USD != .042 || h.Total.Calculations[1].USD != .084 || h.Latest.Usage.Cost != .084 {
		t.Fatalf("historical rate changed: %+v", h)
	}
	if snapshot.LastRun.Usage.InputTokens != input || snapshot.Sync["show"].Latest.Usage.InputTokens != input {
		t.Fatal("manual processing changed completed sync")
	}
	// Returned values own their nested slices and cannot mutate store memory.
	ep, _ := s.Episode("show", "id")
	ep.Usage.Total.Calculations[0].USD = 100
	again, _ := s.Episode("show", "id")
	if again.Usage.Total.Calculations[0].USD != .042 {
		t.Fatal("history escaped by reference")
	}
}

func TestInterruptedUsageAndLegacyMigrationRemainUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := []byte(`{"version":4,"episodes":{"show":{"id":{"stage":"published"}}}}`)
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadSnapshot(path)
	if err != nil || snapshot.Episodes["show"]["id"].Usage != nil {
		t.Fatal("legacy usage invented", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, legacy) {
		t.Fatal("read migrated legacy state")
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(s.BeginUsage("show", "id", "interrupted", usage.JevModel, false))
	check(s.CheckpointUsage("show", "id", "interrupted", usage.Summary{Attempts: 1, Unresolved: 1}, false))
	check(s.Close())
	s, err = Open(path)
	check(err)
	check(s.BeginUsage("show", "id", "retry", usage.JevModel, false))
	check(s.CheckpointUsage("show", "id", "retry", usage.Summary{}, true))
	ep, _ := s.Episode("show", "id")
	if ep.Usage.Total.Unresolved != 1 || ep.Usage.InterruptedAttempts != 1 {
		t.Fatalf("interruption erased: %+v", ep.Usage)
	}
	if err := s.CheckpointUsage("show", "id", "interrupted", usage.Summary{}, true); err == nil {
		t.Fatal("accepted stale attempt")
	}
	data, _ := os.ReadFile(path)
	if !bytes.Contains(data, []byte(`"version": 6`)) {
		t.Fatal("state not upgraded")
	}
}
