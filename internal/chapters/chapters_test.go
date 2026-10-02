package chapters_test

import (
	"context"
	"encoding/binary"
	"math"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/chapters"
	"github.com/cwebley/shearcast/internal/render"
)

func TestSourceTopicsFollowFinalTimeline(t *testing.T) {
	timeline := &chapters.Timeline{Keep: []render.Range{{Start: 0, End: 600}, {Start: 720, End: 1800}}, Crossfade: .05, SourceDuration: 1800, Duration: 1679.95}
	source := []chapters.Source{{Start: 0, Title: "Opening"}, {Start: 600, Title: "Sponsor"}, {Start: 700, Title: "Surviving topic"}, {Start: 900, Title: "Science &amp; stars"}}
	original := chapters.CloneSource(source)
	description := "About this episode\n\n0:00 Opening\n10:00 Sponsor\n11:40 Surviving topic\n15:00 Science &amp; stars\n\nLinks: https://example.test/?t=900&x=2\nMeet at 15:00 tomorrow.\n15:00 Unrelated timestamp"
	want := []chapters.Chapter{{Start: 0, Title: "Opening"}, {Start: 599.95, Title: "Surviving topic"}, {Start: 779.95, Title: "Science & stars"}}
	for range 2 {
		text, got, err := chapters.Derive(description, source, timeline)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("mapped chapters: %#v, %v", got, err)
		}
		if strings.Contains(text, "Sponsor") || strings.Contains(text, "15:00 Science") || !strings.Contains(text, "00:13:00 Science & stars") || strings.Count(text, "Science & stars") != 1 {
			t.Fatal(text)
		}
		for _, preserved := range []string{"Links: https://example.test/?t=900&x=2", "Meet at 15:00 tomorrow.", "15:00 Unrelated timestamp"} {
			if !strings.Contains(text, preserved) {
				t.Fatalf("lost %q: %s", preserved, text)
			}
		}
	}
	if !reflect.DeepEqual(source, original) {
		t.Fatal("source metadata mutated")
	}
}

func TestChapterEdgesAndDescriptionRecognition(t *testing.T) {
	end := 6.0
	timeline := &chapters.Timeline{Keep: []render.Range{{Start: 2, End: 4}, {Start: 6, End: 10}}, Crossfade: .05, SourceDuration: 10, Duration: 5.95}
	tests := []struct {
		name              string
		source            []chapters.Source
		description, text string
		want              []chapters.Chapter
	}{
		{"partly removed intro", []chapters.Source{{Start: 0, Title: "Intro"}, {Start: 3, Title: "Topic"}, {Start: 4, End: &end, Title: "Ad"}, {Start: 6, Title: "End"}}, "Intro 0:00\r\n* 0:03 - Topic\r\n0:04 Ad\r\n0:06 End", "00:00:00 Intro\r\n00:00:01 Topic\r\n00:00:02 End", []chapters.Chapter{{Start: 0, Title: "Intro"}, {Start: 1, Title: "Topic"}, {Start: 1.95, Title: "End"}}},
		{"duplicate starts", []chapters.Source{{Start: 2, Title: "First"}, {Start: 2, Title: "Duplicate"}, {Start: 2.2, Title: "Same second"}}, "", "Chapters\n00:00:00 First\n00:00:00 Same second", []chapters.Chapter{{Start: 0, Title: "First"}, {Start: .2, Title: "Same second"}}},
		{"no invented headings", []chapters.Source{{Start: 0, Title: "<Untitled Chapter 1>"}, {Start: 2, Title: ""}}, "Plain description", "Plain description", nil},
		{"absent", nil, "Plain description\n0:01 unrelated", "Plain description\n0:01 unrelated", nil},
		{"all removed", []chapters.Source{{Start: 4, End: &end, Title: "Ad"}}, "Top\n0:04 Ad\nBottom", "Top\nBottom", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			text, got, err := chapters.Derive(test.description, test.source, timeline)
			if err != nil || text != test.text || len(got) != len(test.want) {
				t.Fatalf("%q, %#v: %v", text, got, err)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("chapters: %#v", got)
				}
			}
		})
	}
}

