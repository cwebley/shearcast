package state

import (
	"fmt"
	"maps"
	"time"

	"github.com/cwebley/shearcast/internal/usage"
)

// SyncAttempt describes recorded work, not process liveness. An absent finish
// means completion was not recorded, even if the process has since exited.
type SyncAttempt struct {
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt time.Time      `json:"finished_at,omitempty"`
	Result     string         `json:"result"` // unfinished, success, failed or skipped
	Error      string         `json:"error,omitempty"`
	Usage      *usage.Summary `json:"usage,omitempty"`
}

type SyncHistory struct {
	Latest      SyncAttempt `json:"latest"`
	LastSuccess time.Time   `json:"last_success,omitempty"`
}

type SyncRun struct {
	SyncAttempt
	Channels []string `json:"channels"`
}

// Snapshot owns a detached copy of the last atomically saved document. Reading
// a missing library creates nothing and reading a busy library takes no lock.
type Snapshot struct {
	Exists     bool
	Publishing *PublishingDestination
	Episodes   map[string]map[string]Episode
	Purges     map[string]bool
	Sync       map[string]SyncHistory
	LastRun    *SyncRun
}

func ReadSnapshot(path string) (*Snapshot, error) {
	doc, exists, err := readDocument(path)
	if err != nil {
		return nil, err
	}
	return &Snapshot{Exists: exists, Publishing: doc.Publishing, Episodes: doc.Episodes,
		Purges: doc.Purges, Sync: doc.Sync, LastRun: doc.LastRun}, nil
}

func (s *Snapshot) CheckPublishing(destination PublishingDestination) error {
	return checkPublishing(document{Publishing: s.Publishing, Episodes: s.Episodes}, destination)
}

func (s *Store) StartSync(channels []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.doc
	next.LastRun = &SyncRun{SyncAttempt: startedAttempt(), Channels: append([]string(nil), channels...)}
	return s.writeLocked(next)
}

func (s *Store) FinishSync(runErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.doc.LastRun == nil {
		return fmt.Errorf("no sync invocation started")
	}
	next := s.doc
	run := *next.LastRun
	run.SyncAttempt = finishedAttempt(run.SyncAttempt, false, runErr)
	next.LastRun = &run
	return s.writeLocked(next)
}

func (s *Store) StartChannelSync(channel string) error {
	return s.updateSync(channel, func(history SyncHistory) SyncHistory {
		history.Latest = startedAttempt()
		return history
	})
}

// Successful disabled-channel recovery is skipped for sync freshness purposes.
// A recovery error still records failure. Waiting captions are not an error.
func (s *Store) FinishChannelSync(channel string, skipped bool, runErr error) error {
	return s.updateSync(channel, func(history SyncHistory) SyncHistory {
		history.Latest = finishedAttempt(history.Latest, skipped, runErr)
		if history.Latest.Result == "success" {
			history.LastSuccess = history.Latest.FinishedAt
		}
		return history
	})
}

func (s *Store) updateSync(channel string, update func(SyncHistory) SyncHistory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.doc
	next.Sync = maps.Clone(s.doc.Sync)
	if next.Sync == nil {
		next.Sync = map[string]SyncHistory{}
	}
	next.Sync[channel] = update(next.Sync[channel])
	return s.writeLocked(next)
}

func startedAttempt() SyncAttempt {
	return SyncAttempt{StartedAt: time.Now().UTC(), Result: "unfinished", Usage: &usage.Summary{}}
}

func finishedAttempt(attempt SyncAttempt, skipped bool, err error) SyncAttempt {
	attempt.FinishedAt = time.Now().UTC()
	attempt.Result, attempt.Error = "success", ""
	if skipped {
		attempt.Result = "skipped"
	}
	if err != nil {
		attempt.Result, attempt.Error = "failed", err.Error()
	}
	return attempt
}

func validateSync(doc document) error {
	validate := func(a SyncAttempt) error {
		if a.StartedAt.IsZero() {
			return fmt.Errorf("missing sync start time")
		}
		switch a.Result {
		case "unfinished":
			if !a.FinishedAt.IsZero() {
				return fmt.Errorf("unfinished sync has a completion time")
			}
		case "success", "failed", "skipped":
			if a.FinishedAt.IsZero() {
				return fmt.Errorf("finished sync has no completion time")
			}
		default:
			return fmt.Errorf("unknown sync result %q", a.Result)
		}
		return nil
	}
	if doc.LastRun != nil {
		if err := validate(doc.LastRun.SyncAttempt); err != nil {
			return err
		}
	}
	for channel, history := range doc.Sync {
		if err := validate(history.Latest); err != nil {
			return fmt.Errorf("%s: %w", channel, err)
		}
	}
	return nil
}
