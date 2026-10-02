// Package state holds episode checkpoints and a process-level writer lock.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cwebley/shearcast/internal/chapters"
	"github.com/cwebley/shearcast/internal/fileutil"
	"github.com/cwebley/shearcast/internal/usage"
	"github.com/cwebley/shearcast/internal/youtube"
)

type Stage string

const (
	Pending   Stage = "pending"
	Waiting   Stage = "waiting_captions"
	Rendered  Stage = "rendered"
	Published Stage = "published"
)

// LastError describes a retryable failure without discarding the last stage.
type Episode struct {
	PublishedAudioKey    string             `json:"published_audio_key,omitempty"`
	PublishedAudioSHA256 string             `json:"published_audio_sha256,omitempty"`
	PendingPublication   *Publication       `json:"pending_publication,omitempty"`
	AudioKeys            []string           `json:"audio_keys,omitempty"`
	ChaptersEnabled      bool               `json:"chapters_enabled,omitempty"` // only publications made by chapter-aware builds
	PublishedTimeline    *chapters.Timeline `json:"published_timeline,omitempty"`
	ChapterKeys          []string           `json:"chapter_keys,omitempty"` // includes staged revisions until feed-aware cleanup
	Stage                Stage              `json:"stage"`
	HasPublished         bool               `json:"has_published,omitempty"`
	PublishPending       bool               `json:"publish_pending,omitempty"`
	ReprocessPending     bool               `json:"reprocess_pending,omitempty"`
	Removal              string             `json:"removal,omitempty"` // excluded or pruned; independent of history
	DeletePending        bool               `json:"delete_pending,omitempty"`
	Video                *youtube.Video     `json:"video,omitempty"`
	RenderPath           string             `json:"render_path,omitempty"`
	RecordPath           string             `json:"record_path,omitempty"` // private sidecar when audio lives in publication storage
	RenderID             string             `json:"render_id,omitempty"`
	AudioSHA256          string             `json:"audio_sha256,omitempty"`
	AudioBytes           int64              `json:"audio_bytes,omitempty"`
	DurationSeconds      float64            `json:"duration_seconds,omitempty"`
	AudioURL             string             `json:"audio_url,omitempty"`
	FeedURL              string             `json:"feed_url,omitempty"`
	UpdatedAt            time.Time          `json:"updated_at"`
	LastError            string             `json:"last_error,omitempty"`
	Usage                *usage.History     `json:"usage,omitempty"`
}

// Publication journals the timeline associated with a staged audio revision.
// Recovery checks RSS before adopting it, including a lost feed-write response.
type Publication struct {
	ID          string             `json:"id"`
	AudioKey    string             `json:"audio_key"`
	AudioSHA256 string             `json:"audio_sha256"`
	Timeline    *chapters.Timeline `json:"timeline,omitempty"`
}

// AdoptPublication moves a feed-confirmed revision into the published snapshot.
// Processing stage and unfinished-work flags belong to the lifecycle caller.
func (e *Episode) AdoptPublication(p Publication) {
	e.PublishedAudioKey = p.AudioKey
	e.PublishedAudioSHA256 = p.AudioSHA256
	e.PublishedTimeline = p.Timeline.Clone()
	e.ChaptersEnabled, e.HasPublished = true, true
}

type document struct {
	Publishing *PublishingDestination        `json:"publishing,omitempty"`
	Version    int                           `json:"version"`
	Processed  map[string][]string           `json:"processed,omitempty"`
	Episodes   map[string]map[string]Episode `json:"episodes"`
	Purges     map[string]bool               `json:"purges,omitempty"`
	Sync       map[string]SyncHistory        `json:"sync,omitempty"`
	LastRun    *SyncRun                      `json:"last_run,omitempty"`
}

// PublishingDestination is the identity of one library's published storage.
// Location is a canonical directory for filesystem storage or account/bucket
// for R2. URLs alone cannot identify the storage behind a custom domain.
type PublishingDestination struct {
	Backend  string `json:"backend"`
	Location string `json:"location"`
	BaseURL  string `json:"base_url"`
}

// BindPublishing prevents accidental destination changes while preserving
// legacy R2 state. A dry run checks the binding without writing it.
func (s *Store) BindPublishing(destination PublishingDestination, readOnly bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkPublishing(s.doc, destination); err != nil {
		return err
	}
	if readOnly || s.doc.Publishing != nil {
		return nil
	}
	next := s.doc
	next.Publishing = &destination
	return s.writeLocked(next)
}

func checkPublishing(doc document, destination PublishingDestination) error {
	if doc.Publishing != nil {
		if *doc.Publishing != destination {
			return fmt.Errorf("publishing destination changed; restore the previous backend, directory and base URL, or use separate state/cache for a new library; existing libraries require an explicit migration")
		}
		return nil
	}
	for channel, episodes := range doc.Episodes {
		for _, ep := range episodes {
			if ep.FeedURL != "" && ep.FeedURL != strings.TrimRight(destination.BaseURL, "/")+"/"+channel+"/feed.xml" {
				return fmt.Errorf("publishing base URL differs from existing %s history; migrate the library explicitly or use separate state/cache", channel)
			}
			if destination.Backend == "filesystem" && (ep.HasPublished || ep.Stage == Published || ep.DeletePending) {
				return fmt.Errorf("legacy published history belongs to R2; use separate state/cache for local publishing or migrate the library explicitly")
			}
		}
	}
	return nil
}

type Store struct {
	mu   sync.Mutex
	path string
	lock *os.File
	doc  document
}

