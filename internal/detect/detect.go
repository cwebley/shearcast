// Package detect finds ad regions in a transcript using a System One model.
//
// The problem it solves is the soft segue: a read that opens with real subject
// matter and only reveals itself later. Classifying windows in isolation cannot
// catch that, because the lead-in genuinely is content-shaped.
//
// Detection runs in two stages:
//
//  1. scan   30s windows over the whole transcript, one Choice each. Finds
//     anchors: the unmissable stretch naming a brand or a URL.
//  2. bound  a monotone predicate over the *sentences* around each anchor edge,
//     with the anchor text in the state: "has the advertisement begun by
//     here?". The true answer is false then true, so the answers form a
//     step and the boundary is its edge.
//
// Two earlier framings for stage 2 failed on real videos. Scoring each chunk
// for membership gave a smooth ramp with no step at the true edge, so every
// threshold read off it landed either seconds late or half a minute early.
// Asking the model to select a sentence gave a confident answer that disagreed
// with human marks, and asking the mirror question returned the same boundary,
// so it was not confusion: no amount of extra instruction moved it.
//
// The predicate works because it changes the shape of the question rather than
// its wording. Each question is sharper than a selection, all of them ride in
// one parallel request, and a step fitted across dozens of answers cannot be
// moved by one noisy answer. See edge.go for the estimator.
package detect

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/cwebley/shearcast/internal/jev"
	"github.com/cwebley/shearcast/internal/segment"
	"github.com/cwebley/shearcast/internal/transcript"
)

const (
	// KeepRule is the option every scan window is scored against alongside the
	// cut rules.
	KeepRule = "content"

	// NoReturn is the one escape hatch left: a read that runs to the end of the
	// video has no sentence that returns to the subject. The start choice needs
	// no equivalent, because its candidates straddle the anchor and therefore
	// always contain the right answer.
	NoReturn = "never_returns"

	keepDescription = "The video's own subject matter, narration, or discussion"

	// segueBrief describes the shape of an ad run-in without describing any
	// particular one. The failure it exists to fix is the model confidently
	// naming the sentence where selling starts, when the advertisement really
	// started earlier, with material written to make the selling feel earned.
	//
	// This is the limit on how generic Rule really is right now: the scan
	// stage (scan()) is fully rule-agnostic -- any Rule.Prompt becomes another
	// Choice option -- but startPredicate/endPredicate below always frame the
	// boundary question in terms of "the advertisement" and "a segue",
	// whichever rule actually matched. A rule describing something else
	// entirely (a chatter segment, an off-topic tangent) would scan correctly
	// but get boundary-refined by a predicate talking about an "advertisement"
	// it never claimed to be. Making that genuinely rule-agnostic means
	// parameterizing this text (and validating the predicate still forms a
	// clean step on non-ad content), not just adding a config entry.
	segueBrief = "Advertisements inside a video or podcast are usually introduced by a segue: " +
		"a passage, often one to several sentences, that is not part of the programme's own " +
		"subject but was written to make the advertisement feel like a natural continuation of " +
		"it. A segue is commonly an anecdote, a personal aside, a rhetorical question, a " +
		"hypothetical or an observation. It is usually factually true and delivered in the same " +
		"voice and tone as the rest of the programme, which is what makes it easy to mistake for " +
		"real content. It ends by pivoting into the product. The segue is part of the " +
		"advertisement: it exists to serve the advertiser, not the audience, and a listener " +
		"skipping the advertisement would want it skipped too."

	// choiceOptionLimit is the API's cap on options in a single Choice.
	choiceOptionLimit = 255
)

// Rule is one user-defined thing to cut, described in plain English.
type Rule struct {
	ID        string
	Prompt    string
	Threshold float64
}

