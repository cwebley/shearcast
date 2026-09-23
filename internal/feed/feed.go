// Package feed builds a podcast RSS feed: one feed per source channel, so a
// podcast app subscribes to "History of the Universe" or "The PrimeTime"
// exactly the way it would subscribe to any other show, with no auth and no
// shared vocabulary between unrelated sources.
//
// XML is built through encoding/xml rather than string concatenation so that
// titles and descriptions containing "&", "<" or quotes are escaped
// correctly instead of producing an invalid feed.
package feed

import (
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Item is one episode.
type Item struct {
	// ID is the source video/episode id. It becomes the RSS guid, so it must
	// be stable across republishes: change it and podcast apps will treat the
	// episode as new.
	ID          string
	Title       string
	Description string
	PublishedAt time.Time
	AudioURL    string
	AudioBytes  int64
	Duration    time.Duration
}

// Feed is one channel's show, publishable as its own subscription.
type Feed struct {
	Title       string
	Description string
	// SelfURL is the feed's own public URL, required by the RSS spec's
	// atom:link rel="self" convention that most podcast apps expect.
	SelfURL  string
	Language string
	Category string
	Items    []Item
}

// Upsert adds item, or replaces the existing one with the same ID. Items are
// kept newest-first by PublishedAt, which is convention though not required
// by the spec.
func (f *Feed) Upsert(item Item) {
	for i, existing := range f.Items {
		if existing.ID == item.ID {
			f.Items[i] = item
			f.sort()
			return
		}
	}
	f.Items = append(f.Items, item)
	f.sort()
}

func (f *Feed) sort() {
	sort.Slice(f.Items, func(i, j int) bool {
		return f.Items[i].PublishedAt.After(f.Items[j].PublishedAt)
	})
}

// XML renders the feed as a complete RSS document, including the header.
func (f Feed) XML() ([]byte, error) {
	lang := f.Language
	if lang == "" {
		lang = "en-us"
	}
	category := f.Category
	if category == "" {
		category = "Technology"
	}

	channel := rssChannel{
		Title:          f.Title,
		Link:           f.SelfURL,
		Description:    f.Description,
		Language:       lang,
		ItunesExplicit: "false",
		ItunesCategory: itunesCategory{Text: category},
		AtomLink: atomLink{
			Href: f.SelfURL,
			Rel:  "self",
			Type: "application/rss+xml",
		},
	}
	for _, item := range f.Items {
		channel.Items = append(channel.Items, rssItem{
			Title:          item.Title,
			GUID:           rssGUID{IsPermaLink: "false", Value: item.ID},
			PubDate:        item.PublishedAt.Format(time.RFC1123Z),
			Description:    item.Description,
			ItunesDuration: formatDuration(item.Duration),
			Enclosure: rssEnclosure{
				URL:    item.AudioURL,
				Length: fmt.Sprintf("%d", item.AudioBytes),
				Type:   "audio/x-m4a",
			},
		})
	}

	doc := rssDocument{
		Version:     "2.0",
		XMLNSItunes: "http://www.itunes.com/dtds/podcast-1.0.dtd",
		XMLNSAtom:   "http://www.w3.org/2005/Atom",
		Channel:     channel,
	}
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling feed: %w", err)
	}
	return append([]byte(xml.Header), body...), nil
}

// Parse reads a previously rendered feed back, so a new episode can be
// upserted into it without losing the ones already there. It parses only
// what XML round-trips: a feed encountered from elsewhere with fields this
// package doesn't render is not expected to parse cleanly.
//
// SelfURL is deliberately not recovered here. It is always deterministic
// from where the feed is stored, so callers should set it themselves after
// Parse rather than trust a value read back from the file -- which also
// sidesteps a genuine XML-namespace ambiguity: the plain <link> element and
// <atom:link> both resolve to Local name "link" once namespaces are
// stripped, and atom:link is self-closing, so a naive single field tagged
// "link" gets silently clobbered back to empty by whichever element the
// decoder visits second.
func Parse(data []byte) (*Feed, error) {
	var doc rssDocumentIn
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing feed: %w", err)
	}
	f := &Feed{
		Title:       doc.Channel.Title,
		Description: doc.Channel.Description,
		Language:    doc.Channel.Language,
		Category:    doc.Channel.ItunesCategory.Text,
	}
	for _, item := range doc.Channel.Items {
		published, err := time.Parse(time.RFC1123Z, item.PubDate)
		if err != nil {
			return nil, fmt.Errorf("parsing pubDate %q: %w", item.PubDate, err)
		}
		bytes, err := strconv.ParseInt(item.Enclosure.Length, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parsing enclosure length %q: %w", item.Enclosure.Length, err)
		}
		duration, err := parseDuration(item.ItunesDuration)
		if err != nil {
			return nil, fmt.Errorf("parsing itunes:duration %q: %w", item.ItunesDuration, err)
		}
		f.Items = append(f.Items, Item{
			ID:          item.GUID.Value,
			Title:       item.Title,
			Description: item.Description,
			PublishedAt: published,
			AudioURL:    item.Enclosure.URL,
			AudioBytes:  bytes,
			Duration:    duration,
		})
	}
	f.sort()
	return f, nil
}

