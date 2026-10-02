package detect

import (
	"context"
	"fmt"
	"strings"

	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/transcript"
)

// Localizing weak anchors.
//
// A short read (a five-second sponsor credit, a subscribe ask) fills a small
// part of a 30 to 45s scan window, and the window scores somewhere around 0.3
// to 0.7: elevated, but under AnchorThreshold. Anchoring on such a window
// doesn't work, because the edge stage assumes its anchor sits inside the read
// and ends up cutting around the whole window (a 6s credit became a 60s cut).
// So a weak window is only a lead: each of its sentences gets the scan's own
// question, and a run of sentences confidently not content becomes the anchor.
// A weak window with no such run is dropped.

// Localized records what one weak lead turned into, for diagnosis.
type Localized struct {
	Start, End float64 // the weak windows' span
	Sentences  []LocalizedSentence
	Anchors    int // runs that became anchors
}

// LocalizedSentence is one sentence of a weak lead and its probability of
// belonging to any cut rule.
type LocalizedSentence struct {
	ID         string
	Start, End float64
	Text       string
	Rule       string // the most likely cut rule
	P          float64
	probs      map[string]float64
}

// weakLeads returns runs of windows scoring at least weak but under strong,
// leaving out any window next to a strong cluster: the edge stage already
// looks there.
func weakLeads(scored []ScoredWindow, weak, strong float64) []cluster {
	var out []cluster
	open := false
	for i, w := range scored {
		isWeak := w.Rule != KeepRule && w.Prob >= weak && w.Prob < strong
		nearStrong := (i > 0 && scored[i-1].Prob >= strong) || (i+1 < len(scored) && scored[i+1].Prob >= strong)
		if !isWeak || nearStrong {
			open = false
			continue
		}
		if open {
			c := &out[len(out)-1]
			c.end = w.End
			c.prob = max(c.prob, w.Prob)
			continue
		}
		out = append(out, cluster{rule: w.Rule, start: w.Start, end: w.End, prob: w.Prob})
		open = true
	}
	return out
}

// maxLocalizeSentences bounds one localization request. The longest lead in
// the labeled renders had 29 sentences, so it changes nothing measured; it
// keeps a long run of weak windows from becoming one oversized request.
const maxLocalizeSentences = 40

// classify asks the scan's question of every sentence in each span, with the
// transcript on either side as context, and returns each span's sentences with
// their probabilities.
func (d *Detector) classify(ctx context.Context, cues []transcript.Cue, spans [][2]float64) ([][]LocalizedSentence, error) {
	criteria := d.criteria()
	sentences := make([][]transcript.Sentence, len(spans))
	var batches []jev.Batch
	for k, span := range spans {
		ss := transcript.Sentences(transcript.Slice(cues, span[0], span[1]), "S", d.Opts.MinSentence)
		sentences[k] = ss
		// Adjacent weak windows merge without limit, so a long lead is asked
		// in parts, each with its own context. Its sentences are rejoined in
		// order below, so a run can still span parts.
		for len(ss) > 0 {
			part := ss[:min(len(ss), maxLocalizeSentences)]
			ss = ss[len(part):]
			from, to := part[0].Start, part[len(part)-1].End
			var state strings.Builder
			if before := joinCues(transcript.Slice(cues, max(0, from-d.Opts.ScanWindow), from)); before != "" {
				state.WriteString("TRANSCRIPT JUST BEFORE:\n" + before + "\n\n")
			}
			state.WriteString("SENTENCES OF THIS VIDEO TRANSCRIPT, IN ORDER:\n")
			state.WriteString(transcript.SentenceText(part))
			if after := joinCues(transcript.Slice(cues, to, to+d.Opts.ScanWindow)); after != "" {
				state.WriteString("\nTRANSCRIPT JUST AFTER:\n" + after + "\n")
			}
			questions := make(map[string]jev.Question, len(part))
			for _, s := range part {
				questions[leadID(k, s.ID)] = jev.Choice(
					fmt.Sprintf("Sentence [%s] of this video transcript is best described as", s.ID), criteria)
			}
			batches = append(batches, jev.Batch{State: state.String(), Questions: questions})
		}
	}
	if len(batches) == 0 {
		return make([][]LocalizedSentence, len(spans)), nil
	}
	answers, err := d.Client.AskAll(ctx, batches, d.Opts.Parallel)
	if err != nil {
		return nil, err
	}
	out := make([][]LocalizedSentence, len(spans))
	for k := range spans {
		for _, s := range sentences[k] {
			a, _, err := choiceAnswer(answers, leadID(k, s.ID), criteria)
			if err != nil {
				return nil, fmt.Errorf("localize: %w", err)
			}
			ls := LocalizedSentence{ID: s.ID, Start: s.Start, End: s.End, Text: s.Text, probs: map[string]float64{}}
			var best float64
			for _, r := range d.Opts.Rules {
				p := a.P(r.ID)
				ls.probs[r.ID] = p
				ls.P += p
				if p > best {
					ls.Rule, best = r.ID, p
				}
			}
			out[k] = append(out[k], ls)
		}
	}
	return out, nil
}

// localize classifies each weak lead's sentences and returns an anchor for
// each run that scores at least LocalizedThreshold, with a record of every
// lead.
func (d *Detector) localize(ctx context.Context, cues []transcript.Cue, leads []cluster) ([]cluster, []Localized, error) {
	if len(leads) == 0 {
		return nil, nil, nil
	}
	spans := make([][2]float64, len(leads))
	for k, lead := range leads {
		spans[k] = [2]float64{lead.start, lead.end}
	}
	classified, err := d.classify(ctx, cues, spans)
	if err != nil {
		return nil, nil, err
	}

	var anchors []cluster
	records := make([]Localized, len(leads))
	for k, lead := range leads {
		rec := Localized{Start: lead.start, End: lead.end, Sentences: classified[k]}
		var run *cluster
		votes := map[string]float64{}
		closeRun := func() {
			if run == nil {
				return
			}
			for rule, p := range votes {
				if p > votes[run.rule] || (p == votes[run.rule] && rule < run.rule) {
					run.rule = rule
				}
			}
			anchors = append(anchors, *run)
			rec.Anchors++
			run, votes = nil, map[string]float64{}
		}
		for _, s := range classified[k] {
			if s.P < d.Opts.LocalizedThreshold {
				closeRun()
				continue
			}
			if run == nil {
				run = &cluster{rule: s.Rule, start: s.Start, end: s.End, prob: s.P, text: s.Text, localized: true}
			} else {
				run.end = s.End
				run.prob = max(run.prob, s.P)
				run.text += " " + s.Text
			}
			for rule, p := range s.probs {
				votes[rule] += p
			}
		}
		closeRun()
		records[k] = rec
	}
	return anchors, records, nil
}

func leadID(k int, sentence string) string { return fmt.Sprintf("C%d-%s", k, sentence) }

// criteria is the scan's choice: keep, or one of the cut rules.
func (d *Detector) criteria() map[string]string {
	c := map[string]string{KeepRule: keepDescription}
	for _, r := range d.Opts.Rules {
		c[r.ID] = r.Prompt
	}
	return c
}
