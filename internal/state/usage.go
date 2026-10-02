package state

import (
	"fmt"
	"maps"
	"time"

	"github.com/cwebley/shearcast/internal/usage"
)

func withEpisode(doc document, channel, id string, ep Episode) document {
	doc.Episodes = maps.Clone(doc.Episodes)
	entries := maps.Clone(doc.Episodes[channel])
	if entries == nil {
		entries = map[string]Episode{}
	}
	entries[id] = ep
	doc.Episodes[channel] = entries
	return doc
}

// BeginUsage creates a processing checkpoint before any model request. Sync
// links identify the exact invocation, so later manual work cannot affect it.
func (s *Store) BeginUsage(channel, id, attemptID, model string, syncing bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ep, ok := s.doc.Episodes[channel][id]
	if !ok || attemptID == "" {
		return fmt.Errorf("usage requires an episode and attempt id")
	}
	now := time.Now().UTC()
	h := ep.Usage.Clone()
	if h == nil {
		h = &usage.History{Since: now}
	}
	if h.Latest.ID == attemptID {
		return fmt.Errorf("usage attempt already started")
	}
	if h.Latest.ID != "" && h.Latest.FinishedAt.IsZero() {
		h.InterruptedAttempts++
	}
	h.Latest = usage.Attempt{ID: attemptID, Model: model, StartedAt: now}
	if syncing {
		if history, ok := s.doc.Sync[channel]; ok && history.Latest.Result == "unfinished" {
			h.Latest.ChannelStartedAt = history.Latest.StartedAt
		}
		if run := s.doc.LastRun; run != nil && run.Result == "unfinished" {
			h.Latest.SyncStartedAt = run.StartedAt
		}
	}
	ep.Usage = h
	return s.writeLocked(withEpisode(s.doc, channel, id, ep))
}

// CheckpointUsage replaces this attempt's absolute subtotal and applies only
// its delta to episode and sync totals, in one atomic document replacement.
func (s *Store) CheckpointUsage(channel, id, attemptID string, observed usage.Summary, finished bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ep := s.doc.Episodes[channel][id]
	h := ep.Usage.Clone()
	if h == nil || h.Latest.ID != attemptID {
		return fmt.Errorf("usage attempt does not match episode")
	}
	delta := observed.Since(h.Latest.Usage)
	h.Latest.Usage = observed.Clone()
	h.Total = h.Total.Add(delta)
	if finished {
		h.Latest.FinishedAt = time.Now().UTC()
	}
	ep.Usage = h
	next := withEpisode(s.doc, channel, id, ep)
	if !h.Latest.ChannelStartedAt.IsZero() {
		history := next.Sync[channel]
		if history.Latest.StartedAt.Equal(h.Latest.ChannelStartedAt) && history.Latest.Result == "unfinished" {
			total := usage.Summary{}
			if history.Latest.Usage != nil {
				total = *history.Latest.Usage
			}
			total = total.Add(delta)
			history.Latest.Usage = &total
			next.Sync = maps.Clone(next.Sync)
			next.Sync[channel] = history
		}
	}
	if run := next.LastRun; run != nil && !h.Latest.SyncStartedAt.IsZero() && run.StartedAt.Equal(h.Latest.SyncStartedAt) && run.Result == "unfinished" {
		copy := *run
		total := usage.Summary{}
		if copy.Usage != nil {
			total = *copy.Usage
		}
		total = total.Add(delta)
		copy.Usage = &total
		next.LastRun = &copy
	}
	return s.writeLocked(next)
}
