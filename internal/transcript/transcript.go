// Package transcript turns a WebVTT caption track into the windows we ask Jev
// about. YouTube's auto-generated captions are the messy case: cues repeat the
// previous cue's text as they roll, and carry inline word-level timing tags.
package transcript

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Cue is one caption line with its timing.
type Cue struct {
	Start float64
	End   float64
	Text  string
}

// Window is a run of cues we ask a single question about.
type Window struct {
	ID    string
	Start float64
	End   float64
	Text  string
	Cues  []Cue
}

func (w Window) Duration() float64 { return w.End - w.Start }

var (
	timingLine = regexp.MustCompile(`^\s*(\d{1,3}:)?\d{1,2}:\d{2}[.,]\d{1,3}\s*-->\s*(\d{1,3}:)?\d{1,2}:\d{2}[.,]\d{1,3}`)
	inlineTag  = regexp.MustCompile(`<[^>]*>`)
	manySpaces = regexp.MustCompile(`\s+`)
)

// ParseVTT reads a WebVTT track and returns de-duplicated cues in order.
func ParseVTT(r io.Reader) ([]Cue, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var (
		cues    []Cue
		pending *Cue
		body    []string
	)

	flush := func() {
		if pending == nil {
			return
		}
		text := clean(strings.Join(body, " "))
		if text != "" {
			pending.Text = text
			cues = append(cues, *pending)
		}
		pending, body = nil, nil
	}

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)

		if timingLine.MatchString(trimmed) {
			flush()
			start, end, err := parseTiming(trimmed)
			if err != nil {
				return nil, err
			}
			pending = &Cue{Start: start, End: end}
			continue
		}
		if pending == nil {
			continue // header, NOTE block, or a cue identifier we don't need
		}
		if trimmed == "" {
			// YouTube's auto-captions often put a whitespace-only line directly
			// after the timing line. Only a blank line following real text ends
			// the cue; a leading one is padding.
			if len(body) > 0 {
				flush()
			}
			continue
		}
		body = append(body, trimmed)
	}
	flush()

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading vtt: %w", err)
	}
	return dedupe(cues), nil
}

// dedupe strips the rolling repetition in YouTube's auto captions, where each
// cue restates the tail of the one before it.
func dedupe(cues []Cue) []Cue {
	out := make([]Cue, 0, len(cues))
	var prev string
	for _, c := range cues {
		text := c.Text
		switch {
		case text == prev:
			continue
		case prev != "" && strings.HasPrefix(text, prev+" "):
			text = strings.TrimSpace(text[len(prev):])
		case prev != "":
			if tail := overlapSuffix(prev, text); tail > 0 {
				text = strings.TrimSpace(text[tail:])
			}
		}
		if text == "" {
			continue
		}
		prev = c.Text
		c.Text = text
		out = append(out, c)
	}
	return out
}

// overlapSuffix finds how much of next's head duplicates prev's tail, matching
// on word boundaries so we never slice a word in half.
func overlapSuffix(prev, next string) int {
	prevWords := strings.Fields(prev)
	nextWords := strings.Fields(next)
	maxOverlap := min(len(prevWords), len(nextWords))
	for n := maxOverlap; n > 0; n-- {
		if strings.EqualFold(strings.Join(prevWords[len(prevWords)-n:], " "), strings.Join(nextWords[:n], " ")) {
			return len(strings.Join(nextWords[:n], " "))
		}
	}
	return 0
}

// Windows groups cues into spans of roughly target seconds, preferring to break
// at a sentence end so a window rarely splits a thought mid-clause.
func Windows(cues []Cue, prefix string, target, maxLen float64) []Window {
	var (
		out     []Window
		current []Cue
	)
	emit := func() {
		if len(current) == 0 {
			return
		}
		parts := make([]string, len(current))
		for i, c := range current {
			parts[i] = c.Text
		}
		out = append(out, Window{
			ID:    fmt.Sprintf("%s%03d", prefix, len(out)+1),
			Start: current[0].Start,
			End:   current[len(current)-1].End,
			Text:  strings.Join(parts, " "),
			Cues:  append([]Cue(nil), current...),
		})
		current = nil
	}

	for _, c := range cues {
		current = append(current, c)
		span := current[len(current)-1].End - current[0].Start
		if span < target {
			continue
		}
		if span >= maxLen || endsSentence(c.Text) {
			emit()
		}
	}
	emit()
	return out
}

// Slice returns the cues overlapping [start, end].
func Slice(cues []Cue, start, end float64) []Cue {
	var out []Cue
	for _, c := range cues {
		if c.End > start && c.Start < end {
			out = append(out, c)
		}
	}
	return out
}

// Text joins windows back into a readable block, each one labelled with its ID
// so Jev's answers can be mapped back to a time range.
func Text(windows []Window) string {
	var b strings.Builder
	for _, w := range windows {
		fmt.Fprintf(&b, "[%s] %s\n", w.ID, w.Text)
	}
	return b.String()
}

func endsSentence(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	switch s[len(s)-1] {
	case '.', '?', '!':
		return true
	}
	return false
}

func clean(s string) string {
	s = inlineTag.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	return strings.TrimSpace(manySpaces.ReplaceAllString(s, " "))
}