// Options tunes the two stages. Zero values are filled in by Defaults.
type Options struct {
	Rules []Rule

	ScanWindow      float64 // target seconds per scan window
	ScanMaxWindow   float64 // hard cap when no sentence ends
	ScanBatchSize   int     // questions per request
	AnchorThreshold float64 // a window this confident becomes an anchor

	LeadInSpan    float64 // how far before the anchor to offer candidates
	TailSpan      float64 // how far after the anchor to offer candidates
	MinSentence   int     // chars; shorter sentences fold into the next
	MaxCandidates int     // cap on candidate sentences per edge
	ContextSpan   float64 // transcript sampled to establish the video's subject

	// Subject is what the video is actually about, normally its title. Without
	// it, "has this sentence left the video's own subject" is unanswerable, and
	// a bespoke anecdote written to set up a sponsor reads as ordinary content.
	// It matters most when the read sits early enough that there is no earlier
	// transcript to sample.
	Subject string
	Verify  bool // run the content-loss check on each region

	// StartWeights, when set, replaces the single predicate on the opening edge
	// with a fitted combination of narrow per-sentence features.
	//
	// Measured cross-channel, fitting on one channel and scoring another it had
	// never seen: features 2.7s against the predicate's 17.2s on the start edge,
	// but 38.4s against 1.0s on the closing edge. So this applies to the start
	// only, and the end keeps the predicate. Each edge gets whatever works on it.
	StartWeights *Weights

	// Repeats is how many times each edge predicate is asked before the curve
	// is averaged. The answers are noisiest right at the boundary, which is the
	// only place they matter, and repeats cost only the tokens to restate the
	// questions.
	Repeats int

	// EndFitWindow caps how many after-candidates the closing edge's changepoint
	// fit considers, though every candidate in TailSpan is still asked about and
	// still shown in the curve. Real narration after the read does not reliably
	// settle near 1.0 on the "has the programme returned to its own subject"
	// predicate. Capping the fit to the first few candidates removes that
	// tail's vote without changing what gets asked or shown. Zero means no cap.
	//
	// This is end-edge only. The start edge needs the opposite: LeadInSpan
	// exists precisely because a segue can begin 100s+ before the anchor, so
	// capping its fit window would throw away the far end of the very thing
	// pass 2 exists to find.
	EndFitWindow int

	// ReverseCandidates relabels candidate ids so that sorted order runs
	// backwards in time. Options are serialised as a sorted map, so this is the
	// only way to change the order the model sees them in. It exists to measure
	// positional bias: if the sentence picked changes when only the labels move,
	// the answer is being driven by position rather than by content.
	ReverseCandidates bool

	// ClusterBridgeGap fuses same-rule scan clusters separated by less than
	// this many seconds into one, before boundary-finding runs. See
	// bridgeClusters. Default is 3x ScanWindow: enough to survive a couple of
	// consecutive low-confidence windows in a row (one hiccup is 1x), while
	// staying far short of the gap to a genuinely separate later read.
	ClusterBridgeGap float64

	// SegmentMergeGap is the final, small-gap net: see the comment on its use
	// in Run. Unlike ClusterBridgeGap this runs after boundary-fitting, on
	// finished cuts, so it only needs to catch near-touching results, not
	// structural scan splits.
	SegmentMergeGap float64

	MinRegion float64
	Parallel  int
}

func Defaults(rules []Rule) Options {
	return Options{
		Rules:            rules,
		ScanWindow:       30,
		ScanMaxWindow:    45,
		ScanBatchSize:    24,
		AnchorThreshold:  0.85,
		LeadInSpan:       180,
		TailSpan:         180,
		MinSentence:      25,
		MaxCandidates:    60,
		ContextSpan:      120,
		Verify:           true,
		Repeats:          3,
		EndFitWindow:     11,
		ClusterBridgeGap: 90,
		SegmentMergeGap:  2,
		MinRegion:        8,
		Parallel:         4,
	}
}

func (o *Options) fill() {
	d := Defaults(o.Rules)
	setF(&o.ScanWindow, d.ScanWindow)
	setF(&o.ScanMaxWindow, d.ScanMaxWindow)
	setF(&o.AnchorThreshold, d.AnchorThreshold)
	setF(&o.LeadInSpan, d.LeadInSpan)
	setF(&o.TailSpan, d.TailSpan)
	setF(&o.ContextSpan, d.ContextSpan)
	setF(&o.ClusterBridgeGap, d.ClusterBridgeGap)
	setF(&o.SegmentMergeGap, d.SegmentMergeGap)
	setF(&o.MinRegion, d.MinRegion)
	setI(&o.ScanBatchSize, d.ScanBatchSize)
	setI(&o.MinSentence, d.MinSentence)
	setI(&o.MaxCandidates, d.MaxCandidates)
	setI(&o.Parallel, d.Parallel)
	setI(&o.Repeats, d.Repeats)
	setI(&o.EndFitWindow, d.EndFitWindow)
	if o.MaxCandidates > choiceOptionLimit-1 {
		o.MaxCandidates = choiceOptionLimit - 1
	}
}

