// Package subscribe builds the subscription page and OPML list published
// beside the channel feeds, so a phone can add every show from one address
// instead of each feed URL being typed in by hand.
//
// Both files live under Dir, which config reserves so no channel slug can
// collide with it. The page links each show through AntennaPod's
// subscribe deep link and offers the OPML file for a bulk import.
package subscribe

import (
	"bytes"
	"encoding/xml"
	"html/template"
	"net/url"
)

const (
	Dir      = "subscribe"
	PageKey  = Dir + "/index.html"
	OPMLKey  = Dir + "/feeds.opml"
	PageType = "text/html; charset=utf-8"
	OPMLType = "text/x-opml; charset=utf-8"
	opmlName = "shearcast-feeds.opml"
)

// Show is one channel feed to list.
type Show struct {
	Title   string
	FeedURL string
}

type opml struct {
	XMLName xml.Name  `xml:"opml"`
	Version string    `xml:"version,attr"`
	Title   string    `xml:"head>title"`
	Body    []outline `xml:"body>outline"`
}

type outline struct {
	Type   string `xml:"type,attr"`
	Text   string `xml:"text,attr"`
	Title  string `xml:"title,attr"`
	XMLURL string `xml:"xmlUrl,attr"`
}

// OPML returns the standard subscription-list format podcast apps import.
func OPML(shows []Show) ([]byte, error) {
	doc := opml{Version: "2.0", Title: "shearcast feeds"}
	for _, s := range shows {
		doc.Body = append(doc.Body, outline{Type: "rss", Text: s.Title, Title: s.Title, XMLURL: s.FeedURL})
	}
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(body, '\n')...), nil
}

// Page returns the phone-facing HTML. Links are relative to PageKey, so the
// page works unchanged on R2 and behind `shearcast serve`.
func Page(shows []Show) ([]byte, error) {
	var buf bytes.Buffer
	err := page.Execute(&buf, struct {
		Shows    []Show
		OPMLName string
	}{shows, opmlName})
	return buf.Bytes(), err
}

// subscribeLink is AntennaPod's https deep link: the app opens its subscribe
// screen for the url parameter, and without the app the site explains how to
// subscribe. The antennapod-subscribe:// scheme is unusable for https feeds:
// browsers parse "https" as its host and drop the colon, so the app received
// "https//host/feed.xml" and failed to resolve a host named "https".
func subscribeLink(feedURL string) template.URL {
	return template.URL("https://antennapod.org/deeplink/subscribe?url=" + url.QueryEscape(feedURL))
}

var page = template.Must(template.New("page").Funcs(template.FuncMap{"subscribe": subscribeLink}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Shearcast feeds</title>
<style>
:root { --bg: #fafaf9; --fg: #1c1917; --muted: #57534e; --line: #e7e5e4; --card: #fff; --accent: #0f766e; --on-accent: #fff; }
@media (prefers-color-scheme: dark) {
  :root { --bg: #1c1917; --fg: #f5f5f4; --muted: #a8a29e; --line: #44403c; --card: #292524; --accent: #2dd4bf; --on-accent: #042f2e; }
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--fg); font: 16px/1.5 system-ui, sans-serif; }
main { max-width: 36rem; margin: 0 auto; padding: 24px 16px 48px; }
h1 { font-size: 1.5rem; margin: 0 0 4px; }
p { color: var(--muted); margin: 0 0 16px; }
.button { display: block; text-align: center; padding: 14px 16px; border-radius: 10px; background: var(--accent); color: var(--on-accent); font-weight: 600; text-decoration: none; }
.all { margin-bottom: 8px; }
ul { list-style: none; padding: 0; margin: 24px 0 0; }
li { background: var(--card); border: 1px solid var(--line); border-radius: 12px; padding: 14px 16px; margin-bottom: 12px; }
.title { font-weight: 600; margin-bottom: 10px; }
.url { display: block; color: var(--muted); font-size: 0.8rem; word-break: break-all; margin-top: 10px; }
</style>
</head>
<body>
<main>
<h1>Shearcast feeds</h1>
<p>Tap a show to subscribe in AntennaPod.</p>
<ul>
{{- range .Shows}}
<li>
<div class="title">{{.Title}}</div>
<a class="button" href="{{subscribe .FeedURL}}">Subscribe</a>
<a class="url" href="{{.FeedURL}}">{{.FeedURL}}</a>
</li>
{{- end}}
</ul>
<p>Or add them all at once: download the list, then in AntennaPod choose Add podcast, Import OPML file.</p>
<a class="button all" href="feeds.opml" download="{{.OPMLName}}">Download all feeds (OPML)</a>
</main>
</body>
</html>
`))
