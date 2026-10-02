package transcript

import (
	"math"
	"strings"
	"testing"
)

func eq(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// A realistic slice of YouTube auto-captions: rolling repetition, inline word
// timing tags, and cue settings trailing the end timestamp.
const autoCaptions = `WEBVTT
Kind: captions
Language: en

00:00:00.080 --> 00:00:03.520 align:start position:0%

the universe<00:00:00.719><c> is</c><00:00:01.040><c> vast</c>

00:00:03.520 --> 00:00:03.530 align:start position:0%
the universe is vast


00:00:03.530 --> 00:00:06.900 align:start position:0%
the universe is vast
and<00:00:04.000><c> mostly</c><00:00:04.400><c> empty.</c>

00:00:06.900 --> 00:00:10.100 align:start position:0%
and mostly empty.
but<00:00:07.200><c> not</c><00:00:07.600><c> entirely</c>
`

func TestParseVTTAutoCaptions(t *testing.T) {
	cues, err := ParseVTT(strings.NewReader(autoCaptions))
	if err != nil {
		t.Fatalf("ParseVTT: %v", err)
	}
	want := []string{"the universe is vast", "and mostly empty.", "but not entirely"}
	if len(cues) != len(want) {
		t.Fatalf("got %d cues %v, want %d", len(cues), cues, len(want))
	}
	for i, w := range want {
		if cues[i].Text != w {
			t.Errorf("cue %d: got %q, want %q", i, cues[i].Text, w)
		}
	}
	if !eq(cues[0].Start, 0.08) || !eq(cues[0].End, 3.52) {
		t.Errorf("first cue timing: got %v-%v, want 0.08-3.52", cues[0].Start, cues[0].End)
	}
}

func TestParseTimestampForms(t *testing.T) {
	cases := map[string]float64{
		"00:00:01.500": 1.5,
		"01:02:03.250": 3723.25,
		"02:03.500":    123.5,
		"00:00:01,250": 1.25, // SRT-style comma
	}
	for in, want := range cases {
		got, err := parseTimestamp(in)
		if err != nil {
			t.Errorf("%q: unexpected error %v", in, err)
			continue
		}
		if !eq(got, want) {
			t.Errorf("%q: got %v, want %v", in, got, want)
		}
	}
	if _, err := parseTimestamp("nonsense"); err == nil {
		t.Error("expected an error for a malformed timestamp")
	}
}

func TestCleanStripsTagsAndEntities(t *testing.T) {
	got := clean(`<c.colorE5E5E5>Bob&#39;s</c> &amp; <i>friends</i>   say   &quot;hi&quot;`)
	want := `Bob's & friends say "hi"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDedupeOverlapNotJustPrefix(t *testing.T) {
	// The rolling window drops a word off the front as it advances, so the new
	// cue shares a suffix with the old one rather than repeating it whole.
	cues := []Cue{
		{Start: 0, End: 2, Text: "the universe is vast"},
		{Start: 2, End: 4, Text: "is vast and mostly empty"},
	}
	got := dedupe(cues)
	if len(got) != 2 {
		t.Fatalf("got %d cues, want 2", len(got))
	}
	if got[1].Text != "and mostly empty" {
		t.Errorf("got %q, want %q", got[1].Text, "and mostly empty")
	}
}

func TestDedupeKeepsLaterRepeats(t *testing.T) {
	// The same words said again seconds later are new speech, not a rolling
	// restatement, and keep their own text and timing.
	cues := []Cue{
		{Start: 0, End: 2, Text: "thank you for listening"},
		{Start: 8, End: 10, Text: "thank you for listening"},
		{Start: 20, End: 22, Text: "for listening and goodbye"},
	}
	got := dedupe(cues)
	if len(got) != 3 || got[1].Start != 8 || got[2].Text != "for listening and goodbye" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseVTTRejectsBrokenTiming(t *testing.T) {
	for name, timing := range map[string]string{
		"garbled end": "00:00:10.000 --> BROKEN",
		"reversed":    "00:00:12.000 --> 00:00:10.000",
		"empty":       "00:00:10.000 --> 00:00:10.000",
	} {
		vtt := "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nIntroduction.\n\n" + timing +
			"\nThis episode is sponsored by Acme.\n\n00:00:20.000 --> 00:00:22.000\nNow back to the program.\n"
		if cues, err := ParseVTT(strings.NewReader(vtt)); err == nil {
			t.Errorf("%s: dropped the broken cue instead of failing: %+v", name, cues)
		}
	}
}

func TestWindowsSnapToSentenceEnd(t *testing.T) {
	cues := []Cue{
		{Start: 0, End: 10, Text: "one"},
		{Start: 10, End: 20, Text: "two"},
		{Start: 20, End: 28, Text: "three"},
		{Start: 28, End: 34, Text: "four."}, // first sentence end past the 30s target
		{Start: 34, End: 44, Text: "five"},
		{Start: 44, End: 54, Text: "six."},
	}
	got := Windows(cues, "W", 30, 45)
	if len(got) != 2 {
		t.Fatalf("got %d windows %v, want 2", len(got), got)
	}
	if got[0].ID != "W001" || !eq(got[0].End, 34) {
		t.Errorf("first window: got %s ending %v, want W001 ending 34", got[0].ID, got[0].End)
	}
	if got[0].Text != "one two three four." {
		t.Errorf("first window text: got %q", got[0].Text)
	}
}

func TestWindowsHonorMaxLength(t *testing.T) {
	// No sentence ever ends, so the max length has to force the break.
	var cues []Cue
	for i := 0; i < 20; i++ {
		cues = append(cues, Cue{Start: float64(i) * 5, End: float64(i+1) * 5, Text: "word"})
	}
	got := Windows(cues, "W", 30, 45)
	for _, w := range got {
		if w.Duration() > 45.001 {
			t.Errorf("window %s is %.1fs, over the 45s cap", w.ID, w.Duration())
		}
	}
	if len(got) < 2 {
		t.Fatalf("got %d windows, want the 100s of cues split up", len(got))
	}
}

func TestSlice(t *testing.T) {
	cues := []Cue{
		{Start: 0, End: 10, Text: "a"},
		{Start: 10, End: 20, Text: "b"},
		{Start: 20, End: 30, Text: "c"},
		{Start: 30, End: 40, Text: "d"},
	}
	got := Slice(cues, 12, 28)
	if len(got) != 2 || got[0].Text != "b" || got[1].Text != "c" {
		t.Errorf("got %v, want cues b and c", got)
	}
}

func TestTextLabelsWindows(t *testing.T) {
	got := Text([]Window{
		{ID: "W001", Text: "hello"},
		{ID: "W002", Text: "world"},
	})
	want := "[W001] hello\n[W002] world\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSentencesSplitWithinACue(t *testing.T) {
	// One cue holds the end of a sentence and the start of the next. The split
	// has to land inside the cue, with timing interpolated across it.
	cues := []Cue{
		{Start: 0, End: 10, Text: "the cosmos exists at all. In 2013, NASA's Ames Research Center"},
		{Start: 10, End: 20, Text: "was remotely commanded from orbit."},
	}
	got := Sentences(cues, "S", 0)
	if len(got) != 2 {
		t.Fatalf("got %d sentences %v, want 2", len(got), got)
	}
	if got[0].Text != "the cosmos exists at all." {
		t.Errorf("first: got %q", got[0].Text)
	}
	if got[1].Text != "In 2013, NASA's Ames Research Center was remotely commanded from orbit." {
		t.Errorf("second: got %q", got[1].Text)
	}
	// "the cosmos exists at all." is 25 of the cue's 61 chars, so it should end
	// around 4.1s rather than at the cue boundary.
	if got[0].End < 3.5 || got[0].End > 4.8 {
		t.Errorf("first sentence ends at %.2f, want roughly 4.1 (interpolated)", got[0].End)
	}
	if !eq(got[1].End, 20) {
		t.Errorf("second sentence ends at %v, want 20", got[1].End)
	}
}

func TestSentencesFoldShortFragments(t *testing.T) {
	cues := []Cue{
		{Start: 0, End: 5, Text: "Right."},
		{Start: 5, End: 10, Text: "So."},
		{Start: 10, End: 20, Text: "Here is a genuinely long sentence with plenty of substance in it."},
	}
	got := Sentences(cues, "S", 30)
	if len(got) != 1 {
		t.Fatalf("got %d sentences %v, want 1 after folding", len(got), got)
	}
	if !strings.HasPrefix(got[0].Text, "Right. So.") {
		t.Errorf("got %q, want the fragments folded in", got[0].Text)
	}
	if !eq(got[0].Start, 0) || !eq(got[0].End, 20) {
		t.Errorf("span: got %v-%v, want 0-20", got[0].Start, got[0].End)
	}
}

func TestSentencesFoldShortTrailingQuestionBackward(t *testing.T) {
	// Real shape from MhOCMpePvjU: a short rhetorical question closes out the
	// previous sentence's setup, then an ad's segue begins immediately after.
	// Folding forward (the old behavior) glued "Why are we here?" onto the
	// segue's own first sentence, so the candidate boundary landed one word
	// into the ad's own sentence and the actual audio cut clipped "Why" mid
	// word. It belongs with what precedes it.
	cues := []Cue{
		{Start: 0, End: 8, Text: "And this is because the question can nothing exist has a twin."},
		{Start: 8, End: 10, Text: "Why are we here?"},
		{Start: 10, End: 20, Text: "The Dextra robot is operated remotely from Earth."},
	}
	got := Sentences(cues, "S", 25)
	if len(got) != 2 {
		t.Fatalf("got %d sentences %v, want 2", len(got), got)
	}
	if got[0].Text != "And this is because the question can nothing exist has a twin. Why are we here?" {
		t.Errorf("first sentence should absorb the short trailing question: got %q", got[0].Text)
	}
	if got[1].Text != "The Dextra robot is operated remotely from Earth." {
		t.Errorf("second sentence should start clean at the segue, not one word in: got %q", got[1].Text)
	}
	if !eq(got[1].Start, 10) {
		t.Errorf("second sentence should start exactly at the cue boundary (10), not drift into the question: got %v", got[1].Start)
	}
}

func TestSentencesIDsAndLabels(t *testing.T) {
	cues := []Cue{
		{Start: 0, End: 5, Text: "One sentence here."},
		{Start: 5, End: 10, Text: "Another sentence here."},
	}
	got := Sentences(cues, "S", 0)
	if len(got) != 2 || got[0].ID != "S001" || got[1].ID != "S002" {
		t.Fatalf("got %+v", got)
	}
	want := "[S001] One sentence here.\n[S002] Another sentence here.\n"
	if SentenceText(got) != want {
		t.Errorf("SentenceText: got %q, want %q", SentenceText(got), want)
	}
}

func TestSentencesHandleNoTerminator(t *testing.T) {
	cues := []Cue{{Start: 0, End: 5, Text: "no terminator anywhere in here"}}
	got := Sentences(cues, "S", 0)
	if len(got) != 1 || got[0].Text != "no terminator anywhere in here" {
		t.Fatalf("got %+v, want the trailing fragment kept", got)
	}
}

func TestSpeechEndIgnoresHowLongTheLastCueIsDisplayed(t *testing.T) {
	// I07RBedXRYA's final caption: shown 1078.96-1098.96, but whisper puts the
	// end of speech at 1083.3 and music plays to the end of the video.
	cues := []Cue{
		{Start: 1072.4, End: 1078.96, Text: "you want removed. To learn more about Incogni just check out: https://incogni.com/spacetime"},
		{Start: 1078.96, End: 1098.96, Text: "and use the code: SPACETIME for 60% off Incogni Annual"},
	}
	got := SpeechEnd(cues)
	if got < 1083.3 || got > 1088 {
		t.Errorf("got %.2f, want just after the words end at 1083.3", got)
	}
}

func TestSpeechEndSkipsAnnotationOnlyCues(t *testing.T) {
	cues := []Cue{
		{Start: 10, End: 12, Text: "Thanks for watching."},
		{Start: 12, End: 40, Text: "[Music]"},
	}
	if got := SpeechEnd(cues); got != 12 {
		t.Errorf("got %.2f, want 12: the words end within their own cue", got)
	}
	if got := SpeechEnd([]Cue{{Start: 0, End: 5, Text: "[music]"}}); got != 0 {
		t.Errorf("got %.2f for a track with no speech, want 0", got)
	}
}

func TestSentencesKeepWebAddressesWhole(t *testing.T) {
	cues := []Cue{
		{Start: 0, End: 3, Text: "Find and book the right doctor with Zakdoc. Head to zakdoc."},
		{Start: 3, End: 6, Text: "com/allthehacks to get started today."},
		{Start: 6, End: 9, Text: "Visit sonos.com and build your system. It costs 3.5 dollars."},
	}
	var got []string
	for _, s := range Sentences(cues, "S", 1) {
		got = append(got, s.Text)
	}
	want := []string{
		"Find and book the right doctor with Zakdoc.",
		"Head to zakdoc.com/allthehacks to get started today.",
		"Visit sonos.com and build your system.",
		"It costs 3.5 dollars.",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got\n  %q\nwant\n  %q", got, want)
	}
}