// Region is one detected ad, with enough detail to show what each stage did.
type Region struct {
	Rule string

	AnchorStart float64
	AnchorEnd   float64
	AnchorProb  float64

	Start float64
	End   float64

	// LeadIn and TailOut are how far the boundary choice moved each edge off the
	// anchor. Positive means outward (an earlier start, a later end); negative
	// means the choice pulled the anchor's own edge back in.
	LeadIn  float64
	TailOut float64

	// StartPick is the sentence at the fitted edge and StartStep is how tall
	// the step is there, which serves as confidence: a tall step is an
	// unambiguous boundary, a shallow one a gradual segue.
	StartPick string
	StartStep float64
	EndPick   string
	EndStep   float64

	// ContentLoss is the verification noul: does the cut swallow any of the
	// video's real subject matter. Only set when Options.Verify is on.
	ContentLoss float64

	// StartCurve is the averaged predicate across the start candidates, kept so
	// a bad boundary can be diagnosed without paying for the run twice.
	StartCurve []CurvePoint

	// StartState is the exact state sent to the model for the start edge. Kept
	// so another model can be handed the identical task, which is the only way
	// a head-to-head means anything.
	StartState string

	// EndCurve and EndState are the same for the closing edge. A cut has two
	// edges and only the opening one has had the feature treatment so far.
	EndCurve []CurvePoint
	EndState string
}

// CurvePoint is one candidate sentence and what the predicate said about it.
type CurvePoint struct {
	ID    string
	Start float64
	End   float64
	P     float64
	Text  string
	Edge  bool // the index the fitted step chose
}

// Sentence rebuilds the candidate this point came from, so a later pass can ask
// different questions of exactly the same sentences.
func (c CurvePoint) Sentence() transcript.Sentence {
	return transcript.Sentence{ID: c.ID, Start: c.Start, End: c.End, Text: c.Text}
}

func (r Region) Segment() segment.Segment {
	return segment.Segment{
		Start:      r.Start,
		End:        r.End,
		Rule:       r.Rule,
		Source:     "jev",
		Confidence: r.AnchorProb,
	}
}

// Result is everything a run produced.
type Result struct {
	Regions  []Region
	Segments []segment.Segment
	Windows  []ScoredWindow
	Stats    jev.Stats
}

// ScoredWindow is one scan window and what the model made of it.
type ScoredWindow struct {
	transcript.Window
	Rule string  // best-scoring cut rule, or KeepRule
	Prob float64 // that rule's probability
}

// Detector runs the stages against a client.
type Detector struct {
	Client *jev.Client
	Opts   Options
}

func New(client *jev.Client, opts Options) *Detector {
	opts.fill()
	return &Detector{Client: client, Opts: opts}
}

// Run detects ad regions across a full caption track.
func (d *Detector) Run(ctx context.Context, cues []transcript.Cue, duration float64) (*Result, error) {
	if len(cues) == 0 {
		return &Result{Stats: d.Client.Stats()}, nil
	}

	scored, err := d.scan(ctx, cues)
	if err != nil {
		return nil, fmt.Errorf("scan stage: %w", err)
	}

	result := &Result{Windows: scored}
	bridged := bridgeClusters(clusters(scored, d.Opts.AnchorThreshold), d.Opts.ClusterBridgeGap)
	for _, c := range bridged {
		region, err := d.bound(ctx, cues, c)
		if err != nil {
			return nil, fmt.Errorf("boundary stage: %w", err)
		}
		if region.End-region.Start < d.Opts.MinRegion {
			continue
		}
		result.Regions = append(result.Regions, region)
	}

	for _, r := range result.Regions {
		result.Segments = append(result.Segments, r.Segment())
	}
	result.Segments = segment.Clamp(result.Segments, duration)
	// A second, much smaller-gap net: ClusterBridgeGap already keeps one real
	// ad from being bounded as separate fragments, but two independently
	// bounded regions (different rules, or the same rule with a genuine but
	// tiny gap) can still end up touching or a fraction of a second apart
	// after boundary-fitting. Merge is exactly "a two second island of
	// narration is more jarring kept than cut" -- see segment.go.
	result.Segments = segment.Merge(result.Segments, d.Opts.SegmentMergeGap)
	result.Stats = d.Client.Stats()
	return result, nil
}

