package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/fileutil"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/youtube"
)

const (
	Excluded = "excluded"
	Pruned   = "pruned"
)

var ErrSourceOrder = errors.New("same-day source publication order is unavailable")

// RemoveEpisode saves the exclusion before changing the feed or deleting audio.
// Recovery repeats the operation after any failure, including a failed state save
// after deletion. Small processing records and publication history survive.
func (r *Runner) RemoveEpisode(ctx context.Context, channel config.Channel, target string) error {
	id := youtube.VideoID(target)
	if id == "" {
		return fmt.Errorf("need a YouTube video URL or eleven-character id")
	}
	return r.removeEpisode(ctx, channel, id, Excluded)
}

func (r *Runner) removeEpisode(ctx context.Context, channel config.Channel, id, reason string) error {
	if !safeChannel(channel.Slug) || youtube.VideoID(id) != id {
		return fmt.Errorf("invalid channel or episode id")
	}
	if r.Publisher == nil || r.State == nil {
		return fmt.Errorf("state and publishing storage are required")
	}
	ep, _ := r.State.Episode(channel.Slug, id)
	if ep.Stage == "" {
		ep.Stage = state.Pending
	}
	if ep.Removal == Excluded {
		reason = Excluded
	}
	ep.Removal, ep.DeletePending, ep.PublishPending = reason, true, false
	ep.ReprocessPending = false
	if err := r.State.Save(channel.Slug, id, ep); err != nil {
		return err
	}
	return r.finishRemoval(ctx, channel, id, ep)
}

func (r *Runner) finishRemoval(ctx context.Context, channel config.Channel, id string, ep state.Episode) (err error) {
	defer func() {
		if err != nil {
			ep.LastError = err.Error()
			err = errors.Join(err, r.State.Save(channel.Slug, id, ep))
		}
	}()
	if r.Publisher == nil {
		return fmt.Errorf("publishing storage is required to finish removal")
	}
	key := channel.Slug + "/feed.xml"
	fd, err := loadOrCreateFeed(ctx, r.Publisher, channel, key)
	if err != nil {
		return err
	}
	items := fd.Items[:0]
	found := false
	for _, item := range fd.Items {
		if item.ID == id {
			found = true
			ep.HasPublished = true
			continue
		}
		items = append(items, item)
	}
	if found {
		// Save legacy feed-only publication history before removing its entry.
		if err := r.State.Save(channel.Slug, id, ep); err != nil {
			return err
		}
		fd.Items = items
		if err := putFeed(ctx, r.Publisher, channel, fd); err != nil {
			return err
		}
	}
	// No live feed may point at an object we are about to delete.
	if err := r.cleanupRevisions(ctx, channel, id, &ep); err != nil {
		return err
	}
	if err := r.Publisher.Delete(ctx, channel.Slug+"/"+id+".m4a"); err != nil {
		return err
	}
	if err := r.removeManagedAudio(channel, id); err != nil {
		return err
	}
	ep.DeletePending, ep.LastError = false, ""
	ep.PendingPublication = nil
	return r.State.Save(channel.Slug, id, ep)
}

func putFeed(ctx context.Context, publisher Publisher, channel config.Channel, fd *feed.Feed) error {
	data, err := fd.XML()
	if err != nil {
		return err
	}
	_, err = publisher.Put(ctx, channel.Slug+"/feed.xml", strings.NewReader(string(data)), int64(len(data)), "application/rss+xml")
	return err
}

