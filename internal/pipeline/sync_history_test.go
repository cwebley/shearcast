package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/state"
	"github.com/cwebley/shearcast/internal/youtube"
)

func TestSyncRecordsWaitingSuccessListingFailureAndDisabledSkip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	st, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v := youtube.Video{ID: lifecycleID, UploadDate: "20260923", Duration: 30}
	r := Runner{State: st, Cache: youtube.Cache{Dir: filepath.Join(dir, "cache")},
		Publisher:   &memoryPublisher{objects: map[string][]byte{}},
		Source:      &testSource{video: v, cuesErr: youtube.ErrCaptionsUnavailable},
		ListUploads: func(context.Context, string, int) ([]youtube.Video, error) { return []youtube.Video{v}, nil },
	}
	ch := config.Channel{Slug: "show", URL: "https://youtube.com/@show"}
	if _, err := r.SyncChannel(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	snapshot, err := state.ReadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	success := snapshot.Sync[ch.Slug].LastSuccess
	if success.IsZero() || snapshot.Episodes[ch.Slug][v.ID].Stage != state.Waiting {
		t.Fatal("waiting is not successful")
	}
	r.ListUploads = func(context.Context, string, int) ([]youtube.Video, error) {
		return nil, errors.New("listing unavailable")
	}
	if _, err := r.SyncChannel(context.Background(), ch); err == nil {
		t.Fatal("expected listing failure")
	}
	snapshot, _ = state.ReadSnapshot(path)
	history := snapshot.Sync[ch.Slug]
	if history.Latest.Result != "failed" || !strings.Contains(history.Latest.Error, "listing unavailable") || !history.LastSuccess.Equal(success) {
		t.Fatalf("failure history: %+v", history)
	}
	ch.Disabled = true
	if _, err := r.SyncChannel(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = state.ReadSnapshot(path)
	if snapshot.Sync[ch.Slug].Latest.Result != "skipped" || !snapshot.Sync[ch.Slug].LastSuccess.Equal(success) {
		t.Fatal("disabled channel advanced success")
	}
}