// scan is stage 1: one Choice per window over the whole transcript.
func (d *Detector) scan(ctx context.Context, cues []transcript.Cue) ([]ScoredWindow, error) {
	windows := transcript.Windows(cues, "W", d.Opts.ScanWindow, d.Opts.ScanMaxWindow)
	if len(windows) == 0 {
		return nil, nil
	}

	criteria := map[string]string{KeepRule: keepDescription}
	for _, r := range d.Opts.Rules {
		criteria[r.ID] = r.Prompt
	}

	// Each batch carries its own slice of transcript plus one window of context
	// on either side, so a window at a batch boundary still has somewhere to
	// look. The context windows are shown but not asked about.
	var batches []jev.Batch
	size := d.Opts.ScanBatchSize
	for start := 0; start < len(windows); start += size {
		end := min(start+size, len(windows))
		ctxStart := max(0, start-1)
		ctxEnd := min(len(windows), end+1)

		questions := make(map[string]jev.Question, end-start)
		for _, w := range windows[start:end] {
			questions[w.ID] = jev.Choice(
				fmt.Sprintf("Window [%s] of this video transcript is best described as", w.ID),
				criteria,
			)
		}
		batches = append(batches, jev.Batch{
			State:     "VIDEO TRANSCRIPT, IN WINDOWS:\n" + transcript.Text(windows[ctxStart:ctxEnd]),
			Questions: questions,
		})
	}

	answers, err := d.Client.AskAll(ctx, batches, d.Opts.Parallel)
	if err != nil {
		return nil, err
	}

	scored := make([]ScoredWindow, 0, len(windows))
	for _, w := range windows {
		sw := ScoredWindow{Window: w, Rule: KeepRule}
		if a, ok := answers[w.ID]; ok {
			for _, r := range d.Opts.Rules {
				if p := a.P(r.ID); p > sw.Prob {
					sw.Rule, sw.Prob = r.ID, p
				}
			}
		}
		scored = append(scored, sw)
	}
	return scored, nil
}

// cluster is a contiguous run of scan windows confident about the same rule.
type cluster struct {
	rule  string
	start float64
	end   float64
	prob  float64
	text  string
}

// bridgeClusters fuses same-rule clusters separated by a small gap into one.
//
// Scan classifies each 30s window in isolation. A single quieter aside, a
// music sting, or one sentence that happens to read as content-shaped is
// enough to drop one window below AnchorThreshold and split what is really
// one continuous read into two or more clusters. Left alone, each fragment
// would be handed to bound() separately: a middle fragment's "the
// advertisement quoted here" state would only ever contain that fragment's
// own text, not the read as a whole, so its start/end questions would be
// answered against an incomplete quote -- not just "two cuts instead of
// one", but boundary-finding working from the wrong premise. Bridging first
// means bound() only ever sees one cluster per real ad, however many scan
// fragments it arrived in, with the full concatenated text.
//
// This is a standard chained interval merge, so it handles any number of
// fragments the same way: two, three, or more.
func bridgeClusters(cs []cluster, maxGap float64) []cluster {
	if len(cs) == 0 {
		return cs
	}
	out := []cluster{cs[0]}
	for _, c := range cs[1:] {
		last := &out[len(out)-1]
		if c.rule == last.rule && c.start-last.end <= maxGap {
			last.end = c.end
			last.text += " " + c.text
			if c.prob > last.prob {
				last.prob = c.prob
			}
			continue
		}
		out = append(out, c)
	}
	return out
}

