package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/detect"
	"github.com/cwebley/shearcast/internal/pipeline"
	"github.com/cwebley/shearcast/internal/state"
	modelusage "github.com/cwebley/shearcast/internal/usage"
	"github.com/cwebley/shearcast/internal/youtube"
)

func TestUsageReportDistinguishesUnknownZeroAndPartial(t *testing.T) {
	if text := usageText(nil); !strings.Contains(text, "unknown") || strings.Contains(text, "$0") {
		t.Fatal(text)
	}
	if text := usageText(&modelusage.Summary{}); !strings.Contains(text, "no new model work") {
		t.Fatal(text)
	}
	s := modelusage.Summary{Attempts: 2, MissingUsage: 1, MissingCost: 1}
	i, o, cost := 100, 10, 0.0
	s.Observe(&i, &o, &cost, modelusage.ModelRate(modelusage.JevModel))
	text := usageText(&s)
	for _, want := range []string{"100 input / 10 output [partial]", "calculated $0.000004200 [partial]", "OpenRouter-reported $0.000000000 [partial]", "verified 2026-09-23"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}

func TestEpisodeLegacyCalculationIsNotRepricedOrClaimedAsTotal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "render.m4a.json")
	record := pipeline.RenderRecord{Version: 1, Channel: "show", VideoID: "abcdefghijk", Model: modelusage.JevModel, Detection: &detect.Result{Stats: modelusage.Summary{InputTokens: 1000, Cost: .123}}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printEpisodeUsage(&out, config.Channel{Slug: "show"}, "abcdefghijk", state.Episode{RecordPath: path}, youtube.Cache{Dir: dir})
	for _, want := range []string{"legacy/unverified", "$0.123000000", "output tokens unknown", "cumulative history unknown"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(data, after) {
		t.Fatal("inspection changed historical record")
	}
}

func TestStatusReportsSavedPartialUsageDuringWriterWithoutMutation(t *testing.T) {
	dir, args := diagnosticFixture(t)
	path := filepath.Join(dir, "state.json")
	s, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(s.StartSync([]string{"show"}))
	check(s.StartChannelSync("show"))
	check(s.Save("show", "abcdefghijk", state.Episode{Stage: state.Pending}))
	check(s.BeginUsage("show", "abcdefghijk", "attempt", modelusage.JevModel, true))
	observed := modelusage.Summary{Attempts: 2, Unresolved: 1}
	i, o, cost := 100, 10, .001
	observed.Observe(&i, &o, &cost, modelusage.ModelRate(modelusage.JevModel))
	check(s.CheckpointUsage("show", "abcdefghijk", "attempt", observed, false))
	before, err := os.ReadFile(path)
	check(err)
	var out bytes.Buffer
	check(statusCommand(args, &out))
	for _, want := range []string{"completion not recorded", "100 input / 10 output [partial]", "OpenRouter-reported $0.001000000 [partial]", "unresolved requests 1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	after, err := os.ReadFile(path)
	check(err)
	if !bytes.Equal(before, after) {
		t.Fatal("status mutated usage")
	}
}
