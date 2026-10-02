package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/youtube"
)

// unavailableFixture lists uploads newest first and stages source audio for
// every public one, so a sync can render without downloading.
func unavailableFixture(t *testing.T, latest int, uploads ...youtube.Video) (*lifecycleFixture, *librarySource, config.Channel) {
	t.Helper()
	f := newLifecycleFixture(t)
	f.audio(t)
	audio, err := os.ReadFile(f.runner.Cache.SourceAudioPath(lifecycleID))
	if err != nil {
		t.Fatal(err)
	}
	source := &librarySource{videos: map[string]youtube.Video{}, errs: map[string]error{}}
	for _, v := range uploads {
		source.videos[v.ID] = v
		path := f.runner.Cache.SourceAudioPath(v.ID)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, audio, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.runner.Source = source
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) { return uploads, nil }
	ch := request(Sync).Channel
	ch.URL = "https://youtube.com/@show"
	ch.Latest, ch.Keep = latest, latest
	return f, source, ch
}

func TestSyncSkipsMembersOnlyUploadAndGivesItsSlotToThePublicOne(t *testing.T) {
	members := youtube.Video{ID: "members0001", Availability: "subscriber_only"}
	public := youtube.Video{ID: "public00001", Title: "Public", Duration: 3, UploadDate: "20260924"}
	older := youtube.Video{ID: "public00002", Title: "Older", Duration: 3, UploadDate: "20260923"}
	f, _, ch := unavailableFixture(t, 1, members, public, older)
	// An earlier release tried the members-only video and recorded a failure.
	stale := state.Episode{Stage: state.Pending, PublishPending: true, LastError: "loading metadata: yt-dlp: ERROR: members-only content"}
	if err := f.runner.State.Save(ch.Slug, members.ID, stale); err != nil {
		t.Fatal(err)
	}

	outcomes, err := f.runner.SyncChannel(context.Background(), ch)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if len(outcomes) != 2 || outcomes[0].ID != members.ID || outcomes[0].Skipped != "members-only" || outcomes[0].Error != nil ||
		outcomes[1].ID != public.ID || outcomes[1].Episode.Stage != state.Published {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if _, ok := f.runner.State.Episode(ch.Slug, members.ID); ok {
		t.Fatal("stale record for the skipped upload survived")
	}
	if _, ok := f.runner.State.Episode(ch.Slug, older.ID); ok {
		t.Fatal("processed an upload outside latest")
	}
}

func TestSyncSkipsUploadWhoseMetadataIsRefused(t *testing.T) {
	// The listing does not always flag restricted videos; yt-dlp's metadata
	// refusal is the fallback.
	hidden := youtube.Video{ID: "hidden00001"}
	public := youtube.Video{ID: "public00001", Title: "Public", Duration: 3, UploadDate: "20260924"}
	f, source, ch := unavailableFixture(t, 2, hidden, public)
	refusal := &youtube.UnavailableError{Reason: "members-only", Err: errors.New("yt-dlp: ERROR: members-only content")}
	source.errs[hidden.ID] = refusal

	outcomes, err := f.runner.SyncChannel(context.Background(), ch)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if len(outcomes) != 2 || outcomes[0].Skipped != "members-only" || outcomes[0].Error != nil || outcomes[1].Episode.Stage != state.Published {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if _, ok := f.runner.State.Episode(ch.Slug, hidden.ID); ok {
		t.Fatal("skipped upload left a record")
	}

	// Asking for this video explicitly is still an error.
	req := EpisodeRequest{Action: Render, Channel: ch, Target: hidden.ID}
	if _, err := f.runner.Run(context.Background(), req); !errors.Is(err, youtube.ErrUnavailable) {
		t.Fatalf("explicit render: %v", err)
	}
}

func TestPlanListsSkippedUploadsOutsideTheWindow(t *testing.T) {
	members := youtube.Video{ID: "members0001", Availability: "subscriber_only"}
	hidden := youtube.Video{ID: "hidden00001"} // unflagged; metadata is refused
	public := youtube.Video{ID: "public00001", Title: "Public", Duration: 3, UploadDate: "20260924"}
	older := youtube.Video{ID: "public00002", Title: "Older", Duration: 3, UploadDate: "20260923"}
	f, source, ch := unavailableFixture(t, 2, members, hidden, public, older)
	source.errs[hidden.ID] = &youtube.UnavailableError{Reason: "private", Err: errors.New("yt-dlp: ERROR: Private video")}

	plan, err := f.runner.Plan(context.Background(), ch)
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	rows := map[string]PlannedEpisode{}
	for _, row := range plan.Episodes {
		rows[row.ID] = row
	}
	if row := rows[members.ID]; row.Skipped != "members-only" || row.Selected || row.NeedsProcessing {
		t.Fatalf("members-only row = %+v", row)
	}
	if row := rows[hidden.ID]; row.Skipped != "private" || row.Selected || row.NeedsProcessing {
		t.Fatalf("refused row = %+v", row)
	}
	if row := rows[public.ID]; !row.Selected || !row.NeedsProcessing {
		t.Fatalf("public row = %+v", row)
	}
	if plan.SourceSeconds != 3 || plan.UnknownDurations != 0 {
		t.Fatalf("skipped uploads counted toward the window: %+v", plan)
	}
}

func TestSourceOrderKeepsSkippedUploadsPositions(t *testing.T) {
	// The newest upload went members-only. It is still newer than the public
	// one after it, so retention must not order the public one first.
	f := newLifecycleFixture(t)
	f.runner.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) {
		return []youtube.Video{{ID: "members0001", Availability: "subscriber_only"}, {ID: "public00001"}}, nil
	}
	ch := request(Sync).Channel
	ch.URL, ch.Latest, ch.Keep = "https://youtube.com/@show", 1, 1
	_, skipped, order, err := f.runner.uploads(context.Background(), ch)
	if err != nil || len(skipped) != 1 || order["members0001"] != 0 || order["public00001"] != 1 {
		t.Fatalf("order %v skipped %v err %v", order, skipped, err)
	}
}
