package feed

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cwebley/shearcast/internal/chapters"
)

func TestChapterFormatsSurviveUnrelatedFeedUpdates(t *testing.T) {
	inline := []chapters.Chapter{{Start: 0, Title: "Opening & context"}, {Start: 779.95, Title: "Stars < galaxies"}}
	f := Feed{Items: []Item{
		{ID: "inline", PublicationID: "opaque-publication-marker", PublishedAt: time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC), Chapters: inline},
		{ID: "json", PublishedAt: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC), ChaptersURL: "https://example.test/show/chapters.json"},
	}}
	for range 2 {
		data, err := f.XML()
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`xmlns:psc="http://podlove.org/simple-chapters"`, `start="00:12:59.950"`, `type="application/json+chapters"`, `xmlns:podcast="https://podcastindex.org/namespace/1.0"`} {
			if !strings.Contains(string(data), want) {
				t.Fatalf("missing %s: %s", want, data)
			}
		}
		parsed, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Items[0].PublicationID != "opaque-publication-marker" {
			t.Fatal("lost publication commit marker")
		}
		if !reflect.DeepEqual(parsed.Items[0].Chapters, inline) || parsed.Items[0].ChaptersURL != "" || len(parsed.Items[1].Chapters) != 0 || parsed.Items[1].ChaptersURL != f.Items[1].ChaptersURL {
			t.Fatalf("roundtrip: %#v", parsed.Items)
		}
		parsed.Upsert(Item{ID: "unrelated", PublishedAt: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)})
		f = *parsed
	}
}
