package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/fileutil"
	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/render"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/transcript"
	"github.com/cwebley/shearcast/internal/usage"
	"github.com/cwebley/shearcast/internal/youtube"
)

// Source provides metadata and caption access. youtube.Cache is the production
// adapter; lifecycle tests substitute a source that can fail between stages.
type Source interface {
	Info(context.Context, string) (*youtube.Video, error)
	RefreshInfo(context.Context, string) (*youtube.Video, error)
	Cues(context.Context, *youtube.Video) ([]transcript.Cue, error)
}

type Action string

const (
	Sync      Action = "sync"
	Render    Action = "render"
	Publish   Action = "publish"
	Restore   Action = "restore"
	Reprocess Action = "reprocess"
)

type EpisodeRequest struct {
	Action        Action
	Channel       config.Channel
	Target        string
	RenderOptions RenderOptions
	AudioPath     string // explicit import for publish; no sidecar required
}

// Runner owns episode transitions for all mutating commands. State must stay
// open throughout a command so its process lock covers rendering and publishing.
// NewClient is lazy: resuming a render or refreshing metadata needs no model key.
type Runner struct {
	Config      *config.Config
	Cache       youtube.Cache
	State       *state.Store
	Source      Source
	Publisher   Publisher
	NewClient   func() (*jev.Client, error)
	Progress    Progress
	ListUploads func(context.Context, string, int) ([]youtube.Video, error)
	// ChannelArtwork finds a channel's show artwork when config names none.
	// Nil leaves the feed's existing artwork alone.
	ChannelArtwork func(context.Context, string) (string, error)
	uploadOrder    map[string]int // source listing position of each upload in this channel's window, skipped ones included
	Usage          usage.Summary  // observations from this runner invocation, including failures
}

// episodeProgress prefixes Runner.Progress messages with the channel and
// episode they concern. It returns nil when progress is off.
func (r *Runner) episodeProgress(channel, id string) Progress {
	if r.Progress == nil {
		return nil
	}
	return func(msg string) { r.Progress(channel + ": " + id + ": " + msg) }
}

// say sends one message, doing nothing when progress is off.
func (p Progress) say(msg string) {
	if p != nil {
		p(msg)
	}
}

