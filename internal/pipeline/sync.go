package pipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/storage"
	"github.com/cwebley/shearcast/internal/usage"
	"github.com/cwebley/shearcast/internal/youtube"
)

type EpisodeOutcome struct {
	ID      string
	Episode state.Episode
	Error   error
	Skipped string // why an inaccessible upload was passed over, in youtube.Video.Unavailable's words
	Usage   usage.Summary
}

// uploads returns the latest-N window of accessible uploads, the inaccessible
// ones listed among them, which do not count toward N, and the source listing
// position of each. Positions include skipped uploads: an upload that became
// inaccessible is no older for it.
func (r *Runner) uploads(ctx context.Context, ch config.Channel) (selected, skipped []youtube.Video, order map[string]int, err error) {
	if ch.URL == "" {
		return nil, nil, nil, fmt.Errorf("no source URL configured")
	}
	list := r.ListUploads
	if list == nil {
		list = youtube.List
	}
	limit := ch.SelectionLimit()
	if limit <= 0 || ch.RetentionLimit() < limit {
		return nil, nil, nil, fmt.Errorf("latest must be positive and keep must be at least latest")
	}
	uploads, err := list(ctx, ch.URL, limit)
	order = map[string]int{}
	for i, v := range uploads {
		if len(selected) == limit {
			break
		}
		order[v.ID] = i
		if v.Unavailable() != "" {
			skipped = append(skipped, v)
		} else {
			selected = append(selected, v)
		}
	}
	return selected, skipped, order, err
}

// skip reports an inaccessible upload. Access can change, so nothing lasting
// is recorded; a record left only by an earlier failed attempt is dropped so
// it does not linger as unfinished work.
func (r *Runner) skip(ch config.Channel, id, reason string) (EpisodeOutcome, error) {
	ep, ok := r.State.Episode(ch.Slug, id)
	if ok && abandonedAttempt(ep) {
		if err := r.State.Forget(ch.Slug, id); err != nil {
			return EpisodeOutcome{ID: id, Episode: ep, Error: err}, err
		}
		ep = state.Episode{}
	}
	return EpisodeOutcome{ID: id, Episode: ep, Skipped: reason}, nil
}

// abandonedAttempt is a record holding nothing but an unfinished attempt: no
// publication, removal, artifact or model usage.
func abandonedAttempt(ep state.Episode) bool {
	return (ep.Stage == state.Pending || ep.Stage == state.Waiting) && !ep.HasPublished && ep.Removal == "" && !ep.DeletePending &&
		ep.PendingPublication == nil && ep.AudioSHA256 == "" && ep.RecordPath == "" && ep.Usage == nil && len(ep.AudioKeys) == 0
}

// withArtwork fills in the channel's show artwork from its source when config
// names none. A failed lookup only costs freshness: the feed keeps whatever
// artwork it already has.
func (r *Runner) withArtwork(ctx context.Context, ch config.Channel) config.Channel {
	if ch.Image != "" || r.ChannelArtwork == nil || ch.URL == "" {
		return ch
	}
	image, err := r.ChannelArtwork(ctx, ch.URL)
	if err != nil {
		if r.Progress != nil {
			r.Progress(fmt.Sprintf("%s: keeping existing show artwork: %v", ch.Slug, err))
		}
		return ch
	}
	ch.Image = image
	return ch
}

// refreshArtwork rewrites an existing feed whose show artwork differs from the
// channel's, so new artwork appears without waiting for a new episode.
func (r *Runner) refreshArtwork(ctx context.Context, ch config.Channel) error {
	if ch.Image == "" {
		return nil
	}
	data, err := r.Publisher.Get(ctx, ch.Slug+"/feed.xml")
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	current, err := feed.Parse(data)
	if err != nil {
		return err
	}
	if current.ImageURL == ch.Image {
		return nil
	}
	fd, err := loadOrCreateFeed(ctx, r.Publisher, ch, ch.Slug+"/feed.xml")
	if err != nil {
		return err
	}
	return putFeed(ctx, r.Publisher, ch, fd)
}