var ErrLocked = errors.New("another shearcast command holds the state lock")

// Open acquires the writer lock before reading state. Call Close when finished.
// Legacy processed IDs become published checkpoints in memory. The original
// file is not rewritten until the first successful mutation.
func Open(path string) (*Store, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := fileutil.MkdirAll(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		dir, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		path = filepath.Join(dir, filepath.Base(path))
	}
	lock, err := acquireLock(path + ".lock")
	if err != nil {
		return nil, err
	}
	doc, _, err := readDocument(path)
	if err != nil {
		lock.Close()
		return nil, err
	}
	return &Store{path: path, lock: lock, doc: doc}, nil
}

const currentVersion = 6

// Readers and writers use exactly the same validation and legacy interpretation.
func readDocument(path string) (document, bool, error) {
	doc := document{Version: currentVersion, Episodes: map[string]map[string]Episode{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return doc, false, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &doc)
	}
	if err == nil && (doc.Version < 0 || doc.Version > currentVersion) {
		err = fmt.Errorf("unsupported state version %d", doc.Version)
	}
	if err != nil {
		return document{}, true, err
	}
	if strings.TrimSpace(string(data)) == "null" {
		return document{}, true, fmt.Errorf("state must be a JSON object")
	}
	doc.Version = currentVersion
	if doc.Episodes == nil {
		doc.Episodes = map[string]map[string]Episode{}
	}
	for channel, ids := range doc.Processed {
		if doc.Episodes[channel] == nil {
			doc.Episodes[channel] = map[string]Episode{}
		}
		for _, id := range ids {
			if _, exists := doc.Episodes[channel][id]; !exists {
				doc.Episodes[channel][id] = Episode{Stage: Published, HasPublished: true}
			}
		}
	}
	for channel, entries := range doc.Episodes {
		for id, e := range entries {
			switch e.Stage {
			case Pending, Waiting, Rendered, Published:
			default:
				return document{}, true, fmt.Errorf("unknown episode stage %q for %s/%s", e.Stage, channel, id)
			}
			if e.Stage == Published {
				e.HasPublished = true
				doc.Episodes[channel][id] = e
			}
		}
	}
	if err := validateSync(doc); err != nil {
		return document{}, true, err
	}
	return doc, true, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close()
	s.lock = nil
	return err
}

func (s *Store) Episode(channel, id string) (Episode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.doc.Episodes[channel][id]
	e.Usage = e.Usage.Clone()
	e = cloneMetadata(e)
	return e, ok
}

func (s *Store) Episodes(channel string) map[string]Episode {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Episode, len(s.doc.Episodes[channel]))
	for id, e := range s.doc.Episodes[channel] {
		e.Usage = e.Usage.Clone()
		e = cloneMetadata(e)
		out[id] = e
	}
	return out
}

func (s *Store) Save(channel, id string, episode Episode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return fmt.Errorf("state store is closed")
	}
	switch episode.Stage {
	case Pending, Waiting, Rendered, Published:
	default:
		return fmt.Errorf("invalid episode stage %q", episode.Stage)
	}
	episode.UpdatedAt = time.Now().UTC()
	previous := s.doc.Episodes[channel][id]
	// Accounting checkpoints can advance while lifecycle code holds an older
	// episode value. Only the accounting methods may replace this history.
	episode.Usage = previous.Usage
	if previous.HasPublished || previous.Stage == Published || episode.Stage == Published {
		episode.HasPublished = true
	}
	episode = cloneMetadata(episode)
	return s.writeLocked(withEpisode(s.doc, channel, id, episode))
}

// Forget drops an episode record. Callers decide that nothing in it (publication
// history, artifacts, usage) is still needed.
func (s *Store) Forget(channel, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.doc.Episodes[channel][id]; !ok {
		return nil
	}
	next := s.doc
	next.Episodes = maps.Clone(s.doc.Episodes)
	entries := maps.Clone(s.doc.Episodes[channel])
	delete(entries, id)
	next.Episodes[channel] = entries
	return s.writeLocked(next)
}

func cloneMetadata(e Episode) Episode {
	e.Video = e.Video.Clone()
	e.PublishedTimeline = e.PublishedTimeline.Clone()
	e.ChapterKeys = append([]string(nil), e.ChapterKeys...)
	e.AudioKeys = append([]string(nil), e.AudioKeys...)
	if e.PendingPublication != nil {
		p := *e.PendingPublication
		p.Timeline = p.Timeline.Clone()
		e.PendingPublication = &p
	}
	return e
}

func (s *Store) PurgePending(channel string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doc.Purges[channel]
}

func (s *Store) SetPurgePending(channel string, pending bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.doc
	next.Purges = make(map[string]bool, len(s.doc.Purges)+1)
	for ch, value := range s.doc.Purges {
		next.Purges[ch] = value
	}
	if pending {
		next.Purges[channel] = true
	} else {
		delete(next.Purges, channel)
	}
	return s.writeLocked(next)
}

func (s *Store) writeLocked(next document) error {
	if s.lock == nil {
		return fmt.Errorf("state store is closed")
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := fileutil.WriteAtomic(s.path, data, 0o600); err != nil {
		return err
	}
	s.doc = next
	return nil
}

func (s *Store) IsProcessed(channel, id string) bool {
	e, ok := s.Episode(channel, id)
	return ok && (e.HasPublished || e.Stage == Published)
}

func (s *Store) MarkProcessed(channel, id string) error {
	e, _ := s.Episode(channel, id)
	e.Stage = Published
	e.HasPublished = true
	return s.Save(channel, id, e)
}
