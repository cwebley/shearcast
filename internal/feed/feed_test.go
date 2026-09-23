package feed

import (
	"strings"
	"testing"
	"time"
)

func TestXMLEscapesSpecialCharacters(t *testing.T) {
	f := Feed{
		Title:       `Tom & Jerry's "Show"`,
		Description: "desc",
		SelfURL:     "https://example.com/feed.xml",
	}
	f.Upsert(Item{
		ID:          "abc123",
		Title:       "A <sponsored> episode & more",
		Description: "d",
		PublishedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		AudioURL:    "https://example.com/abc123.m4a",
		AudioBytes:  1024,
		Duration:    90 * time.Second,
	})

	out, err := f.XML()
	if err != nil {
		t.Fatalf("XML: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "<sponsored>") {
		t.Error("title was not escaped: raw <sponsored> tag leaked into the XML")
	}
	if !strings.Contains(s, "&amp;") {
		t.Error("expected an escaped ampersand somewhere in the feed")
	}
	if !strings.Contains(s, `<itunes:duration>0:01:30</itunes:duration>`) {
		t.Errorf("duration not rendered as expected: %s", s)
	}
	if !strings.Contains(s, `isPermaLink="false"`) {
		t.Error("guid should be marked isPermaLink=false")
	}
}

func TestUpsertReplacesByID(t *testing.T) {
	f := Feed{Title: "t", SelfURL: "https://example.com/feed.xml"}
	f.Upsert(Item{ID: "a", Title: "first", PublishedAt: time.Unix(100, 0)})
	f.Upsert(Item{ID: "a", Title: "replaced", PublishedAt: time.Unix(100, 0)})
	if len(f.Items) != 1 {
		t.Fatalf("got %d items, want 1 (upsert should replace, not append)", len(f.Items))
	}
	if f.Items[0].Title != "replaced" {
		t.Errorf("got title %q, want %q", f.Items[0].Title, "replaced")
	}
}

func TestUpsertOrdersNewestFirst(t *testing.T) {
	f := Feed{Title: "t", SelfURL: "https://example.com/feed.xml"}
	f.Upsert(Item{ID: "old", PublishedAt: time.Unix(100, 0)})
	f.Upsert(Item{ID: "new", PublishedAt: time.Unix(200, 0)})
	if f.Items[0].ID != "new" {
		t.Errorf("got %q first, want the newer item first", f.Items[0].ID)
	}
}

func TestParseRoundTripsXML(t *testing.T) {
	f := Feed{
		Title:       "History of the Universe",
		Description: "de-sponsored episodes",
		SelfURL:     "https://pub-xxxx.r2.dev/hotu/feed.xml",
		Language:    "en-us",
		Category:    "Science",
	}
	f.Upsert(Item{
		ID:          "vid1",
		Title:       "Episode One",
		Description: "the first one",
		PublishedAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("", -4*3600)),
		AudioURL:    "https://pub-xxxx.r2.dev/hotu/vid1.m4a",
		AudioBytes:  123456,
		Duration:    3725 * time.Second, // 1:02:05
	})

	xmlBytes, err := f.XML()
	if err != nil {
		t.Fatalf("XML: %v", err)
	}
	got, err := Parse(xmlBytes)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// SelfURL is deliberately not recovered by Parse -- callers set it fresh
	// from the store instead (see Parse's doc comment).
	if got.Title != f.Title || got.Description != f.Description {
		t.Errorf("channel fields did not round-trip: got %+v", got)
	}
	if len(got.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(got.Items))
	}
	item := got.Items[0]
	if item.ID != "vid1" || item.Title != "Episode One" || item.AudioBytes != 123456 {
		t.Errorf("item did not round-trip: got %+v", item)
	}
	if item.Duration != 3725*time.Second {
		t.Errorf("duration: got %v, want 1h2m5s", item.Duration)
	}
	if !item.PublishedAt.Equal(f.Items[0].PublishedAt) {
		t.Errorf("pubDate: got %v, want %v", item.PublishedAt, f.Items[0].PublishedAt)
	}
}

func TestUpsertPreservesOtherItemsOnRepublish(t *testing.T) {
	f := Feed{Title: "t", SelfURL: "https://example.com/feed.xml"}
	f.Upsert(Item{ID: "a", Title: "A", PublishedAt: time.Unix(100, 0)})
	f.Upsert(Item{ID: "b", Title: "B", PublishedAt: time.Unix(200, 0)})

	xmlBytes, err := f.XML()
	if err != nil {
		t.Fatalf("XML: %v", err)
	}
	reloaded, err := Parse(xmlBytes)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	reloaded.Upsert(Item{ID: "c", Title: "C", PublishedAt: time.Unix(300, 0)})
	if len(reloaded.Items) != 3 {
		t.Fatalf("got %d items after adding a third, want 3", len(reloaded.Items))
	}
}