func (r *Runner) Run(ctx context.Context, req EpisodeRequest) (episode state.Episode, err error) {
	if req.Action != Sync && req.Action != Render && req.Action != Publish && req.Action != Restore && req.Action != Reprocess {
		return episode, fmt.Errorf("unknown episode action %q", req.Action)
	}
	id := youtube.VideoID(req.Target)
	if id == "" {
		return episode, fmt.Errorf("need a YouTube video URL or eleven-character id")
	}
	if !safeChannel(req.Channel.Slug) {
		return episode, fmt.Errorf("invalid channel slug %q", req.Channel.Slug)
	}
	if r.State == nil {
		return episode, fmt.Errorf("episode state is required")
	}
	if req.Action != Render && r.Publisher == nil {
		return episode, fmt.Errorf("publishing storage is required")
	}
	source := r.Source
	if source == nil {
		source = r.Cache
	}
	episode, _ = r.State.Episode(req.Channel.Slug, id)
	if r.State.PurgePending(req.Channel.Slug) {
		return episode, fmt.Errorf("channel purge is pending; finish it before processing episodes")
	}
	if req.Action != Render {
		if err := r.recoverRemovals(ctx, req.Channel); err != nil {
			return episode, err
		}
		episode, _ = r.State.Episode(req.Channel.Slug, id)
	}
	if episode.DeletePending {
		if err := r.finishRemoval(ctx, req.Channel, id, episode); err != nil {
			return episode, err
		}
		episode, _ = r.State.Episode(req.Channel.Slug, id)
	}
	if episode.Removal != "" && req.Action != Restore {
		if req.Action == Sync {
			return episode, nil
		}
		return episode, fmt.Errorf("episode is %s; use episode restore first", episode.Removal)
	}
	if req.Action == Restore && episode.Removal == "" {
		return episode, fmt.Errorf("episode is not removed; use episode reprocess to replace it")
	}
	forceRender := req.Action == Render || req.Action == Restore || req.Action == Reprocess
	if episode.Stage == "" {
		episode.Stage = state.Pending
	}
	if episode.Stage == state.Published {
		episode.HasPublished = true
	}
	if r.Publisher != nil {
		if err := r.reconcilePublication(ctx, req.Channel, id, &episode); err != nil {
			return episode, err
		}
	} else if episode.PendingPublication != nil {
		return episode, fmt.Errorf("publication is unfinished; retry publish before replacing this render")
	}
	defer func() {
		// Sync passes over an inaccessible video without recording anything:
		// access can change, and it is not a failure of this episode.
		if err != nil && !(req.Action == Sync && errors.Is(err, youtube.ErrUnavailable)) {
			// Retention may have journaled this episode's removal before a
			// remote deletion failed. Never overwrite that intent with the
			// earlier processing snapshot.
			if current, ok := r.State.Episode(req.Channel.Slug, id); ok && current.Removal != "" {
				episode = current
			}
			episode.LastError = err.Error()
			if saveErr := r.State.Save(req.Channel.Slug, id, episode); saveErr != nil {
				err = errors.Join(err, fmt.Errorf("recording failure: %w", saveErr))
			}
			if current, ok := r.State.Episode(req.Channel.Slug, id); ok {
				episode.Usage = current.Usage
			}
		}
	}()
	save := func() error {
		episode.LastError = ""
		if err := r.State.Save(req.Channel.Slug, id, episode); err != nil {
			return err
		}
		episode, _ = r.State.Episode(req.Channel.Slug, id)
		return nil
	}

	path := episode.RenderPath
	if path == "" {
		path = RenderPath(r.Cache, req.Channel, id)
	}
	if forceRender {
		path = RenderPath(r.Cache, req.Channel, id)
		if req.RenderOptions.OutPath != "" {
			path = req.RenderOptions.OutPath
		}
	}
	if req.Action == Publish && req.AudioPath != "" {
		path = req.AudioPath
	}
	path, err = fileutil.CanonicalPath(path)
	if err != nil {
		return episode, err
	}
	if forceRender && episode.RecordPath != "" && sameFile(path, episode.RenderPath) {
		return episode, fmt.Errorf("render output must be outside publication storage; render privately, then publish")
	}
	if episode.RecordPath == "" || path != episode.RenderPath {
		if err := r.prepareArtifact(path, id); err != nil {
			return episode, err
		}
	}
	if req.Action == Sync && episode.HasPublished && !episode.PublishPending {
		video, err := source.RefreshInfo(ctx, req.Target)
		if err != nil {
			return episode, fmt.Errorf("refreshing metadata: %w", err)
		}
		if video == nil || video.ID != id {
			return episode, fmt.Errorf("source returned mismatched video metadata")
		}
		var metadata *chapterMetadata
		if episode.ChaptersEnabled {
			metadata, err = r.prepareChapters(ctx, req.Channel, video, episode.PublishedTimeline, &episode)
			if err != nil {
				return episode, err
			}
		}
		result, err := refreshPublishedEpisode(ctx, r.Publisher, req.Channel, video, metadata)
		if err != nil {
			return episode, fmt.Errorf("refreshing feed: %w", err)
		}
		episode.Video, episode.AudioURL, episode.FeedURL = video, result.AudioURL, result.FeedURL
		if episode.Stage == state.Published {
			err = save()
		} else {
			err = r.State.Save(req.Channel.Slug, id, episode)
		} // preserve a failed manual replacement's error
		if err == nil {
			err = r.cleanupRevisions(ctx, req.Channel, id, &episode)
		}
		if err == nil {
			err = r.EnforceRetention(ctx, req.Channel)
		}
		if err == nil {
			episode, _ = r.State.Episode(req.Channel.Slug, id)
		}
		return episode, err
	}
	if req.Action == Sync {
		episode.PublishPending = true
	}
	if req.Action == Render {
		episode.PublishPending = false
		episode.ReprocessPending = false
	}

	// A sidecar is also a checkpoint when a process stopped after rendering but
	// before recording the rendered stage in the main state file.
	ready := false
	if !forceRender {
		if req.Action == Publish && req.AudioPath != "" {
			if _, statErr := os.Stat(path + ".json"); statErr == nil {
				imported := episode
				imported.RenderID = "" // explicit import may select an older completed attempt
				imported.RecordPath = ""
				ready, err = recoverRender(ctx, path, req.Channel.Slug, id, &imported)
				if err == nil && ready {
					episode = imported
				}
			} else if os.IsNotExist(statErr) {
				err = inspectImportedAudio(ctx, path, &episode)
				ready = err == nil
			} else {
				err = statErr
			}
		} else {
			ready, err = recoverRender(ctx, path, req.Channel.Slug, id, &episode)
		}
		if err != nil {
			return episode, err
		}
		if !ready && (episode.Stage == state.Rendered || episode.HasPublished && !episode.ReprocessPending || req.Action == Publish) {
			return episode, fmt.Errorf("finished audio is missing at %s; use render to explicitly reprocess, or publish -audio to import an older render", path)
		}
		if req.Action == Publish && ready {
			episode.PublishPending = true
		}
	}
	if !ready {
		var video *youtube.Video
		var loadErr error
		_, dated := sourceDate(episode.Video)
		if req.Action == Restore || episode.Stage == state.Waiting || episode.Video != nil && !dated {
			video, loadErr = source.RefreshInfo(ctx, req.Target)
		} else {
			video, loadErr = source.Info(ctx, req.Target)
		}
		if loadErr != nil {
			return episode, fmt.Errorf("loading metadata: %w", loadErr)
		}
		if video == nil || video.ID != id {
			return episode, fmt.Errorf("source returned mismatched video metadata")
		}
		episode.Video, episode.RenderPath = video, path
		episode.RecordPath = ""
		if req.Action != Render {
			allowed, err := r.admission(ctx, req, &episode)
			if err != nil || !allowed {
				return episode, err
			}
			episode.Removal, episode.PublishPending = "", true
			if req.Action == Restore || req.Action == Reprocess {
				episode.ReprocessPending = true
			}
		}
		episode.Stage = state.Pending
		episode.PendingPublication = nil
		episode.RenderID = rand.Text()
		episode.AudioSHA256, episode.AudioBytes, episode.DurationSeconds = "", 0, 0
		if err := save(); err != nil {
			return episode, err
		}
		r.episodeProgress(req.Channel.Slug, id).say("fetching captions...")
		cues, err := source.Cues(ctx, video)
		if errors.Is(err, youtube.ErrCaptionsUnavailable) {
			if episode.Stage != state.Published {
				episode.Stage = state.Waiting
			}
			if saveErr := save(); saveErr != nil {
				return episode, saveErr
			}
			return episode, nil
		}
		if err != nil {
			return episode, fmt.Errorf("loading captions: %w", err)
		}
		if r.NewClient == nil {
			return episode, fmt.Errorf("model client is required for a new render")
		}
		client, err := r.NewClient()
		if err != nil {
			return episode, err
		}
		opts := req.RenderOptions
		opts.OutPath = path
		opts.RenderID = episode.RenderID
		if err := r.renderTracked(ctx, req, client, video, cues, opts); err != nil {
			return episode, err
		}
		ready, err = recoverRender(ctx, path, req.Channel.Slug, id, &episode)
		if err != nil {
			return episode, err
		}
		if !ready {
			return episode, fmt.Errorf("render completed without a durable record")
		}
	}
	if episode.Video == nil {
		video, err := source.Info(ctx, req.Target)
		if err != nil {
			return episode, err
		}
		if video == nil || video.ID != id {
			return episode, fmt.Errorf("source returned mismatched video metadata")
		}
		episode.Video = video
	}
	if req.Action != Render {
		if _, dated := sourceDate(episode.Video); !dated {
			video, err := source.RefreshInfo(ctx, req.Target)
			if err != nil {
				return episode, fmt.Errorf("refreshing publication date: %w", err)
			}
			if video == nil || video.ID != id {
				return episode, fmt.Errorf("source returned mismatched video metadata")
			}
			episode.Video = video
		}
		allowed, err := r.admission(ctx, req, &episode)
		if err != nil || !allowed {
			return episode, err
		}
	}
	episode.RenderPath, episode.Stage = path, state.Rendered
	if err := save(); err != nil {
		return episode, err
	}
	// Deletion follows both the durable output and the state checkpoint. A
	// cleanup failure leaves the render retryable without another detection.
	if err := r.Cache.RemoveAudio(id); err != nil {
		return episode, fmt.Errorf("removing source audio: %w", err)
	}
	if req.Action == Render {
		return episode, nil
	}
	timeline, err := publicationTimeline(episode, req.Channel.Slug, id)
	if err != nil {
		return episode, err
	}
	metadata, err := r.prepareChapters(ctx, req.Channel, episode.Video, timeline, &episode)
	if err != nil {
		return episode, err
	}
	audioKey := req.Channel.Slug + "/" + id + ".m4a"
	if episode.HasPublished || episode.PublishedAudioKey != "" {
		audioKey = req.Channel.Slug + "/" + id + "." + episode.AudioSHA256 + ".m4a"
	}
	if episode.PublishedAudioSHA256 == episode.AudioSHA256 && episode.PublishedAudioKey != "" {
		audioKey = episode.PublishedAudioKey
	}
	// Journal before any upload, and keep the old enclosure until RSS switches.
	if episode.HasPublished && episode.PublishedAudioKey == "" && len(episode.AudioKeys) == 0 {
		episode.AudioKeys = append(episode.AudioKeys, req.Channel.Slug+"/"+id+".m4a")
	}
	metadata.publicationID = rand.Text()
	episode.PendingPublication = &state.Publication{ID: metadata.publicationID, AudioKey: audioKey, AudioSHA256: episode.AudioSHA256, Timeline: timeline}
	if !slices.Contains(episode.AudioKeys, audioKey) {
		episode.AudioKeys = append(episode.AudioKeys, audioKey)
	}
	if err := save(); err != nil {
		return episode, err
	}
	r.episodeProgress(req.Channel.Slug, id).say("uploading audio and updating the feed...")
	result, err := publishEpisode(ctx, r.Publisher, req.Channel, episode.Video, path, r.uploadOrder, metadata, audioKey)
	if err != nil {
		return episode, err
	}
	if err := r.adoptLocalPublication(req.Channel, id, result.AudioKey, &episode); err != nil {
		return episode, err
	}
	episode.Stage, episode.AudioURL, episode.FeedURL = state.Published, result.AudioURL, result.FeedURL
	episode.PublishPending = false
	episode.ReprocessPending = false
	episode.AdoptPublication(*episode.PendingPublication)
	episode.PendingPublication = nil
	err = save()
	if err == nil {
		err = r.cleanupPublishedRender(req.Channel, id, episode)
	}
	if err == nil {
		err = r.cleanupRevisions(ctx, req.Channel, id, &episode)
	}
	if err == nil {
		err = r.EnforceRetention(ctx, req.Channel)
	}
	if err == nil {
		episode, _ = r.State.Episode(req.Channel.Slug, id)
	}
	return episode, err
}