// clusters groups adjacent high-confidence windows. Each becomes one anchor.
func clusters(scored []ScoredWindow, threshold float64) []cluster {
	var out []cluster
	var current *cluster
	for _, w := range scored {
		if w.Rule == KeepRule || w.Prob < threshold {
			current = nil
			continue
		}
		if current != nil && current.rule == w.Rule {
			current.end = w.End
			current.text += " " + w.Text
			if w.Prob > current.prob {
				current.prob = w.Prob
			}
			continue
		}
		out = append(out, cluster{rule: w.Rule, start: w.Start, end: w.End, prob: w.Prob, text: w.Text})
		current = &out[len(out)-1]
	}
	return out
}

// bound is stage 2: find each edge with a monotone predicate over sentences.
func (d *Detector) bound(ctx context.Context, cues []transcript.Cue, c cluster) (Region, error) {
	region := Region{
		Rule:        c.rule,
		AnchorStart: c.start,
		AnchorEnd:   c.end,
		AnchorProb:  c.prob,
		Start:       c.start,
		End:         c.end,
	}
	rule := d.rule(c.rule)

	// Candidates straddle each anchor edge. The scan pass anchors at the start
	// of a fixed-width window, so the real boundary can sit on either side of
	// it: offering only earlier sentences means a late anchor can never be
	// pulled back far enough, and an early one can never be corrected at all.
	before := d.candidates(cues, max(0, c.start-d.Opts.LeadInSpan), c.start+d.Opts.ScanWindow, "L")
	after := d.candidates(cues, max(0, c.end-d.Opts.ScanWindow), c.end+d.Opts.TailSpan, "R")

	if len(before) > 1 {
		state := d.startState(cues, c, before)
		region.StartState = state

		var (
			idx   int
			step  float64
			curve []float64
			err   error
		)
		if d.Opts.StartWeights != nil {
			idx, step, curve, err = d.findEdgeByFeatures(ctx, cues, c, before)
		} else {
			idx, step, curve, err = d.findEdge(ctx, state, before, d.startPredicate(rule), 0)
		}
		if err != nil {
			return region, err
		}
		for i, s := range before {
			region.StartCurve = append(region.StartCurve, CurvePoint{
				ID: s.ID, Start: s.Start, End: s.End, P: curve[i], Text: s.Text, Edge: i == idx,
			})
		}
		if idx > 0 {
			region.Start = before[idx].Start
			region.LeadIn = c.start - region.Start
			region.StartPick, region.StartStep = before[idx].ID, step
		}
	}

	if len(after) > 1 {
		endState := d.endState(c, after)
		region.EndState = endState
		idx, step, curve, err := d.findEdge(ctx, endState, after, d.endPredicate(rule), d.Opts.EndFitWindow)
		if err != nil {
			return region, err
		}
		for i, s := range after {
			region.EndCurve = append(region.EndCurve, CurvePoint{
				ID: s.ID, Start: s.Start, End: s.End, P: curve[i], Text: s.Text, Edge: i == idx,
			})
		}
		if idx > 0 {
			// The edge is the first sentence back on the programme's subject,
			// so the read ends where that sentence begins.
			region.End = after[idx].Start
			region.TailOut = region.End - c.end
			region.EndPick, region.EndStep = after[idx].ID, step
		}
	}

	if d.Opts.Verify {
		loss, err := d.verify(ctx, cues, region, rule)
		if err != nil {
			return region, err
		}
		region.ContentLoss = loss
	}
	return region, nil
}

