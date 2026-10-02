package state

import (
	"path/filepath"
	"testing"

	"github.com/cwebley/shearcast/internal/chapters"
	"github.com/cwebley/shearcast/internal/render"
	"github.com/cwebley/shearcast/internal/youtube"
)

func TestChapterSnapshotsOwnTheirSlices(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	end := 4.0
	ep := Episode{Stage: Published, Video: &youtube.Video{Chapters: []chapters.Source{{Start: 0, End: &end, Title: "Intro"}}}, PublishedTimeline: &chapters.Timeline{Keep: []render.Range{{Start: 0, End: 4}}}, ChapterKeys: []string{"original"}}
	if err := s.Save("show", "episode", ep); err != nil {
		t.Fatal(err)
	}
	mutate := func(e Episode) {
		e.Video.Chapters[0].Title = "mutated"
		*e.Video.Chapters[0].End = 8
		e.PublishedTimeline.Keep[0].End = 8
		e.ChapterKeys[0] = "mutated"
	}
	mutate(ep)
	got, _ := s.Episode("show", "episode")
	mutate(got)
	mutate(s.Episodes("show")["episode"])
	got, _ = s.Episode("show", "episode")
	if got.Video.Chapters[0].Title != "Intro" || *got.Video.Chapters[0].End != 4 || got.PublishedTimeline.Keep[0].End != 4 || got.ChapterKeys[0] != "original" {
		t.Fatalf("state aliases caller: %+v", got)
	}
}
