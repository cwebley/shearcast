package detect

import (
	"context"

	"github.com/cwebley/shearcast/internal/transcript"
)

// StartFeatures rebuilds a stored region's start-edge candidates under the
// current options and asks every feature of them, exactly as bound would. It
// exists for refitting the start weights: the render record keeps only the
// combined score per sentence, not the answers that went into it.
//
// The anchor text is rebuilt from the captions over the anchor's span rather
// than from the scan windows, which differ only in where they were split.
func (d *Detector) StartFeatures(ctx context.Context, cues []transcript.Cue, r Region) ([]transcript.Sentence, map[string]map[string]float64, error) {
	c := cluster{rule: r.Rule, start: r.AnchorStart, end: r.AnchorEnd, prob: r.AnchorProb,
		text: joinCues(transcript.Slice(cues, r.AnchorStart, r.AnchorEnd))}
	inside := max(min(c.end-c.start, d.Opts.AnchorInteriorSpan), d.Opts.ScanWindow)
	cands := d.candidates(cues, max(0, c.start-d.Opts.LeadInSpan), c.start+inside, "L")
	if len(cands) < 2 {
		return cands, nil, nil
	}
	values, err := d.ExtractFeatures(ctx, cands, d.startState(cues, c, cands), Features(d.Opts.Subject))
	return cands, values, err
}

// EndFeatures is StartFeatures for the closing edge: the same candidates and
// state bound builds for the end, asked the end feature set.
func (d *Detector) EndFeatures(ctx context.Context, cues []transcript.Cue, r Region) ([]transcript.Sentence, map[string]map[string]float64, error) {
	c := cluster{rule: r.Rule, start: r.AnchorStart, end: r.AnchorEnd, prob: r.AnchorProb,
		text: joinCues(transcript.Slice(cues, r.AnchorStart, r.AnchorEnd))}
	cands := d.candidates(cues, max(0, c.end-d.Opts.ScanWindow), c.end+d.Opts.TailSpan, "R")
	if len(cands) < 2 {
		return cands, nil, nil
	}
	values, err := d.ExtractFeatures(ctx, cands, d.endState(c, cands), EndFeatures(d.Opts.Subject))
	return cands, values, err
}