func TestUnknownAndInvalidTimelines(t *testing.T) {
	textWithoutChapters, _, err := chapters.Derive("0:00 Intro", nil, nil)
	if err != nil || !strings.Contains(textWithoutChapters, "original video") {
		t.Fatalf("unlabeled import: %s, %v", textWithoutChapters, err)
	}
	if text, _, err := chapters.Derive("Plain description", nil, &chapters.Timeline{}); err != nil || text != "Plain description" {
		t.Fatalf("chapterless metadata blocked: %q %v", text, err)
	}
	source := []chapters.Source{{Start: 0, Title: "Intro"}}
	text, list, err := chapters.Derive("0:00 Intro", source, nil)
	if err != nil || len(list) != 0 || !strings.Contains(text, "original video") || !strings.HasSuffix(text, "0:00 Intro") {
		t.Fatalf("%q %#v %v", text, list, err)
	}
	for _, timeline := range []*chapters.Timeline{
		{},
		{Keep: []render.Range{{Start: 0, End: 3}}, SourceDuration: 3, Duration: 2},
		{Keep: []render.Range{{Start: 0, End: 3}, {Start: 2, End: 4}}, SourceDuration: 4, Duration: 5},
		{Keep: []render.Range{{Start: 0, End: 3}}, SourceDuration: 3, Duration: 3, Crossfade: math.NaN()},
	} {
		if _, _, err := chapters.Derive("", source, timeline); err == nil {
			t.Fatal("invalid provenance accepted")
		}
	}
}

// A generated source has short tones exactly at two chapter starts. Decode the
// actual AAC output and check their placement, as well as its probed duration.
func TestChapterStartsMatchRenderedAudio(t *testing.T) {
	if _, err := exec.LookPath(render.Binary); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	for _, rate := range []string{"44100", "48000"} {
		t.Run(rate, func(t *testing.T) {
			dir := t.TempDir()
			source, output := filepath.Join(dir, "source.wav"), filepath.Join(dir, "render.m4a")
			expression := "aevalsrc=if(between(t\\,6\\,6.15)+between(t\\,10.6\\,10.75)\\,0.7*sin(2*PI*1000*t)\\,0):s=" + rate + ":d=12"
			cmd := exec.Command(render.Binary, "-v", "error", "-y", "-f", "lavfi", "-i", expression, source)
			if data, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("source: %v %s", err, data)
			}
			keep := []render.Range{{Start: .0004, End: 3.0004}, {Start: 5.0004, End: 8.0004}, {Start: 10.0004, End: 12}}
			if err := render.Cut(context.Background(), source, keep, output, render.CutOptions{Crossfade: .05}); err != nil {
				t.Fatal(err)
			}
			duration, err := render.Probe(context.Background(), output)
			if err != nil || math.Abs(duration.Seconds()-7.9) > .05 {
				t.Fatalf("duration %v %v", duration, err)
			}
			tl := &chapters.Timeline{Keep: keep, Crossfade: .05, SourceDuration: 12, Duration: duration.Seconds()}
			_, list, err := chapters.Derive("", []chapters.Source{{Start: 6, Title: "Tone one"}, {Start: 10.6, Title: "Tone two"}}, tl)
			if err != nil || len(list) != 2 {
				t.Fatalf("%#v %v", list, err)
			}
			pcm, err := exec.Command(render.Binary, "-v", "error", "-i", output, "-f", "f32le", "-ac", "1", "-ar", "48000", "pipe:1").Output()
			if err != nil {
				t.Fatal(err)
			}
			energy := func(start float64) float64 {
				var sum float64
				for i := int(start * 48000); i < int((start+.04)*48000); i++ {
					v := float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[i*4 : i*4+4])))
					sum += v * v
				}
				return sum
			}
			for _, c := range list {
				if energy(c.Start+.03) < 100 || energy(c.Start-.1) > 1 {
					t.Fatalf("tone not at chapter %.3f", c.Start)
				}
			}
		})
	}
}
