package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/cwebley/shearcast/internal/chapters"
	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/storage"
	"github.com/cwebley/shearcast/internal/youtube"
)

type chapterMetadata struct {
	publicationID string
	description   string
	list          []chapters.Chapter
	url           string
}

// Publication uses only a record already verified by recoverRender. Absence of
// a timeline is allowed for caller-owned imports, but never implies no cuts.
func publicationTimeline(ep state.Episode, channel, id string) (*chapters.Timeline, error) {
	data, err := os.ReadFile(recordPath(ep.RenderPath, ep))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec RenderRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	if rec.VideoID != id || rec.Channel != channel || rec.AudioSHA256 == "" || rec.AudioSHA256 != ep.AudioSHA256 || rec.AudioBytes != ep.AudioBytes {
		// Legacy sidecars without checksums cannot establish timeline provenance.
		return nil, nil
	}
	if len(rec.Keep) == 0 || rec.Source == nil {
		return nil, nil
	}
	sourceDuration := rec.SourceDurationSeconds
	if sourceDuration == 0 {
		sourceDuration = rec.Source.Duration
	}
	timeline := &chapters.Timeline{Keep: rec.Keep, Crossfade: rec.Cut.Crossfade, SourceDuration: sourceDuration, Duration: rec.DurationSeconds}
	if err := timeline.Validate(); err != nil {
		if ep.Video == nil || len(ep.Video.Chapters) == 0 {
			return nil, nil
		}
		return nil, err
	}
	return timeline, nil
}

// Journal every revision before uploading it. A failed feed write leaves the
// previous revision intact; recovery deletes only objects not referenced by RSS.
func (r *Runner) prepareChapters(ctx context.Context, ch config.Channel, video *youtube.Video, timeline *chapters.Timeline, ep *state.Episode) (*chapterMetadata, error) {
	description, list, err := chapters.Derive(video.Description, video.Chapters, timeline)
	if err != nil {
		return nil, err
	}
	metadata := &chapterMetadata{description: description, list: list}
	if len(list) == 0 || !strings.HasPrefix(r.Publisher.PublicURL(ch.Slug+"/feed.xml"), "https://") {
		return metadata, nil
	}
	data, err := chapters.JSON(list)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s/%s.%x.chapters.json", ch.Slug, video.ID, sha256.Sum256(data))
	if !slices.Contains(ep.ChapterKeys, key) {
		ep.ChapterKeys = append(ep.ChapterKeys, key)
		if err := r.State.Save(ch.Slug, video.ID, *ep); err != nil {
			return nil, err
		}
	}
	url, err := r.Publisher.Put(ctx, key, bytes.NewReader(data), int64(len(data)), chapters.ContentType)
	if err != nil {
		return nil, fmt.Errorf("publishing chapters: %w", err)
	}
	metadata.url, metadata.list = url, nil
	return metadata, nil
}

func (r *Runner) cleanupRevisions(ctx context.Context, ch config.Channel, id string, ep *state.Episode) error {
	if len(ep.ChapterKeys) == 0 && len(ep.AudioKeys) == 0 {
		return nil
	}
	// A revision is obsolete only when a feed exists and does not reference
	// it. A missing feed proves nothing about an established library, so
	// without one only a recorded removal may delete media.
	fd := &feed.Feed{}
	data, err := r.Publisher.Get(ctx, ch.Slug+"/feed.xml")
	switch {
	case errors.Is(err, storage.ErrNotFound):
		if ep.Removal == "" {
			return nil
		}
	case err != nil:
		return fmt.Errorf("fetching existing feed: %w", err)
	default:
		if fd, err = feed.Parse(data); err != nil {
			return err
		}
	}
	live := map[string]bool{}
	for _, item := range fd.Items {
		live[item.ChaptersURL] = true
		live[item.AudioURL] = true
	}
	filter := func(keys []string, valid func(string) bool) ([]string, error) {
		var retained []string
		for _, key := range keys {
			if !valid(key) || !strings.HasPrefix(key, ch.Slug+"/"+id+".") {
				return nil, fmt.Errorf("invalid managed revision key %q", key)
			}
			pending := ep.Removal == "" && ep.PendingPublication != nil && ep.PendingPublication.AudioKey == key
			if live[r.Publisher.PublicURL(key)] || pending {
				retained = append(retained, key)
				continue
			}
			if err := r.Publisher.Delete(ctx, key); err != nil {
				return nil, fmt.Errorf("removing obsolete revision: %w", err)
			}
		}
		return retained, nil
	}
	chapterKeys, err := filter(ep.ChapterKeys, storage.IsChapterKey)
	if err != nil {
		return err
	}
	audioKeys, err := filter(ep.AudioKeys, storage.IsAudioKey)
	if err != nil {
		return err
	}
	if slices.Equal(chapterKeys, ep.ChapterKeys) && slices.Equal(audioKeys, ep.AudioKeys) {
		return nil
	}
	ep.ChapterKeys, ep.AudioKeys = chapterKeys, audioKeys
	return r.State.Save(ch.Slug, id, *ep)
}

func (r *Runner) reconcilePublication(ctx context.Context, ch config.Channel, id string, ep *state.Episode) error {
	if ep.PendingPublication == nil {
		return nil
	}
	fd, err := loadOrCreateFeed(ctx, r.Publisher, ch, ch.Slug+"/feed.xml")
	if err != nil {
		return err
	}
	for _, item := range fd.Items {
		if item.ID == id && ep.PendingPublication.ID != "" && item.PublicationID == ep.PendingPublication.ID && item.AudioURL == r.Publisher.PublicURL(ep.PendingPublication.AudioKey) {
			ep.AdoptPublication(*ep.PendingPublication)
			ep.AudioURL = item.AudioURL
			return r.State.Save(ch.Slug, id, *ep)
		}
	}
	return nil
}