// findEdge asks one predicate of every candidate at once, repeats it, and fits
// a step to the averaged curve. It returns the index of the edge and the step's
// height, which is a usable confidence: a tall step is an unambiguous boundary.
//
// An index of zero means the predicate never rose, so the boundary lies outside
// the candidates offered and the caller should keep the anchor's own edge.
//
// fitWindow, if positive, restricts the changepoint fit to the first fitWindow
// candidates; every candidate is still asked about and still returned in the
// full curve, only the fit itself is narrowed. See Options.EndFitWindow for
// why this matters on the closing edge and must not be used on the opening
// one.
func (d *Detector) findEdge(ctx context.Context, state string, cands []transcript.Sentence, instructions func(transcript.Sentence) string, fitWindow int) (int, float64, []float64, error) {
	questions := make(map[string]jev.Question, len(cands))
	for _, s := range cands {
		questions[s.ID] = jev.Noul(instructions(s))
	}

	// Repeat and average. The predicate is noisiest right at the boundary,
	// which is the only place it matters, and extra questions cost almost
	// nothing beyond the tokens to state them.
	batches := make([]jev.Batch, 0, d.Opts.Repeats)
	for i := 0; i < d.Opts.Repeats; i++ {
		batches = append(batches, jev.Batch{State: state, Questions: questions})
	}

	runs := make([][]float64, 0, d.Opts.Repeats)
	for _, b := range batches {
		resp, err := d.Client.Ask(ctx, b.State, b.Questions)
		if err != nil {
			return 0, 0, nil, err
		}
		curve := make([]float64, len(cands))
		for i, s := range cands {
			curve[i] = resp.Answers[s.ID].Noul
		}
		runs = append(runs, curve)
	}

	curve := average(runs)

	// The full curve is always returned -- every candidate was asked about and
	// -trace wants to show all of it -- but the fit itself, when capped, only
	// sees the front of it. A candidate list can run for minutes past the true
	// edge, and on real narration that tail does not reliably settle near 1.0,
	// so leaving it in the fit lets it out-vote the true edge on noise alone.
	fitCurve := curve
	if fitWindow > 0 && fitWindow < len(curve) {
		fitCurve = curve[:fitWindow]
	}

	idx := changepoint(fitCurve)
	if !stepRises(fitCurve, idx) {
		return 0, 0, curve, nil
	}
	var lo, hi float64
	for _, y := range fitCurve[:idx] {
		lo += y
	}
	for _, y := range fitCurve[idx:] {
		hi += y
	}
	return idx, hi/float64(len(fitCurve)-idx) - lo/float64(idx), curve, nil
}

// findEdgeByFeatures scores every candidate with the fitted weights and fits a
// step to the resulting curve. The model never judges where the boundary is; it
// only answers narrow factual questions, and the combination happens here where
// it can be inspected and refitted for nothing.
func (d *Detector) findEdgeByFeatures(ctx context.Context, cues []transcript.Cue, c cluster, cands []transcript.Sentence) (int, float64, []float64, error) {
	// The state must match the one the weights were fitted on. Feature
	// extraction happens under the start predicate's state, so the live path
	// asks under the same one; answers given in a different context are
	// different features, and the weights do not transfer.
	feats := Features(d.Opts.Subject)
	values, err := d.ExtractFeatures(ctx, cands, d.startState(cues, c, cands), feats)
	if err != nil {
		return 0, 0, nil, err
	}

	curve := make([]float64, len(cands))
	for i, s := range cands {
		curve[i] = d.Opts.StartWeights.Score(values[s.ID])
	}

	idx := changepoint(curve)
	if !stepRises(curve, idx) {
		return 0, 0, curve, nil
	}
	var lo, hi float64
	for _, y := range curve[:idx] {
		lo += y
	}
	for _, y := range curve[idx:] {
		hi += y
	}
	return idx, hi/float64(len(curve)-idx) - lo/float64(idx), curve, nil
}

// startPredicate is monotone: false before the advertisement, true after. The
// segue counts as part of it, which is the whole point of asking this way.
func (d *Detector) startPredicate(rule Rule) func(transcript.Sentence) string {
	return func(s transcript.Sentence) string {
		return fmt.Sprintf(
			"%s\n\nThe advertisement quoted at the end of the state is: %s.\n\nDoes that "+
				"advertisement, counting any segue written to introduce it, begin at or before "+
				"sentence [%s]?",
			segueBrief, rule.Prompt, s.ID)
	}
}

// endPredicate is the same shape at the other edge.
func (d *Detector) endPredicate(rule Rule) func(transcript.Sentence) string {
	return func(s transcript.Sentence) string {
		return fmt.Sprintf(
			"%s\n\nThe advertisement quoted at the start of the state is: %s.\n\nHas the "+
				"programme returned to its own subject matter by sentence [%s], leaving that "+
				"advertisement and any wind-down from it behind?",
			segueBrief, rule.Prompt, s.ID)
	}
}