func safeChannel(slug string) bool {
	return config.ValidSlug(slug)
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	aInfo, aErr := os.Stat(a)
	bInfo, bErr := os.Stat(b)
	return aErr == nil && bErr == nil && os.SameFile(aInfo, bInfo)
}

func (r *Runner) prepareArtifact(path, id string) error {
	sourcePath, err := fileutil.CanonicalPath(r.Cache.SourceAudioPath(id))
	if err != nil {
		return err
	}
	if sameFile(path, sourcePath) {
		return fmt.Errorf("rendered output must be separate from cached source audio")
	}
	if err := r.Cache.CleanupWorking(id); err != nil {
		return err
	}
	if err := recoverInstall(path); err != nil {
		return fmt.Errorf("recovering render installation: %w", err)
	}
	return cleanupRenderStages(path)
}

// Recover reclaims interrupted work even for episodes outside today's source
// selection. It only touches owned staging files and verified installations.
func (r *Runner) Recover(ctx context.Context, channel config.Channel) error {
	if r.State.PurgePending(channel.Slug) {
		return r.PurgeChannel(ctx, channel)
	}
	var failures []error
	for id, episode := range r.State.Episodes(channel.Slug) {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		if youtube.VideoID(id) != id {
			continue
		}
		if err := r.reconcilePublication(ctx, channel, id, &episode); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", id, err))
			continue
		}
		if episode.Removal != "" {
			if episode.DeletePending {
				if err := r.finishRemoval(ctx, channel, id, episode); err != nil {
					failures = append(failures, fmt.Errorf("%s: %w", id, err))
				}
			}
			continue
		}
		if err := r.cleanupRevisions(ctx, channel, id, &episode); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", id, err))
			continue
		}
		path := episode.RenderPath
		if path == "" {
			path = RenderPath(r.Cache, channel, id)
		}
		path, err := fileutil.CanonicalPath(path)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", id, err))
			continue
		}
		if episode.Stage == state.Published && episode.RecordPath != "" {
			if err := r.cleanupPublishedRender(channel, id, episode); err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", id, err))
			}
			continue
		}
		if episode.RecordPath == "" {
			if err := r.prepareArtifact(path, id); err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", id, err))
				continue
			}
		}
		if episode.Stage == state.Published {
			continue
		}
		ready, err := recoverRender(ctx, path, channel.Slug, id, &episode)
		if err == nil && ready {
			episode.Stage, episode.LastError = state.Rendered, ""
			err = r.State.Save(channel.Slug, id, episode)
			if err == nil {
				err = r.Cache.RemoveAudio(id)
			}
		}
		if err != nil {
			episode.LastError = err.Error()
			saveErr := r.State.Save(channel.Slug, id, episode)
			// A broken unpublished replacement leaves the committed
			// publication intact, so it is recorded on the episode rather
			// than blocking the channel's other work.
			if saveErr == nil && episode.HasPublished && !episode.PublishPending {
				continue
			}
			failures = append(failures, fmt.Errorf("%s: %w", id, errors.Join(err, saveErr)))
		}
	}
	return errors.Join(failures...)
}