func (r *Runner) removeManagedAudio(channel config.Channel, id string) error {
	path, err := fileutil.CanonicalPath(RenderPath(r.Cache, channel, id))
	if err != nil {
		return err
	}
	// Complete a durable install before deleting it, so its journal can never
	// resurrect audio. Explicit imports and custom output files are caller-owned.
	if err := r.prepareArtifact(path, id); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := fileutil.SyncDir(filepath.Dir(path)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return r.Cache.RemoveAudio(id)
}

// EnforceRetention uses source dates in the feed, including legacy entries that
// have no detailed state record. Publication order has no bearing on retention.
func (r *Runner) EnforceRetention(ctx context.Context, channel config.Channel) error {
	if channel.RetentionLimit() <= 0 {
		return fmt.Errorf("keep must be positive")
	}
	if err := r.recoverRemovals(ctx, channel); err != nil {
		return err
	}
	fd, err := loadOrCreateFeed(ctx, r.Publisher, channel, channel.Slug+"/feed.xml")
	if err != nil {
		return err
	}
	original := make([]string, len(fd.Items))
	for i, item := range fd.Items {
		original[i] = item.ID
	}
	if err := orderForRetention(fd.Items, channel.RetentionLimit(), r.uploadOrder); err != nil {
		return err
	}
	for i, item := range fd.Items {
		if item.ID != original[i] {
			if err := putFeed(ctx, r.Publisher, channel, fd); err != nil {
				return err
			}
			break
		}
	}
	if len(fd.Items) <= channel.RetentionLimit() {
		return nil
	}
	for _, item := range fd.Items[channel.RetentionLimit():] {
		if err := r.removeEpisode(ctx, channel, item.ID, Pruned); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) recoverRemovals(ctx context.Context, channel config.Channel) error {
	for id, episode := range r.State.Episodes(channel.Slug) {
		if episode.DeletePending {
			if err := r.finishRemoval(ctx, channel, id, episode); err != nil {
				return fmt.Errorf("finishing removal of %s: %w", id, err)
			}
		}
	}
	return nil
}

// PurgeChannel journals its intent before enumerating entries. A restart can
// resume even if only part of the feed or media was removed. The config caller
// disables synchronization before invoking this method.
func (r *Runner) PurgeChannel(ctx context.Context, channel config.Channel) error {
	if !safeChannel(channel.Slug) || r.Publisher == nil {
		return fmt.Errorf("valid channel and publishing storage are required")
	}
	if err := r.State.SetPurgePending(channel.Slug, true); err != nil {
		return err
	}
	fd, err := loadOrCreateFeed(ctx, r.Publisher, channel, channel.Slug+"/feed.xml")
	if err != nil {
		return err
	}
	entries := r.State.Episodes(channel.Slug)
	// Persist every feed-only entry before any feed mutation.
	for _, item := range fd.Items {
		if youtube.VideoID(item.ID) != item.ID {
			return fmt.Errorf("invalid episode id %q in feed", item.ID)
		}
		ep := entries[item.ID]
		if ep.Stage == "" {
			ep.Stage = state.Published
		}
		ep.HasPublished = true
		if err := r.State.Save(channel.Slug, item.ID, ep); err != nil {
			return err
		}
		entries[item.ID] = ep
	}
	for id := range entries {
		if err := r.removeEpisode(ctx, channel, id, Excluded); err != nil {
			return err
		}
	}
	if err := r.Publisher.Delete(ctx, channel.Slug+"/feed.xml"); err != nil {
		return err
	}
	return r.State.SetPurgePending(channel.Slug, false)
}

// admission checks chronology before paid work. Existing entries may be
// replaced; restoring an older entry requires increasing keep first.
func (r *Runner) admission(ctx context.Context, req EpisodeRequest, ep *state.Episode) (bool, error) {
	fd, err := loadOrCreateFeed(ctx, r.Publisher, req.Channel, req.Channel.Slug+"/feed.xml")
	if err != nil {
		return false, err
	}
	if ep.Video == nil {
		return false, fmt.Errorf("source metadata is required for retention planning")
	}
	allowed, err := fitsRetention(fd.Items, ep.Video, req.Channel.RetentionLimit(), r.uploadOrder)
	if err != nil || allowed {
		return allowed, err
	}
	if req.Action != Sync {
		return false, fmt.Errorf("episode falls outside keep=%d; increase keep before restoring or reprocessing it", req.Channel.RetentionLimit())
	}
	if err := r.State.Save(req.Channel.Slug, ep.Video.ID, *ep); err != nil {
		return false, err
	}
	if err := r.removeEpisode(ctx, req.Channel, ep.Video.ID, Pruned); err != nil {
		return false, err
	}
	*ep, _ = r.State.Episode(req.Channel.Slug, ep.Video.ID)
	return false, nil
}

func sourceDate(video *youtube.Video) (time.Time, bool) {
	if video == nil {
		return time.Time{}, false
	}
	if video.Timestamp > 0 {
		return time.Unix(video.Timestamp, 0).UTC(), true
	}
	date, err := time.Parse("20060102", video.UploadDate)
	return date, err == nil
}

func publicationDate(video *youtube.Video, previous time.Time) time.Time {
	date, ok := sourceDate(video)
	if !ok {
		if !previous.IsZero() {
			return previous
		}
		return parseUploadDate(video.UploadDate)
	}
	if video.Timestamp == 0 && date.Format("20060102") == previous.UTC().Format("20060102") {
		return previous
	}
	return date
}

func dayOnly(date time.Time) bool { return date.UTC().Format("150405") == "000000" }

func fitsRetention(items []feed.Item, video *youtube.Video, keep int, order map[string]int) (bool, error) {
	if keep <= 0 {
		return false, fmt.Errorf("keep must be positive")
	}
	for _, item := range items {
		if item.ID == video.ID {
			return true, nil
		}
	}
	date, ok := sourceDate(video)
	if !ok {
		return false, fmt.Errorf("source publication date is unavailable for %s; retry when metadata is available", video.ID)
	}
	candidates := append(append([]feed.Item(nil), items...), feed.Item{ID: video.ID, PublishedAt: date})
	orderErr := orderForRetention(candidates, keep, order)
	for i, item := range candidates {
		if item.ID != video.ID {
			continue
		}
		if orderErr != nil && date.UTC().Format("20060102") == candidates[keep-1].PublishedAt.UTC().Format("20060102") {
			if _, known := order[video.ID]; !known {
				return false, orderErr
			}
		}
		return i < keep, nil
	}
	return false, fmt.Errorf("episode missing from retention plan")
}

// Never turn a guessed same-day order into permanent pruning. Legacy feeds and
// interrupted publication may not have a known order until the source listing
// is fetched. Existing source timestamps or listing positions resolve it.
func orderForRetention(items []feed.Item, keep int, order map[string]int) error {
	feed.OrderItems(items, order)
	if len(items) <= keep {
		return nil
	}
	a, b := items[keep-1], items[keep]
	day := a.PublishedAt.UTC().Format("20060102")
	if day != b.PublishedAt.UTC().Format("20060102") {
		return nil
	}
	_, aok := order[a.ID]
	_, bok := order[b.ID]
	if aok || bok {
		return nil
	}
	for _, item := range items {
		if item.PublishedAt.UTC().Format("20060102") == day && dayOnly(item.PublishedAt) {
			return fmt.Errorf("%w: sync a latest window covering the retention cutoff, or increase keep", ErrSourceOrder)
		}
	}
	return nil
}