// candidates builds the sentence options for one edge, keeping the ones nearest
// the anchor when there are more than the cap allows.
func (d *Detector) candidates(cues []transcript.Cue, from, to float64, prefix string) []transcript.Sentence {
	ss := transcript.Sentences(transcript.Slice(cues, from, to), prefix, d.Opts.MinSentence)
	if len(ss) > d.Opts.MaxCandidates {
		if prefix == "L" {
			ss = ss[len(ss)-d.Opts.MaxCandidates:] // nearest the anchor is the tail
		} else {
			ss = ss[:d.Opts.MaxCandidates]
		}
	}
	if d.Opts.ReverseCandidates {
		for i := range ss {
			ss[i].ID = fmt.Sprintf("%s%03d", prefix, len(ss)-i)
		}
	}
	return ss
}

func (d *Detector) startState(cues []transcript.Cue, c cluster, before []transcript.Sentence) string {
	var b strings.Builder
	if subject := d.subject(cues, c); subject != "" {
		b.WriteString("WHAT THIS PROGRAMME IS ACTUALLY ABOUT:\n")
		b.WriteString(subject)
		b.WriteString("\n\n")
	}
	b.WriteString("CANDIDATE SENTENCES, IN ORDER. THE ADVERTISEMENT BEGINS SOMEWHERE IN THIS RANGE:\n")
	b.WriteString(transcript.SentenceText(before))
	b.WriteString("\nTHE ADVERTISEMENT THAT FOLLOWS THESE SENTENCES:\n")
	b.WriteString(excerpt(c.text, 1200))
	return b.String()
}

func (d *Detector) endState(c cluster, after []transcript.Sentence) string {
	var b strings.Builder
	b.WriteString("THE ADVERTISEMENT ALREADY UNDERWAY:\n")
	b.WriteString(excerpt(c.text, 1200))
	b.WriteString("\n\nCANDIDATE SENTENCES, IN ORDER. THE ADVERTISEMENT ENDS SOMEWHERE IN THIS RANGE:\n")
	b.WriteString(transcript.SentenceText(after))
	return b.String()
}

// verify asks directly about the thing that matters: whether the cut we settled
// on swallows any of the video's real content.
func (d *Detector) verify(ctx context.Context, cues []transcript.Cue, r Region, rule Rule) (float64, error) {
	text := joinCues(transcript.Slice(cues, r.Start, r.End))
	if text == "" {
		return 0, nil
	}
	resp, err := d.Client.Ask(ctx,
		"PASSAGE ABOUT TO BE CUT FROM THE AUDIO:\n"+excerpt(text, 4000),
		map[string]jev.Question{"loss": jev.Noul(fmt.Sprintf(
			"This passage contains some of the video's own subject matter, which a listener would "+
				"not want removed. It is not entirely %s.", rule.Prompt))},
	)
	if err != nil {
		return 0, err
	}
	return resp.Answers["loss"].Noul, nil
}

// subject tells the model what the video is actually about.
//
// Two sources, because either alone can fail. The title is authoritative and
// always present. A sample of narration taken from after the read is known to
// be on topic, which the transcript before a read is not: these channels open
// with a cold open and drop the sponsor two minutes in, so "the passage before
// the candidates" is often either empty or already part of the set-up.
func (d *Detector) subject(cues []transcript.Cue, c cluster) string {
	var parts []string
	if d.Opts.Subject != "" {
		parts = append(parts, "Title: "+d.Opts.Subject)
	}
	if sample := joinCues(transcript.Slice(cues, c.end, c.end+d.Opts.ContextSpan)); sample != "" {
		parts = append(parts, "A passage of the video's own narration:\n"+excerpt(sample, 1200))
	}
	return strings.Join(parts, "\n\n")
}

func joinCues(cues []transcript.Cue) string {
	parts := make([]string, len(cues))
	for i, c := range cues {
		parts[i] = c.Text
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func (d *Detector) rule(id string) Rule {
	for _, r := range d.Opts.Rules {
		if r.ID == id {
			return r
		}
	}
	return Rule{ID: id, Prompt: id}
}

func excerpt(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}

// SortRegions orders regions by start time.
func SortRegions(rs []Region) {
	sort.Slice(rs, func(i, j int) bool { return rs[i].Start < rs[j].Start })
}

func setF(dst *float64, def float64) {
	if *dst == 0 {
		*dst = def
	}
}

func setI(dst *int, def int) {
	if *dst == 0 {
		*dst = def
	}
}
