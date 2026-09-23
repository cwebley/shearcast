// Package state tracks which videos have already been processed for each
// channel, so `sync` never re-renders or re-uploads an episode it already
// published. The whole store is a handful of channels times a few dozen
// episodes, so a flat JSON file is proportionate; nothing here needs a real
// database.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Store is a file-backed record of processed video ids, keyed by channel slug.
type Store struct {
	path string

	mu        sync.Mutex
	Processed map[string][]string `json:"processed"`
}

// Open loads path, or starts empty if it does not exist yet.
func Open(path string) (*Store, error) {
	s := &Store{path: path, Processed: map[string][]string{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	if s.Processed == nil {
		s.Processed = map[string][]string{}
	}
	return s, nil
}

// IsProcessed reports whether videoID has already been published for channel.
func (s *Store) IsProcessed(channel, videoID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.Processed[channel] {
		if id == videoID {
			return true
		}
	}
	return false
}

// MarkProcessed records videoID as published for channel and persists the
// change immediately, so a crash partway through a sync run does not lose
// track of episodes already uploaded.
func (s *Store) MarkProcessed(channel, videoID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Processed[channel] = append(s.Processed[channel], videoID)
	return s.writeLocked()
}

func (s *Store) writeLocked() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(s.path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	// Write then rename, so a crash mid-write never leaves a truncated file
	// that later reads as an empty or corrupt store.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
