package pipeline

import (
	"context"
	"errors"
	"fmt"

	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/transcript"
	"github.com/cwebley/shearcast/internal/usage"
	"github.com/cwebley/shearcast/internal/worklimit"
	"github.com/cwebley/shearcast/internal/youtube"
)

func (r *Runner) renderTracked(ctx context.Context, req EpisodeRequest, client *jev.Client, video *youtube.Video, cues []transcript.Cue, opts RenderOptions) error {
	if err := r.State.BeginUsage(req.Channel.Slug, video.ID, opts.RenderID, r.Config.Jev.Model, req.Action == Sync); err != nil {
		return errors.Join(usage.ErrCheckpoint, err)
	}
	before := client.Stats()
	var recorded usage.Summary
	checkpoint := func(stats jev.Stats, finished bool) error {
		current := stats.Since(before)
		// Keep in-memory totals even when persistence fails, so the command can
		// still report observed work alongside its state-write error.
		r.Usage = r.Usage.Add(current.Since(recorded))
		recorded = current
		err := r.State.CheckpointUsage(req.Channel.Slug, video.ID, opts.RenderID, current, finished)
		if err != nil {
			worklimit.Stop(ctx, errors.Join(usage.ErrCheckpoint, err))
		}
		return err
	}
	client.RecordUsage(func(stats jev.Stats) error { return checkpoint(stats, false) })
	defer client.RecordUsage(nil)
	_, _, _, renderErr := RenderEpisode(ctx, r.Config, req.Channel, r.Cache, client, video, cues, opts, r.episodeProgress(req.Channel.Slug, video.ID))
	if err := checkpoint(client.Stats(), true); err != nil {
		return errors.Join(renderErr, usage.ErrCheckpoint, fmt.Errorf("saving processing usage: %w", err))
	}
	return renderErr
}
