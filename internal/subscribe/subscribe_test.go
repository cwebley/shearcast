package subscribe

import (
	"encoding/xml"
	"strings"
	"testing"
)

var shows = []Show{
	{Title: "Tom & Jerry <sheared>", FeedURL: "https://pub.example/tj/feed.xml"},
	{Title: "Space", FeedURL: "https://pub.example/space/feed.xml"},
}

func TestOPMLRoundTripsTitlesAndURLs(t *testing.T) {
	data, err := OPML(shows)
	if err != nil {
		t.Fatal(err)
	}
	var doc opml
	if err := xml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("OPML does not parse: %v\n%s", err, data)
	}
	if len(doc.Body) != 2 || doc.Body[0].Title != shows[0].Title || doc.Body[0].XMLURL != shows[0].FeedURL || doc.Body[0].Type != "rss" {
		t.Fatalf("outlines = %+v", doc.Body)
	}
}

func TestPageLinksEachShowToAntennaPod(t *testing.T) {
	data, err := Page(shows)
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		// An https link survives browsers, which rewrite a custom scheme
		// followed by "https://" into "antennapod-subscribe://https//...".
		`href="https://antennapod.org/deeplink/subscribe?url=https%3A%2F%2Fpub.example%2Ftj%2Ffeed.xml"`,
		`href="https://antennapod.org/deeplink/subscribe?url=https%3A%2F%2Fpub.example%2Fspace%2Ffeed.xml"`,
		`Tom &amp; Jerry &lt;sheared&gt;`,
		`href="feeds.opml"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if strings.Contains(page, "ZgotmplZ") {
		t.Error("html/template rejected a subscribe link")
	}
}
