// Package chapters maps source headings onto a verified edited audio timeline.
// Original metadata stays with the caller; public descriptions and chapter lists
// are derived together so repeated refreshes never adjust an adjusted timestamp.
package chapters

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/cwebley/shearcast/internal/render"
)

// Source is yt-dlp's chapter shape. A missing end is inferred from the next
// start or the original duration, never from the shortened audio's duration.
type Source struct {
	Start float64  `json:"start_time"`
	End   *float64 `json:"end_time,omitempty"`
	Title string   `json:"title"`
}

func CloneSource(in []Source) []Source {
	if in == nil {
		return nil
	}
	out := append([]Source{}, in...)
	for i := range out {
		if in[i].End != nil {
			end := *in[i].End
			out[i].End = &end
		}
	}
	return out
}

// Timeline is a private snapshot of the final retained ranges and effective
// splice settings for one audio file. It must not follow a pending replacement.
type Timeline struct {
	Keep           []render.Range `json:"keep"`
	Crossfade      float64        `json:"crossfade"`
	SourceDuration float64        `json:"source_duration"`
	Duration       float64        `json:"duration"`
}

func (t *Timeline) Clone() *Timeline {
	if t == nil {
		return nil
	}
	c := *t
	c.Keep = append([]render.Range(nil), t.Keep...)
	return &c
}

// Validate checks the millisecond trim/overlap timeline against the probed file.
// The tolerance covers AAC/container and sample rounding, not missing cut maps.
func (t *Timeline) Validate() error {
	if t == nil || len(t.Keep) == 0 || !finite(t.SourceDuration) || t.SourceDuration <= 0 || !finite(t.Duration) || t.Duration <= 0 || !finite(t.Crossfade) || t.Crossfade < 0 {
		return fmt.Errorf("chapter timeline is missing or invalid")
	}
	fade := milliseconds(t.Crossfade)
	total, previous := 0.0, 0.0
	for i, r := range t.Keep {
		if !finite(r.Start) || !finite(r.End) || r.Start < previous || r.End > t.SourceDuration+0.001 || r.Start < 0 {
			return fmt.Errorf("chapter timeline contains invalid retained ranges")
		}
		length := milliseconds(r.End) - milliseconds(r.Start)
		if length <= 0 || len(t.Keep) > 1 && length < fade {
			return fmt.Errorf("chapter timeline has a range shorter than its splice")
		}
		total += length
		if i > 0 {
			total -= fade
		}
		previous = r.End
	}
	if math.Abs(total-t.Duration) > 0.25 {
		return fmt.Errorf("chapter timeline duration %.3f does not match audio %.3f", total, t.Duration)
	}
	return nil
}

type Chapter struct {
	Start float64 `json:"startTime"`
	Title string  `json:"title"`
}

const ContentType = "application/json+chapters"

func JSON(list []Chapter) ([]byte, error) {
	return json.MarshalIndent(struct {
		Version  string    `json:"version"`
		Chapters []Chapter `json:"chapters"`
	}{"1.2.0", list}, "", "  ")
}