// SyncChannel retries verified publication independently of source availability,
// then processes only the bounded upload window. Every episode is sequential.
func (r *Runner) SyncChannel(ctx context.Context, ch config.Channel) (result []EpisodeOutcome, runErr error) {
	r.uploadOrder = nil
	purging := r.State.PurgePending(ch.Slug)
	if err := r.State.StartChannelSync(ch.Slug); err != nil {
		return nil, err
	}
	defer func() {
		runErr = errors.Join(runErr, r.State.FinishChannelSync(ch.Slug, ch.Disabled || purging, runErr))
	}()
	if ch.Disabled && !r.State.PurgePending(ch.Slug) {
		return nil, nil
	}
	if err := r.Recover(ctx, ch); err != nil {
		return nil, fmt.Errorf("recovering interrupted work: %w", err)
	}
	if ch.Disabled || purging {
		return nil, nil
	}
	ch = r.withArtwork(ctx, ch)
	if err := r.refreshArtwork(ctx, ch); err != nil {
		return nil, fmt.Errorf("updating show artwork: %w", err)
	}
	if err := r.EnforceRetention(ctx, ch); err != nil && !errors.Is(err, ErrSourceOrder) {
		return nil, fmt.Errorf("retention: %w", err)
	}
	var outcomes []EpisodeOutcome
	finish := func(extra error) ([]EpisodeOutcome, error) {
		var failures []error
		for _, outcome := range outcomes {
			if outcome.Error != nil {
				failures = append(failures, fmt.Errorf("%s: %w", outcome.ID, outcome.Error))
			}
		}
		return outcomes, errors.Join(append(failures, extra)...)
	}
	attempted := map[string]bool{}
	positions := map[string]int{}
	run := func(id string) error {
		if attempted[id] {
			return nil
		}
		attempted[id] = true
		before := r.Usage.Clone()
		ep, err := r.Run(ctx, EpisodeRequest{Action: Sync, Channel: ch, Target: id})
		outcome := EpisodeOutcome{ID: id, Episode: ep, Error: err, Usage: r.Usage.Since(before)}
		if unavailable := (*youtube.UnavailableError)(nil); errors.As(err, &unavailable) {
			outcome, err = r.skip(ch, id, unavailable.Reason)
			outcome.Usage = r.Usage.Since(before)
		}
		if i, ok := positions[id]; ok {
			outcome.Usage = outcomes[i].Usage.Add(outcome.Usage)
			outcomes[i] = outcome
		} else {
			positions[id] = len(outcomes)
			outcomes = append(outcomes, outcome)
		}
		return err
	}
	var deferred []string
	for _, id := range r.ReadyToPublish(ch) {
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		err := run(id)
		if errors.Is(err, usage.ErrCheckpoint) {
			return finish(nil)
		}
		if errors.Is(err, ErrSourceOrder) {
			// Publication may already have finished before retention discovered
			// an ambiguous older cutoff. Only admission-blocked work needs Run
			// again; completed publication must not turn into a source refresh.
			ep, _ := r.State.Episode(ch.Slug, id)
			if !ep.HasPublished || ep.PublishPending {
				delete(attempted, id)
				deferred = append(deferred, id)
			}
		}
	}
	uploads, skipped, order, err := r.uploads(ctx, ch)
	if err != nil {
		return finish(fmt.Errorf("listing uploads: %w", err))
	}
	for _, v := range skipped {
		if attempted[v.ID] {
			continue
		}
		attempted[v.ID] = true
		outcome, _ := r.skip(ch, v.ID, v.Unavailable())
		outcomes = append(outcomes, outcome)
	}
	r.uploadOrder = order
	if err := r.EnforceRetention(ctx, ch); err != nil && !errors.Is(err, ErrSourceOrder) {
		return finish(err)
	}
	for _, id := range deferred {
		if err := run(id); errors.Is(err, usage.ErrCheckpoint) {
			return finish(nil)
		}
	}
	for _, upload := range uploads {
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		if err := run(upload.ID); errors.Is(err, usage.ErrCheckpoint) {
			return finish(nil)
		}
	}
	err = r.EnforceRetention(ctx, ch)
	if err == nil {
		for i, outcome := range outcomes {
			if errors.Is(outcome.Error, ErrSourceOrder) {
				ep, _ := r.State.Episode(ch.Slug, outcome.ID)
				if ep.HasPublished && !ep.PublishPending {
					ep.LastError = ""
					if saveErr := r.State.Save(ch.Slug, outcome.ID, ep); saveErr != nil {
						return finish(saveErr)
					}
					outcomes[i].Error = nil
					outcomes[i].Episode = ep
				}
			}
		}
	}
	return finish(err)
}
