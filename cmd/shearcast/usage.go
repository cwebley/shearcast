package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cwebley/shearcast/internal/config"
	"github.com/cwebley/shearcast/internal/pipeline"
	"github.com/cwebley/shearcast/internal/state"
	modelusage "github.com/cwebley/shearcast/internal/usage"
	"github.com/cwebley/shearcast/internal/youtube"
)

func usageText(s *modelusage.Summary) string {
	if s == nil {
		return "unknown; no recorded usage"
	}
	if s.Attempts == 0 {
		return "0 requests; no new model work; calculated $0.000000000"
	}
	text := fmt.Sprintf("%d requests; reported tokens %d input / %d output", s.Attempts, s.InputTokens, s.OutputTokens)
	if s.MissingUsage > 0 || s.Unresolved > 0 {
		text += " [partial]"
	}
	if len(s.Calculations) == 0 {
		text += "; calculated cost unknown"
	} else {
		text += fmt.Sprintf("; calculated $%.9f", s.Cost)
		if s.UnpricedCalls > 0 || s.MissingUsage > 0 || s.Unresolved > 0 {
			text += " [partial]"
		}
		var rates []string
		for _, c := range s.Calculations {
			label := "unverified"
			if c.Rate.VerifiedAt != "" {
				label = "verified " + c.Rate.VerifiedAt
			}
			rates = append(rates, fmt.Sprintf("%s $%g/$%g per million input/output, %s", c.Rate.Model, c.Rate.InputPerMillion, c.Rate.OutputPerMillion, label))
		}
		text += " [" + strings.Join(rates, "; ") + "]"
	}
	if s.ReportedCostCalls == 0 {
		text += "; OpenRouter-reported cost unknown"
	} else {
		text += fmt.Sprintf("; OpenRouter-reported $%.9f", s.ProviderCost)
		if s.MissingCost > 0 || s.Unresolved > 0 {
			text += " [partial]"
		}
	}
	if s.MissingUsage > 0 || s.MissingCost > 0 || s.Unresolved > 0 {
		text += fmt.Sprintf("; missing token usage %d, missing cost %d, unresolved requests %d", s.MissingUsage, s.MissingCost, s.Unresolved)
	}
	return text
}

func printUsage(out io.Writer, label string, s *modelusage.Summary) {
	fmt.Fprintf(out, "%s: %s\n", label, usageText(s))
}

func printEpisodeUsage(out io.Writer, ch config.Channel, id string, ep state.Episode, cache youtube.Cache) {
	if h := ep.Usage; h != nil {
		label := "  latest processing attempt"
		if h.Latest.FinishedAt.IsZero() {
			label += " [completion not recorded]"
		}
		printUsage(out, label, &h.Latest.Usage)
		printUsage(out, "  recorded total since "+h.Since.UTC().Format(time.RFC3339), &h.Total)
		if h.InterruptedAttempts > 0 {
			fmt.Fprintf(out, "  %d earlier processing attempt(s) have no recorded completion; totals may be incomplete\n", h.InterruptedAttempts)
		}
		return
	}
	// Legacy artifact usage is a historical observation, not an episode lifetime
	// total. Never import it into new run spending or rewrite its calculation.
	path := ep.RecordPath
	if path == "" {
		audio := ep.RenderPath
		if audio == "" {
			audio = pipeline.RenderPath(cache, ch, id)
		}
		path = audio + ".json"
	}
	data, err := os.ReadFile(path)
	var record pipeline.RenderRecord
	if err == nil && json.Unmarshal(data, &record) == nil && record.Channel == ch.Slug && record.VideoID == id {
		if record.Usage != nil {
			printUsage(out, "  saved render usage; cumulative history unknown", record.Usage)
			return
		}
		if record.Detection != nil {
			s := record.Detection.Stats
			fmt.Fprintf(out, "  legacy render: reported input tokens %d; output tokens unknown; saved calculated $%.9f [legacy/unverified prototype rate]; OpenRouter-reported cost unknown; cumulative history unknown\n", s.InputTokens, s.Cost)
			return
		}
	}
	printUsage(out, "  historical model usage", nil)
}