// Derive uses the first surviving part of each topic, omits fully cut topics,
// and keeps the first source heading when canonical starts coincide.
// Nil timeline means an import with unknown edits: source times are labeled,
// and no navigable chapter list is emitted.
func Derive(description string, source []Source, timeline *Timeline) (string, []Chapter, error) {
	description = html.UnescapeString(description)
	if timeline == nil {
		if len(source) > 0 || hasTimestampLines(description) {
			description = "Source timestamps below refer to the original video, not the edited audio.\n\n" + description
		}
		return description, nil, nil
	}
	if len(source) == 0 {
		return description, nil, nil
	}
	if err := timeline.Validate(); err != nil {
		return "", nil, err
	}
	ordered := CloneSource(source)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	var list []Chapter
	for i, c := range ordered {
		if !finite(c.Start) || c.Start < 0 || c.Start >= timeline.SourceDuration || !sourceTitle(c.Title) {
			continue
		}
		end := timeline.SourceDuration
		for _, next := range ordered[i+1:] {
			if finite(next.Start) && next.Start > c.Start {
				end = math.Min(end, next.Start)
				break
			}
		}
		if c.End != nil {
			if !finite(*c.End) || *c.End <= c.Start {
				continue
			}
			end = math.Min(end, *c.End)
		}
		offset := 0.0
		for n, keep := range timeline.Keep {
			start, stop := milliseconds(keep.Start), milliseconds(keep.End)
			if n > 0 {
				offset -= milliseconds(timeline.Crossfade)
			}
			first := math.Max(start, c.Start)
			if first < math.Min(stop, end) {
				at := milliseconds(math.Max(0, offset+first-start))
				if at < timeline.Duration {
					list = append(list, Chapter{Start: at, Title: strings.TrimSpace(html.UnescapeString(c.Title))})
				}
				break
			}
			offset += stop - start
		}
	}
	// Crossfade overlap can put a later topic just before the previous topic.
	sort.SliceStable(list, func(i, j int) bool { return list[i].Start < list[j].Start })
	unique := list[:0]
	for _, c := range list {
		if len(unique) == 0 || unique[len(unique)-1].Start != c.Start {
			unique = append(unique, c)
		}
	}
	return rewrite(description, source, unique), unique, nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func milliseconds(v float64) float64 {
	// Match FFmpeg's formatted arguments, including rounding at half milliseconds.
	out, _ := strconv.ParseFloat(fmt.Sprintf("%.3f", v), 64)
	return out
}

var placeholder = regexp.MustCompile(`^<Untitled Chapter [0-9]+>$`)

func sourceTitle(s string) bool {
	s = strings.TrimSpace(html.UnescapeString(s))
	return s != "" && !placeholder.MatchString(s)
}

// Time formats Podlove NPT, or whole-second readable timestamps.
func Time(seconds float64, fractional bool) string {
	millis := int64(math.Round(seconds * 1000))
	if !fractional {
		millis = int64(math.Round(seconds)) * 1000
	}
	total := millis / 1000
	s := fmt.Sprintf("%02d:%02d:%02d", total/3600, total/60%60, total%60)
	if fractional {
		s += fmt.Sprintf(".%03d", millis%1000)
	}
	return s
}

var timestampLine = regexp.MustCompile(`^\s*(?:[-*•]\s*)?(\d{1,3}:\d{2}(?::\d{2})?)\s*(?:[-–—|:]\s*)?(.+?)\s*$`)
var trailingTimestamp = regexp.MustCompile(`^\s*(.+?)\s+[-–—|]?\s*(\d{1,3}:\d{2}(?::\d{2})?)\s*$`)

func lineChapter(line string) (float64, string, bool) {
	m := timestampLine.FindStringSubmatch(line)
	if m == nil {
		m = trailingTimestamp.FindStringSubmatch(line)
		if m == nil {
			return 0, "", false
		}
		m[1], m[2] = m[2], m[1]
	}
	seconds := 0.0
	for i, part := range strings.Split(m[1], ":") {
		v, err := strconv.Atoi(part)
		if err != nil || i > 0 && v >= 60 {
			return 0, "", false
		}
		seconds = seconds*60 + float64(v)
	}
	return seconds, strings.TrimSpace(m[2]), true
}

func titleKey(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(html.UnescapeString(title))), " "))
}

func hasTimestampLines(description string) bool {
	for _, line := range strings.Split(description, "\n") {
		if _, _, ok := lineChapter(line); ok {
			return true
		}
	}
	return false
}

func rewrite(description string, source []Source, list []Chapter) string {
	var adjusted []string
	for _, c := range list {
		adjusted = append(adjusted, Time(c.Start, false)+" "+c.Title)
	}
	newline := "\n"
	if strings.Contains(description, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(description, newline)
	var out []string
	replaced := false
	for _, line := range lines {
		at, title, ok := lineChapter(line)
		match := false
		if ok {
			for _, c := range source {
				if finite(c.Start) && math.Abs(c.Start-at) <= 0.5 && titleKey(c.Title) == titleKey(title) {
					match = true
					break
				}
			}
		}
		if !match {
			out = append(out, line)
			continue
		}
		if !replaced {
			out = append(out, adjusted...)
			replaced = true
		}
	}
	result := strings.Join(out, newline)
	if !replaced && len(adjusted) > 0 {
		if result != "" {
			result += newline + newline
		}
		result += "Chapters" + newline + strings.Join(adjusted, newline)
	}
	return result
}