// ReadyToPublish returns verified checkpoints after Recover has run. File
// existence alone is insufficient: a sidecar may belong to an earlier attempt.
// Waiting and unfinished renders remain governed by the latest-N window.
func (r *Runner) ReadyToPublish(channel config.Channel) []string {
	var ids []string
	for id, episode := range r.State.Episodes(channel.Slug) {
		if episode.Removal == "" && !episode.DeletePending && episode.PublishPending && episode.Stage == state.Rendered {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func recoverRender(ctx context.Context, path, channel, id string, episode *state.Episode) (bool, error) {
	data, err := os.ReadFile(recordPath(path, *episode))
	if os.IsNotExist(err) {
		// Explicitly imported files have their verification checkpoint in state.
		if episode.RenderPath == path && episode.AudioSHA256 != "" {
			hash, size, err := audioDigest(path)
			if err != nil {
				return false, err
			}
			if hash != episode.AudioSHA256 || size != episode.AudioBytes {
				return false, fmt.Errorf("finished audio changed; explicitly render or import it again")
			}
			return true, nil
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var record RenderRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return false, fmt.Errorf("reading render record: %w", err)
	}
	if (record.Version != 1 && record.Version != 2) || record.VideoID != id || record.Channel != channel || record.DurationSeconds <= 0 || (record.Source != nil && record.Source.ID != id) {
		return false, fmt.Errorf("render record does not match episode %s/%s", channel, id)
	}
	if episode.RenderID != "" && record.RenderID != episode.RenderID {
		return false, nil
	}
	hash, size, err := audioDigest(path)
	if err != nil {
		return false, err
	}
	if record.AudioSHA256 != "" && (hash != record.AudioSHA256 || size != record.AudioBytes) {
		return false, fmt.Errorf("finished audio failed checksum verification; explicitly render it again")
	}
	if record.AudioSHA256 == "" {
		// Earlier releases wrote sidecars without a checksum. Verify that file
		// before adopting it; never invent historical model or bitrate settings.
		duration, err := render.Probe(ctx, path)
		if err != nil {
			return false, err
		}
		if d := duration.Seconds() - record.DurationSeconds; d < -0.1 || d > 0.1 {
			return false, fmt.Errorf("audio duration does not match its render record")
		}
	}
	episode.RenderPath, episode.AudioSHA256, episode.AudioBytes = path, hash, size
	episode.DurationSeconds = record.DurationSeconds
	episode.RenderID = record.RenderID
	if episode.Video == nil && record.Source != nil {
		episode.Video = record.Source
	}
	return true, nil
}

func inspectImportedAudio(ctx context.Context, path string, episode *state.Episode) error {
	duration, err := render.Probe(ctx, path)
	if err != nil {
		return err
	}
	if duration <= 0 {
		return fmt.Errorf("imported audio is empty")
	}
	hash, size, err := audioDigest(path)
	if err != nil {
		return err
	}
	episode.RenderPath, episode.AudioSHA256, episode.AudioBytes = path, hash, size
	episode.DurationSeconds = duration.Seconds()
	episode.RenderID = ""
	episode.RecordPath = ""
	return nil
}

func recordPath(path string, episode state.Episode) string {
	if episode.RenderPath == path && episode.RecordPath != "" {
		return episode.RecordPath
	}
	return path + ".json"
}