// parseDuration accepts itunes:duration's HH:MM:SS or MM:SS forms.
func parseDuration(s string) (time.Duration, error) {
	parts := strings.Split(s, ":")
	if len(parts) < 1 || len(parts) > 3 {
		return 0, fmt.Errorf("bad duration %q", s)
	}
	var total int64
	for _, p := range parts {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("bad duration %q: %w", s, err)
		}
		total = total*60 + v
	}
	return time.Duration(total) * time.Second, nil
}

// formatDuration renders itunes:duration as HH:MM:SS.
func formatDuration(d time.Duration) string {
	total := int(d.Round(time.Second).Seconds())
	h, m, s := total/3600, (total%3600)/60, total%60
	return fmt.Sprintf("%d:%02d:%02d", h, m, s)
}

// The struct tags below use a literal "itunes:"/"atom:" prefix rather than
// Go's namespace-URI attribute syntax. encoding/xml emits the tag name
// verbatim, and the prefix is declared once via the xmlns:itunes/xmlns:atom
// attributes on the root element, which is the same fixed-prefix approach
// nearly every podcast feed on the web actually uses.

type rssDocument struct {
	XMLName     xml.Name   `xml:"rss"`
	Version     string     `xml:"version,attr"`
	XMLNSItunes string     `xml:"xmlns:itunes,attr"`
	XMLNSAtom   string     `xml:"xmlns:atom,attr"`
	Channel     rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title          string         `xml:"title"`
	Link           string         `xml:"link"`
	AtomLink       atomLink       `xml:"atom:link"`
	Description    string         `xml:"description"`
	Language       string         `xml:"language"`
	ItunesExplicit string         `xml:"itunes:explicit"`
	ItunesCategory itunesCategory `xml:"itunes:category"`
	Items          []rssItem      `xml:"item"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type itunesCategory struct {
	Text string `xml:"text,attr"`
}

type rssItem struct {
	Title          string       `xml:"title"`
	GUID           rssGUID      `xml:"guid"`
	PubDate        string       `xml:"pubDate"`
	Enclosure      rssEnclosure `xml:"enclosure"`
	ItunesDuration string       `xml:"itunes:duration"`
	Description    string       `xml:"description"`
}

type rssGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

// The write-side structs above tag their itunes elements with the literal
// prefix, e.g. "itunes:duration" -- exactly the bytes encoding/xml writes,
// and exactly what every podcast app expects to read. That same tag cannot
// be reused to unmarshal, though: the decoder resolves "itunes:duration" via
// the xmlns:itunes declaration on read, so the parsed element's Local name
// is plain "duration". A struct tag with no namespace prefix at all matches
// on Local name only, regardless of the element's resolved namespace, so
// these mirror structs drop the prefix rather than trying to restate it.
type rssDocumentIn struct {
	Channel rssChannelIn `xml:"channel"`
}

// Note: there is no field here for <link> or atom:link. Both resolve to
// Local name "link" once namespaces are stripped on read, so a single field
// would catch both -- and atom:link is self-closing, so whichever one the
// decoder visits second silently clobbers the other. Parse doesn't need
// either value (see the SelfURL comment on Parse), so both are left unmapped
// rather than fought over.
type rssChannelIn struct {
	Title          string           `xml:"title"`
	Description    string           `xml:"description"`
	Language       string           `xml:"language"`
	ItunesCategory itunesCategoryIn `xml:"category"`
	Items          []rssItemIn      `xml:"item"`
}

type itunesCategoryIn struct {
	Text string `xml:"text,attr"`
}

type rssItemIn struct {
	Title          string       `xml:"title"`
	GUID           rssGUID      `xml:"guid"`
	PubDate        string       `xml:"pubDate"`
	Enclosure      rssEnclosure `xml:"enclosure"`
	ItunesDuration string       `xml:"duration"`
	Description    string       `xml:"description"`
}