func parseTiming(line string) (float64, float64, error) {
	parts := strings.SplitN(line, "-->", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("malformed timing line: %q", line)
	}
	start, err := parseTimestamp(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, err
	}
	// The end timestamp may be followed by cue settings such as "align:start".
	endField := strings.Fields(strings.TrimSpace(parts[1]))
	if len(endField) == 0 {
		return 0, 0, fmt.Errorf("missing end timestamp: %q", line)
	}
	end, err := parseTimestamp(endField[0])
	if err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

// parseTimestamp accepts HH:MM:SS.mmm and MM:SS.mmm, with , or . for the decimal.
func parseTimestamp(s string) (float64, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	fields := strings.Split(s, ":")
	if len(fields) < 2 || len(fields) > 3 {
		return 0, fmt.Errorf("bad timestamp %q", s)
	}
	var total float64
	for _, f := range fields {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return 0, fmt.Errorf("bad timestamp %q: %w", s, err)
		}
		total = total*60 + v
	}
	return total, nil
}

// Sentence is one sentence of the transcript with its time span. Candidate
// boundaries are sentences rather than fixed-length chunks because a narrator
// never switches into an ad read mid-clause, and because a fragment like
// "...which is why we're" cannot be judged on its own.
type Sentence struct {
	ID    string
	Start float64
	End   float64
	Text  string
}

func (s Sentence) Duration() float64 { return s.End - s.Start }

// Sentences splits cues into sentences, interpolating timing across a cue when
// a sentence ends partway through it. Sentences shorter than minChars are
// folded into a neighbor so the candidate list stays judgeable -- preferably
// the sentence right before it, since a short trailing remark ("Right. So.",
// or a short rhetorical question) usually closes out what was just said
// rather than opening what comes next. Only a short sentence with nothing
// before it yet (the very start of the range) folds forward instead.
//
// Getting this backwards is not cosmetic: a real segue on MhOCMpePvjU opened
// "Why are we here? The Dextra robot is operated remotely from Earth." --
// folding forward glued the closing line of real content onto the ad's own
// first sentence, so every question this candidate was ever asked included a
// few words of content it shouldn't have, and the resulting cut audibly
// clipped the word "Why" instead of landing on the real boundary.
func Sentences(cues []Cue, prefix string, minChars int) []Sentence {
	type piece struct {
		text       string
		start, end float64
		terminal   bool
	}

	var pieces []piece
	for _, c := range cues {
		parts := splitAfterTerminators(c.Text)
		if len(parts) == 0 || len(c.Text) == 0 {
			continue
		}
		span := c.End - c.Start
		total := float64(len(c.Text))
		offset := 0
		for _, p := range parts {
			text := strings.TrimSpace(p)
			if text != "" {
				pieces = append(pieces, piece{
					text:     text,
					start:    c.Start + span*(float64(offset)/total),
					end:      c.Start + span*(float64(offset+len(p))/total),
					terminal: endsSentence(p),
				})
			}
			offset += len(p)
		}
	}

	// Pass 1: one raw sentence per terminator, length ignored. A sentence can
	// still span several cues if none of them end it.
	type raw struct {
		text       string
		start, end float64
	}
	var (
		rawSentences []raw
		body         []string
		start, end   float64
		started      bool
	)
	flushRaw := func() {
		if !started {
			return
		}
		text := strings.TrimSpace(strings.Join(body, " "))
		if text != "" {
			rawSentences = append(rawSentences, raw{text: text, start: start, end: end})
		}
		body, started = nil, false
	}
	for _, p := range pieces {
		if !started {
			start, started = p.start, true
		}
		end = p.end
		body = append(body, p.text)
		if p.terminal {
			flushRaw()
		}
	}
	flushRaw()

	// Pass 2: fold anything shorter than minChars into a neighbor -- backward
	// when a sentence already precedes it, forward (by not emitting yet, so
	// the next raw sentence absorbs it) only when nothing does.
	var out []Sentence
	foldInto := func(r raw) {
		last := &out[len(out)-1]
		last.Text = strings.TrimSpace(last.Text + " " + r.text)
		last.End = r.end
	}
	for _, r := range rawSentences {
		switch {
		case len(out) > 0 && len(out[len(out)-1].Text) < minChars:
			// The last emitted sentence is itself still too short to stand
			// alone (nothing preceded it either); keep extending it forward.
			foldInto(r)
		case len(r.text) >= minChars || len(out) == 0:
			// Long enough alone, or nothing behind it to fold into yet.
			out = append(out, Sentence{
				ID: fmt.Sprintf("%s%03d", prefix, len(out)+1), Start: r.start, End: r.end, Text: r.text,
			})
		default:
			// Short, and something already precedes it: fold backward.
			foldInto(r)
		}
	}
	return out
}

// splitAfterTerminators cuts a string after each sentence terminator, keeping
// the terminator attached to the text it ends.
func splitAfterTerminators(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '.', '?', '!':
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// SentenceText renders sentences as a labelled block for a request state.
func SentenceText(ss []Sentence) string {
	var b strings.Builder
	for _, s := range ss {
		fmt.Fprintf(&b, "[%s] %s\n", s.ID, s.Text)
	}
	return b.String()
}

// SliceSentences returns the sentences overlapping [start, end].
func SliceSentences(ss []Sentence, start, end float64) []Sentence {
	var out []Sentence
	for _, s := range ss {
		if s.End > start && s.Start < end {
			out = append(out, s)
		}
	}
	return out
}
