package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/feed"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/usage"
	"github.com/cwebley/shearcast/internal/youtube"
)

type PlannedEpisode struct {
	ID              string
	Status          string
	Selected        bool
	SourceSeconds   float64
	EstimatedBytes  int64
	NeedsProcessing bool
	Skipped         string // inaccessible upload passed over; outside the window
}

type SyncPlan struct {
	Episodes                    []PlannedEpisode
	SourceSeconds               float64
	ProcessingSeconds           float64
	UnknownDurations            int
	CurrentBytes                int64
	UploadBytes                 int64
	RetainedBytes               int64
	PruneIDs                    []string
	HistorySamples              int
	CostKnown                   bool
	ModelCostLow, ModelCostHigh float64
	RecoveryPending             bool
	ChronologyPending           bool
	Pricing                     *usage.Rate
}

// Plan reads source listings, metadata, feed and local records. It never runs
// recovery, captions, detection, rendering, publication or state mutations.
// Storage is an AAC-target estimate with 3% container allowance, not a quota.
func (r *Runner) Plan(ctx context.Context, ch config.Channel) (*SyncPlan, error) {
	p := &SyncPlan{CostKnown: true, RecoveryPending: r.State.PurgePending(ch.Slug), Pricing: usage.ModelRate(r.Config.Jev.Model)}
	if ch.Disabled {
		return p, nil
	}
	uploads, skipped, listOrder, err := r.uploads(ctx, ch)
	if err != nil {
		return nil, err
	}
	fd, err := loadOrCreateFeed(ctx, r.Publisher, ch, ch.Slug+"/feed.xml")
	if err != nil {
		return nil, err
	}
	for _, item := range fd.Items {
		p.CurrentBytes += item.AudioBytes
	}
	entries := r.State.Episodes(ch.Slug)
	pruned := map[string]bool{}
	prune := func(order map[string]int) {
		if err := orderForRetention(fd.Items, ch.RetentionLimit(), order); err != nil {
			return
		}
		kept := fd.Items[:0]
		for _, item := range fd.Items {
			if entries[item.ID].Removal != "" || len(kept) >= ch.RetentionLimit() {
				pruned[item.ID] = true
				ep := entries[item.ID]
				if ep.Removal == "" {
					ep.Removal = Pruned
				}
				ep.PublishPending = false
				entries[item.ID] = ep
				continue
			}
			kept = append(kept, item)
		}
		fd.Items = kept
	}
	prune(nil)
	seen := map[string]bool{}
	add := func(v youtube.Video, selected bool, order map[string]int) error {
		if seen[v.ID] {
			return nil
		}
		seen[v.ID] = true
		if youtube.VideoID(v.ID) != v.ID {
			return fmt.Errorf("invalid source video id %q", v.ID)
		}
		ep := entries[v.ID]
		path := ep.RenderPath
		if path == "" {
			path = RenderPath(r.Cache, ch, v.ID)
		}
		artifact := ep
		ready := false
		var verifyErr error
		if ep.Removal == "" && !(ep.HasPublished && !ep.PublishPending) {
			ready, verifyErr = recoverRender(ctx, path, ch.Slug, v.ID, &artifact)
			if ready && verifyErr == nil && ep.Video == nil {
				ep.Video = artifact.Video
			}
		}
		row := PlannedEpisode{ID: v.ID, Selected: selected, Status: "new"}
		if ep.Video != nil {
			if v.Timestamp == 0 {
				v.Timestamp = ep.Video.Timestamp
			}
			if v.Duration <= 0 {
				v.Duration = ep.Video.Duration
			}
			if v.UploadDate == "" {
				v.UploadDate = ep.Video.UploadDate
			}
		}
		switch {
		case ep.Removal != "":
			row.Status = ep.Removal
		case ep.HasPublished && !ep.PublishPending:
			row.Status = "existing"
		case ep.Stage == state.Waiting:
			row.Status = "waiting"
		case ep.Stage == state.Rendered:
			row.Status = "rendered"
		case ep.LastError != "":
			row.Status = "retry"
		}
		if ep.DeletePending {
			p.RecoveryPending = true
		}
		if _, err := os.Stat(path + ".install.json"); err == nil {
			p.RecoveryPending = true
		}
		_, dated := sourceDate(&v)
		if ep.Removal == "" && (v.Duration <= 0 || !dated) {
			var info *youtube.Video
			if r.Source != nil {
				info, err = r.Source.Info(ctx, v.ID)
			} else {
				info, err = youtube.Info(ctx, "https://www.youtube.com/watch?v="+v.ID)
			}
			if unavailable := (*youtube.UnavailableError)(nil); errors.As(err, &unavailable) {
				p.Episodes = append(p.Episodes, PlannedEpisode{ID: v.ID, Status: "skipped", Skipped: unavailable.Reason})
				return nil
			}
			if err != nil {
				return fmt.Errorf("planning metadata for %s: %w", v.ID, err)
			}
			if info == nil || info.ID != v.ID {
				return fmt.Errorf("source returned mismatched video metadata")
			}
			v = *info
		}
		row.SourceSeconds = v.Duration
		if selected {
			if v.Duration > 0 {
				p.SourceSeconds += v.Duration
			} else {
				p.UnknownDurations++
			}
		}
		if ep.Removal == "" && row.Status != "existing" {
			if ready && verifyErr == nil {
				row.Status, row.EstimatedBytes = "ready_to_publish", artifact.AudioBytes
			} else if verifyErr != nil || ep.Stage == state.Rendered || ep.HasPublished && !ep.ReprocessPending {
				row.Status = "blocked_artifact"
			} else {
				row.NeedsProcessing = true
				row.EstimatedBytes = int64(math.Ceil(v.Duration * float64(r.Config.OutputBitrate(ch)) * 125 * 1.03))
			}
			if row.Status != "blocked_artifact" {
				allowed, admissionErr := fitsRetention(fd.Items, &v, ch.RetentionLimit(), order)
				if admissionErr != nil {
					row.Status, row.NeedsProcessing, row.EstimatedBytes = "waiting_metadata", false, 0
					if errors.Is(admissionErr, ErrSourceOrder) {
						row.Status = "waiting_source_order"
					}
				} else if !allowed {
					row.Status, row.NeedsProcessing, row.EstimatedBytes = "outside_retention", false, 0
				} else {
					date, _ := sourceDate(&v)
					fd.Upsert(feed.Item{ID: v.ID, PublishedAt: date, AudioBytes: row.EstimatedBytes})
					prune(order)
				}
			}
		} else if row.Status == "existing" {
			found := false
			for _, item := range fd.Items {
				if item.ID != v.ID {
					continue
				}
				found = true
				item.PublishedAt = publicationDate(&v, item.PublishedAt)
				fd.Upsert(item)
				prune(order)
				break
			}
			if !found {
				if pruned[v.ID] {
					row.Status = "pruned"
				} else {
					row.Status = "missing_feed"
				}
			}
		}
		p.Episodes = append(p.Episodes, row)
		return nil
	}
	// The queue may lie outside latest-N. Only verified checkpoints qualify;
	// unfinished work remains bounded by the selected window. A queued
	// artifact that fails verification is shown as blocked.
	var ids []string
	for id, ep := range entries {
		if ep.PublishPending && ep.Removal == "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	selected := map[string]youtube.Video{}
	for _, v := range uploads {
		selected[v.ID] = v
	}
	var deferred []youtube.Video
	for _, id := range ids {
		ep := entries[id]
		path := ep.RenderPath
		if path == "" {
			path = RenderPath(r.Cache, ch, id)
		}
		ready, err := recoverRender(ctx, path, ch.Slug, id, &ep)
		if _, inWindow := selected[id]; !inWindow && (err != nil || !ready) {
			// Sync's recovery will fail on it, so show it rather than
			// letting it vanish; in-window entries get the same row below.
			p.Episodes = append(p.Episodes, PlannedEpisode{ID: id, Status: "blocked_artifact"})
			seen[id] = true
			continue
		}
		if err == nil && ready {
			v, inWindow := selected[id]
			if !inWindow {
				v = youtube.Video{ID: id}
			}
			if err := add(v, inWindow, nil); err != nil {
				return nil, err
			}
			if row := p.Episodes[len(p.Episodes)-1]; row.Status == "waiting_source_order" {
				deferred = append(deferred, v)
				p.Episodes = p.Episodes[:len(p.Episodes)-1]
				delete(seen, v.ID)
				if inWindow {
					if row.SourceSeconds > 0 {
						p.SourceSeconds -= row.SourceSeconds
					} else {
						p.UnknownDurations--
					}
				}
			}
		}
	}
	order := listOrder
	prune(order)
	for _, v := range deferred {
		_, inWindow := selected[v.ID]
		if err := add(v, inWindow, order); err != nil {
			return nil, err
		}
	}
	for _, v := range skipped {
		if seen[v.ID] {
			continue
		}
		p.Episodes = append(p.Episodes, PlannedEpisode{ID: v.ID, Status: "skipped", Skipped: v.Unavailable()})
	}
	for _, v := range uploads {
		if err := add(v, true, order); err != nil {
			return nil, err
		}
	}
	p.ChronologyPending = errors.Is(orderForRetention(fd.Items, ch.RetentionLimit(), order), ErrSourceOrder)
	retained := map[string]bool{}
	for _, item := range fd.Items {
		retained[item.ID] = true
		p.RetainedBytes += item.AudioBytes
	}
	for id := range pruned {
		p.PruneIDs = append(p.PruneIDs, id)
	}
	sort.Strings(p.PruneIDs)
	for i := range p.Episodes {
		row := &p.Episodes[i]
		if row.EstimatedBytes > 0 || row.NeedsProcessing {
			if !retained[row.ID] {
				row.Status += "_then_pruned"
			}
			p.UploadBytes += row.EstimatedBytes
			if row.NeedsProcessing {
				p.ProcessingSeconds += row.SourceSeconds
				if row.SourceSeconds <= 0 {
					p.CostKnown = false
				}
			}
		}
	}
	minRate, maxRate := math.Inf(1), 0.0
	wantRules := r.Config.DetectOptions(ch.Rules).Rules
	for id, ep := range entries {
		if ep.DeletePending {
			p.RecoveryPending = true
		}
		path := ep.RenderPath
		if path == "" {
			path = RenderPath(r.Cache, ch, id)
		}
		data, err := os.ReadFile(recordPath(path, ep))
		if err != nil {
			continue
		}
		var record RenderRecord
		if json.Unmarshal(data, &record) != nil || record.Channel != ch.Slug || record.VideoID != id || record.Model != r.Config.Jev.Model || record.Source == nil || record.Source.Duration <= 0 || record.Detection == nil || record.Detection.Stats.InputTokens <= 0 || !reflect.DeepEqual(record.DetectionOptions.Rules, wantRules) {
			continue
		}
		if record.Usage != nil && (record.Usage.MissingUsage > 0 || record.Usage.Unresolved > 0) {
			continue
		}
		rate := float64(record.Detection.Stats.InputTokens) / record.Source.Duration
		minRate, maxRate = math.Min(minRate, rate), math.Max(maxRate, rate)
		p.HistorySamples++
	}
	if p.ProcessingSeconds > 0 {
		if p.HistorySamples == 0 || p.Pricing == nil {
			p.CostKnown = false
		} else {
			p.ModelCostLow = p.ProcessingSeconds * minRate * 0.5 / 1e6 * p.Pricing.InputPerMillion
			p.ModelCostHigh = p.ProcessingSeconds * maxRate * 2 / 1e6 * p.Pricing.InputPerMillion
		}
	}
	return p, nil
}
