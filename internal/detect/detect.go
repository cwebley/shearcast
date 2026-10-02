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
	"math"
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
		"a passage, often one to several sentences, that is not part of the program's own " +
		"subject but was written to make the advertisement feel like a natural continuation of " +
		"it. A segue is commonly an anecdote, a personal aside, a rhetorical question, a " +
		"hypothetical or an observation. It is usually factually true and delivered in the same " +
		"voice and tone as the rest of the program, which is what makes it easy to mistake for " +
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

	// StartWeights, when set, replaces the single predicate on the opening edge
	// with a fitted combination of narrow per-sentence features.
	//
	// Measured cross-channel, fitting on one channel and scoring another it had
	// never seen: features 2.7s against the predicate's 17.2s on the start edge,
	// but 38.4s against 1.0s on the closing edge. So the start uses these, and
	// the end keeps the predicate. Each edge gets whatever works on it.
	StartWeights *Weights

	// EndWeights, when set, lets a fitted per-sentence ad score push the
	// closing edge later than the predicate put it, never earlier. The
	// predicate asks whether the program has returned by a sentence, and it
	// reads a pitch's tail (a web address, thanks to partners) as half
	// returned. Replacing the predicate with the fitted score lost on the Opus
	// labels, because a per-sentence score can't tell content before a short
	// read from content after it. Extending past sentences it scores at
	// EndExtendFloor or above took ad left in from 233s to 162s over 66 held-out
	// ends and cut no content (fit/fit_weights.py -edge end, 2026-09-28). With
	// production credits labeled as cuttable, 0.6 did best: 108s left in.
	EndWeights     *Weights
	EndExtendFloor float64

	// WeakAnchorThreshold, when set below AnchorThreshold, makes windows
	// scoring between the two into leads rather than anchors: each lead's
	// sentences are classified and a run of confident ones becomes the
	// anchor (see localize.go). A value at or above AnchorThreshold turns
	// it off.
	//
	// The default is low enough to classify nearly every sentence outside a
	// strong anchor. A 3 to 10s promo fills a small part of a window, and the
	// scan question asks what the window mostly is, so on the labeled videos
	// most missed promos sat in windows scoring 0.15 to 0.46, under the old
	// 0.5. Classified alone, "This show is sponsored by Hilton" scores near
	// 1. At 0.01 (2026-09-30, 44 training videos), 89 of 96 blocks were
	// detected against 76 at 0.5, for about $0.001 more per video. On the
	// held-out videos, 39 of 42 against 32, with no false cuts.
	WeakAnchorThreshold float64
	// LocalizedThreshold is how confident a classified sentence must be to
	// join a localized anchor. Classifying every sentence turns up sign-ons
	// ("From The New York Times, this is The Daily"), teasers and shout-outs
	// at 0.85 to 0.94. At 0.85 that made 8 false cuts (42s) on the training
	// videos; at 0.95, 1 (4s), with one block fewer detected.
	LocalizedThreshold float64
	// LocalizedMinStep is the smallest step that may move a localized
	// anchor's start earlier. The run was already decided sentence by
	// sentence, and steps of 0.06 to 0.21 dragged three starts 6 to 26s into
	// content to catch 5 to 9s promos. The correct earlier moves stepped 0.5.
	LocalizedMinStep float64
	// OutroTrimFloor trims the front of a read that runs to the end of the
	// video: its start moves to the first sentence, from the fitted start
	// on, that the scan question scores at the floor or above. At the end of
	// a video there's no narration after the read to show what the program
	// is about, and the start weights score wrap-up talk (sign-offs, a
	// teaser, banter, a farewell to a guest) like a promo's lead-in. Outros
	// made 9 of the 11 early starts on 2026-09-28's runs. Cutting content is
	// the worse error, so this only ever moves the start later. On the
	// training videos 0.9 left no early outro start and 0.5 left two. It
	// covers localized anchors too: on one run Klatt's subscribe ask was
	// localized, and a 0.61 step moved its start 30s back to a teaser. A
	// value above 1 turns it off.
	OutroTrimFloor float64
	// LocalizedMinRegion replaces MinRegion for regions bounded from a
	// localized anchor. Those were confirmed sentence by sentence, and a
	// sponsor credit or a subscribe ask runs 3 to 8s, under MinRegion.
	LocalizedMinRegion float64

	// Repeats is how many times each edge predicate is asked before the curve
	// is averaged. The answers are noisiest right at the boundary, which is the
	// only place they matter, and repeats cost only the tokens to restate the
	// questions.
	Repeats int

	// EndFitWindow caps how many after-candidates the closing edge's changepoint
	// fit considers, though every candidate in TailSpan is still asked about and
	// still shown in the curve. Real narration after the read does not reliably
	// settle near 1.0 on the "has the program returned to its own subject"
	// predicate. Capping the fit to the first few candidates removes that
	// tail's vote without changing what gets asked or shown. Zero means no cap.
	//
	EndFitWindow int

	// AnchorInteriorSpan is how far into the anchor the opening edge's
	// candidates reach, capped at the anchor's end. A scan window flags as a
	// whole, so an anchor can begin half a window before the read does; the
	// fit needs enough sentences inside the read to outweigh an unrelated bump
	// among the lead-in candidates. At one window deep, a read starting 26s
	// into its anchor left two points on the ad side of the step, and an
	// earlier mini-plug won the fit. The closing edge keeps one window: listing
	// interior sentences in its state blurred the model's answers there and
	// moved four of 42 measured ends 4-8s early.
	AnchorInteriorSpan float64

	// StartFitSpan is how far before the anchor the opening edge's step fit
	// looks, though every candidate in LeadInSpan is still asked about and
	// still shown in the curve. The fit knows nothing about segue length, so
	// over 180s of lead-in any off-subject stretch (a news brief, a tangent)
	// can win it. Replayed 2026-09-27 over 41 start edges with SponsorBlock
	// marks, cross-checked by Opus reads: no correct edge sat more than 61s
	// before its anchor, and 75s cut content-loss errors from 511s to 159s.
	StartFitSpan float64

	// StartFallbackFloor is the score a sentence inside the anchor must reach
	// to become the opening edge when the fit finds no rise. The anchor's own
	// start is a window edge, which can sit well before the read, so the first
	// sentence that actually reads as advertisement is the better guess.
	StartFallbackFloor float64

	// EndMinStep is the smallest step the closing edge accepts as a real return
	// to the program. Below it the fit widens one candidate at a time past
	// EndFitWindow, and failing that, chooses between EndNeverReturns and the
	// conservative earliest candidate. Measured 2026-09-24 over 19 labeled
	// closing curves (fit/replay_end_edge.py): every correct edge stepped 0.46
	// or more, every wrong one 0.16 or less, so 0.3 sits in the gap.
	EndMinStep float64

	// EndNeverReturns is the ceiling under which a curve with no qualifying step
	// means the read runs past every candidate: the model is saying the
	// program never comes back, as with a sponsor read that ends the video.
	EndNeverReturns float64

	// ReverseCandidates relabels candidate ids so that sorted order runs
	// backwards in time. Options are serialized as a sorted map, so this is the
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
		Rules:           rules,
		ScanWindow:      30,
		ScanMaxWindow:   45,
		ScanBatchSize:   24,
		AnchorThreshold: 0.85,
		LeadInSpan:      180,
		TailSpan:        180,
		MinSentence:     25,
		MaxCandidates:   100,
		ContextSpan:     120,
		Repeats:         3,
		EndFitWindow:    11,
		EndMinStep:      0.3,
		EndNeverReturns: 0.3,

		AnchorInteriorSpan: 60,
		StartFitSpan:       75,
		StartFallbackFloor: 0.5,
		EndExtendFloor:     0.6,
		LocalizedMinRegion: 3,

		WeakAnchorThreshold: 0.01,
		LocalizedThreshold:  0.95,
		LocalizedMinStep:    0.3,
		OutroTrimFloor:      0.9,

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
	setF(&o.EndMinStep, d.EndMinStep)
	setF(&o.EndNeverReturns, d.EndNeverReturns)
	setF(&o.AnchorInteriorSpan, d.AnchorInteriorSpan)
	setF(&o.StartFitSpan, d.StartFitSpan)
	setF(&o.StartFallbackFloor, d.StartFallbackFloor)
	setF(&o.EndExtendFloor, d.EndExtendFloor)
	setF(&o.WeakAnchorThreshold, d.WeakAnchorThreshold)
	setF(&o.LocalizedMinRegion, d.LocalizedMinRegion)
	setF(&o.LocalizedThreshold, d.LocalizedThreshold)
	setF(&o.LocalizedMinStep, d.LocalizedMinStep)
	setF(&o.OutroTrimFloor, d.OutroTrimFloor)
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
	// Localized is set when the anchor came from a weak lead (localize.go).
	Localized bool `json:",omitempty"`

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
	// EndReason records which closing-edge rule decided End; see fitEnd.
	EndReason string

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

	// Trim is the classified sentences an outro's start was trimmed over,
	// from the fitted start to the anchor's end. See OutroTrimFloor.
	Trim []LocalizedSentence `json:",omitempty"`
}

// CurvePoint is one candidate sentence and what the predicate said about it.
type CurvePoint struct {
	ID    string
	Start float64
	End   float64
	P     float64
	// Ad is the fitted end ad score (Options.EndWeights), when it was asked.
	Ad   float64 `json:",omitempty"`
	Text string
	Edge bool // the index the fitted step chose
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
	Regions []Region
	// Dropped holds anchors whose bounded region came out shorter than
	// MinRegion, kept so a missed read can be traced to its edges.
	Dropped []Region
	// Localized is what each weak lead turned into, when
	// Options.WeakAnchorThreshold is on.
	Localized []Localized
	Segments  []segment.Segment
	Windows   []ScoredWindow
	Stats     jev.Stats
}

// ScoredWindow is one scan window and what the model made of it.
type ScoredWindow struct {
	transcript.Window
	Rule  string             // best-scoring cut rule, or KeepRule
	Prob  float64            // probability of any cut rule: everything but KeepRule
	Probs map[string]float64 // every choice's probability, KeepRule included
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
	anchors := bridgeClusters(clusters(scored, d.Opts.AnchorThreshold), d.Opts.ClusterBridgeGap)
	if w := d.Opts.WeakAnchorThreshold; w > 0 && w < d.Opts.AnchorThreshold {
		// Localized anchors are tight on purpose, so they aren't bridged:
		// fusing one with a neighbor up to ClusterBridgeGap away would put
		// the whole gap back inside an anchor.
		local, records, err := d.localize(ctx, cues, weakLeads(scored, w, d.Opts.AnchorThreshold))
		if err != nil {
			return nil, fmt.Errorf("localize stage: %w", err)
		}
		result.Localized = records
		anchors = append(anchors, local...)
		sort.SliceStable(anchors, func(i, j int) bool { return anchors[i].start < anchors[j].start })
	}
	for len(anchors) > 0 {
		c := anchors[0]
		anchors = anchors[1:]
		region, err := d.bound(ctx, cues, c)
		if err != nil {
			return nil, fmt.Errorf("boundary stage: %w", err)
		}
		minimum := d.Opts.MinRegion
		if c.localized {
			minimum = d.Opts.LocalizedMinRegion
		}
		if region.End-region.Start >= minimum {
			result.Regions = append(result.Regions, region)
			continue
		}
		result.Dropped = append(result.Dropped, region)
		if c.localized {
			continue
		}
		// A strong anchor bounded to a sliver is usually a short read at
		// the end of a video: a subscribe ask, "thanks for watching". Three
		// of those came out 7 to 8s long and were dropped under MinRegion.
		// Check its sentences one by one instead, as for a weak lead.
		local, records, err := d.localize(ctx, cues, []cluster{c})
		if err != nil {
			return nil, fmt.Errorf("localize stage: %w", err)
		}
		result.Localized = append(result.Localized, records...)
		anchors = append(local, anchors...)
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

	criteria := d.criteria()

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
		a, probs, err := choiceAnswer(answers, w.ID, criteria)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		sw.Probs = probs
		// A window is anchored on everything that isn't content, not on its
		// strongest rule alone. Rules split the vote: an outro that asks for
		// a subscription and then credits the producers can read as 0.5
		// selfpromo and 0.45 credits, and clear no threshold by itself.
		// Replayed on the labeled videos' stored scan answers, this anchored
		// 76 of 95 blocks against 69, with no new anchor on unlabeled content.
		var best float64
		for _, r := range d.Opts.Rules {
			p := a.P(r.ID)
			sw.Prob += p
			if p > best {
				sw.Rule, best = r.ID, p
			}
		}
		scored = append(scored, sw)
	}
	return scored, nil
}

// cluster is a contiguous run of scan windows confident that they should be
// cut. Its rule is the one with the most probability across those windows.
type cluster struct {
	rule  string
	start float64
	end   float64
	prob  float64
	text  string
	// localized marks an anchor found by localize rather than the scan.
	localized bool
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
	var votes []map[string]float64
	open := false
	for _, w := range scored {
		if w.Rule == KeepRule || w.Prob < threshold {
			open = false
			continue
		}
		if !open {
			out = append(out, cluster{rule: w.Rule, start: w.Start, end: w.End, prob: w.Prob, text: w.Text})
			votes = append(votes, map[string]float64{})
			open = true
		} else {
			c := &out[len(out)-1]
			c.end = w.End
			c.text += " " + w.Text
			c.prob = max(c.prob, w.Prob)
		}
		for rule, p := range w.Probs {
			if rule != KeepRule {
				votes[len(votes)-1][rule] += p
			}
		}
	}
	for i, v := range votes {
		for rule, p := range v {
			if p > v[out[i].rule] || (p == v[out[i].rule] && rule < out[i].rule) {
				out[i].rule = rule
			}
		}
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
		Localized:   c.localized,
		Start:       c.start,
		End:         c.end,
	}
	rule := d.rule(c.rule)

	// Candidates straddle each anchor edge. The scan pass anchors at the start
	// of a fixed-width window, so the real boundary can sit on either side of
	// it: offering only earlier sentences means a late anchor can never be
	// pulled back far enough, and an early one can never be corrected at all.
	// The start reaches AnchorInteriorSpan into the anchor, never less than one
	// scan window and never past the anchor's end.
	inside := max(min(c.end-c.start, d.Opts.AnchorInteriorSpan), d.Opts.ScanWindow)
	before := d.candidates(cues, max(0, c.start-d.Opts.LeadInSpan), c.start+inside, "L")
	after := d.candidates(cues, max(0, c.end-d.Opts.ScanWindow), c.end+d.Opts.TailSpan, "R")

	if len(before) > 1 {
		state := d.startState(cues, c, before)
		region.StartState = state

		var (
			curve []float64
			err   error
		)
		if d.Opts.StartWeights != nil {
			curve, err = d.featureCurve(ctx, cues, c, before)
		} else {
			curve, err = d.askCurve(ctx, state, before, d.startPredicate(rule))
		}
		if err != nil {
			return region, err
		}
		idx, step, ok := d.startEdge(curve, before, c.start)
		if ok && c.localized && !d.localizedStartMove(before[idx].Start, step, c) {
			ok = false
		}
		if ok && !c.localized && before[idx].Start < c.start && !d.recognizesRead(curve, before, c.start) {
			// The step pulls the start earlier, but nothing inside the
			// anchor scores as a read, so the curve doesn't know what the
			// read looks like and its step is noise. The start weights
			// describe pitches: a break bumper ("We'll be right back")
			// scored 0.08, and a 0.17 step took the start 28s into the
			// news segment before it. Fall back to classifying sentences.
			ok = false
		}
		for i, s := range before {
			region.StartCurve = append(region.StartCurve, CurvePoint{
				ID: s.ID, Start: s.Start, End: s.End, P: curve[i], Text: s.Text, Edge: ok && i == idx,
			})
		}
		switch {
		case ok:
			region.Start = before[idx].Start
			region.LeadIn = c.start - region.Start
			region.StartPick, region.StartStep = before[idx].ID, step
		case !c.localized:
			// No step and nothing inside the anchor clears the fallback
			// floor, so the curve says nothing. The anchor's own start is a
			// scan-window edge that can sit well inside the content before
			// the read: NYT Daily's credits were cut from 18s into the news
			// item before them. Ask the scan's question of the anchor's
			// sentences instead and start at the first confident one.
			classified, err := d.classify(ctx, cues, [][2]float64{{c.start, c.start + inside}})
			if err != nil {
				return region, err
			}
			for _, s := range classified[0] {
				if s.P >= d.Opts.AnchorThreshold {
					region.Start = s.Start
					region.LeadIn = c.start - region.Start
					region.StartPick = "classified " + s.ID
					break
				}
			}
		}
	}

	if d.Opts.OutroTrimFloor <= 1 && region.Start < c.end && d.isOutro(cues, c) {
		if err := d.trimOutro(ctx, cues, c, &region); err != nil {
			return region, err
		}
	}

	if c.localized {
		// A localized anchor was already decided sentence by sentence, so
		// its end is where that run of sentences ends. The end fit is built
		// for scan windows: its candidates begin a window before the
		// anchor's end, which for a few-second anchor is content before the
		// read, and on Klatt tG_DkBvgQII it widened past 28s of content to
		// the next read.
		region.EndReason = "localized run"
		return region, nil
	}

	if len(after) > 1 {
		endState := d.endState(c, after)
		region.EndState = endState
		curve, err := d.askCurve(ctx, endState, after, d.endPredicate(rule))
		if err != nil {
			return region, err
		}
		edge := fitEnd(curve, d.Opts.EndFitWindow, d.Opts.EndMinStep, d.Opts.EndNeverReturns)
		var fitted []float64
		if d.Opts.EndWeights != nil && (edge.ambiguous || edge.idx < len(after)) {
			fitted, err = d.endAdCurve(ctx, c, after, curve)
			if err != nil {
				return region, err
			}
		}
		if edge.ambiguous {
			// No step to trust. The first candidate sits a scan window inside
			// the anchor, and falling back to it cut away what the scan was
			// confident about. On a read that ends the video it shrank the
			// region below MinRegion or to a negative length, and the read
			// was dropped. The anchor's own end is too coarse the other way:
			// a window can carry a sign-off or real content past the read.
			// So find the first sentence inside the anchor, from the region's
			// start, that the fitted ad score calls ad (the start itself is
			// often a lead-in it scores lower) and let the walk below run from
			// there. Only inside the anchor: past it, the first ad-scored
			// sentence can be a different read minutes away. With no such
			// sentence, or no weights, keep the anchor's end.
			first := -1
			for i, s := range after {
				if fitted != nil && s.Start >= region.Start && s.Start < c.end && fitted[i] >= d.Opts.EndExtendFloor {
					first = i
					break
				}
			}
			if first >= 0 {
				edge.idx = first
			} else {
				for edge.idx < len(after) && after[edge.idx].Start < c.end {
					edge.idx++
				}
			}
		}
		if fitted != nil {
			from := edge.idx
			for edge.idx < len(after) && fitted[edge.idx] >= d.Opts.EndExtendFloor {
				edge.idx++
			}
			if n := edge.idx - from; n > 0 {
				edge.reason += fmt.Sprintf(", extended %d past sentences still scored as ad", n)
			}
		}
		for i, s := range after {
			region.EndCurve = append(region.EndCurve, CurvePoint{
				ID: s.ID, Start: s.Start, End: s.End, P: curve[i], Text: s.Text, Edge: i == edge.idx,
			})
			if fitted != nil {
				region.EndCurve[i].Ad = fitted[i]
			}
		}
		region.EndReason = edge.reason
		switch {
		case edge.idx >= len(after):
			// The program never comes back within the candidates, so the read
			// runs through the last of them, and never ends before the anchor.
			region.End = max(after[len(after)-1].End, c.end)
			region.EndPick = NoReturn
		default:
			// The edge is the first sentence back on the program's subject,
			// so the read ends where that sentence begins.
			region.End = after[edge.idx].Start
			region.EndPick = after[edge.idx].ID
		}
		region.EndStep = edge.step
		region.TailOut = region.End - c.end
	}
	return region, nil
}

// localizedStartMove reports whether the start fit may move a localized
// anchor's start to start. The anchor is a run of sentences already
// classified one by one, so the fit may only refine it: never past the run's
// end, which on three NYT Daily break bumpers pushed the start beyond the end
// and the region was dropped, and never earlier on a shallow step.
func (d *Detector) localizedStartMove(start, step float64, c cluster) bool {
	if start >= c.end {
		return false
	}
	return start >= c.start || step >= d.Opts.LocalizedMinStep
}

// recognizesRead reports whether any start candidate at or after the anchor
// scores StartFallbackFloor or more.
func (d *Detector) recognizesRead(curve []float64, cands []transcript.Sentence, anchor float64) bool {
	for i, s := range cands {
		if s.Start >= anchor && curve[i] >= d.Opts.StartFallbackFloor {
			return true
		}
	}
	return false
}

// isOutro reports whether a read runs to the end of the video, judged by how
// little narration follows it.
func (d *Detector) isOutro(cues []transcript.Cue, c cluster) bool {
	return len(joinCues(transcript.Slice(cues, c.end, c.end+d.Opts.ContextSpan))) < outroNarration
}

// outroNarration is the most narration, in characters, that can follow a read
// still counted as an outro. The subject sample that follows a mid-video read
// runs about 1,200; the outros that started early had about 100 or less.
const outroNarration = 400

// trimOutro moves an outro's start later, to the first sentence from the
// fitted start on that the scan question scores at OutroTrimFloor. See
// OutroTrimFloor.
func (d *Detector) trimOutro(ctx context.Context, cues []transcript.Cue, c cluster, region *Region) error {
	classified, err := d.classify(ctx, cues, [][2]float64{{region.Start, c.end}})
	if err != nil {
		return err
	}
	region.Trim = classified[0]
	for _, s := range region.Trim {
		if s.P < d.Opts.OutroTrimFloor {
			continue
		}
		if s.Start > region.Start {
			region.Start = s.Start
			region.LeadIn = c.start - region.Start
			region.StartPick = "trimmed to " + s.ID
		}
		return nil
	}
	return nil
}

// startEdge places the opening edge on a start curve. It returns the candidate
// index, the step's height, which is a usable confidence (a tall step is an
// unambiguous boundary), and false when the anchor's own start should stand.
//
// The step is fitted only over candidates within StartFitSpan of the anchor.
// When that finds no rise, the edge falls to the first sentence inside the
// anchor scoring StartFallbackFloor or more, with no step. The closing edge
// does not use this; see fitEnd.
func (d *Detector) startEdge(curve []float64, cands []transcript.Sentence, anchor float64) (int, float64, bool) {
	lo := 0
	for lo < len(cands) && cands[lo].Start < anchor-d.Opts.StartFitSpan {
		lo++
	}
	if idx, step := fitStart(curve[lo:]); idx > 0 {
		return lo + idx, step, true
	}
	for i, s := range cands {
		if s.Start >= anchor && curve[i] >= d.Opts.StartFallbackFloor {
			return i, 0, true
		}
	}
	return 0, 0, false
}

// askCurve asks one predicate of every candidate at once, repeats it, and
// returns the averaged answers in candidate order.
func (d *Detector) askCurve(ctx context.Context, state string, cands []transcript.Sentence, instructions func(transcript.Sentence) string) ([]float64, error) {
	questions := make(map[string]jev.Question, len(cands))
	for _, s := range cands {
		questions[s.ID] = jev.Noul(instructions(s))
	}

	// Repeat and average. The predicate is noisiest right at the boundary,
	// which is the only place it matters, and extra questions cost almost
	// nothing beyond the tokens to state them.
	runs := make([][]float64, 0, d.Opts.Repeats)
	for i := 0; i < d.Opts.Repeats; i++ {
		resp, err := d.Client.Ask(ctx, state, questions)
		if err != nil {
			return nil, err
		}
		curve := make([]float64, len(cands))
		for i, s := range cands {
			if curve[i], err = noulAnswer(resp.Answers, s.ID); err != nil {
				return nil, fmt.Errorf("boundary: %w", err)
			}
		}
		runs = append(runs, curve)
	}
	return average(runs), nil
}

// featureCurve scores every candidate with the fitted weights. The model never
// judges where the boundary is; it only answers narrow factual questions, and
// the combination happens here where it can be inspected and refitted for
// nothing.
func (d *Detector) featureCurve(ctx context.Context, cues []transcript.Cue, c cluster, cands []transcript.Sentence) ([]float64, error) {
	// The state must match the one the weights were fitted on. Feature
	// extraction happens under the start predicate's state, so the live path
	// asks under the same one; answers given in a different context are
	// different features, and the weights do not transfer.
	feats := Features(d.Opts.Subject)
	values, err := d.ExtractFeatures(ctx, cands, d.startState(cues, c, cands), feats)
	if err != nil {
		return nil, err
	}

	curve := make([]float64, len(cands))
	for i, s := range cands {
		curve[i] = d.Opts.StartWeights.Score(values[s.ID])
	}
	return curve, nil
}

// endAdCurve scores every closing-edge candidate with the fitted end weights.
// The weights were fitted on answers given under the end state, with the
// predicate's own answer as one more input, so both are reproduced here.
func (d *Detector) endAdCurve(ctx context.Context, c cluster, cands []transcript.Sentence, predicate []float64) ([]float64, error) {
	values, err := d.ExtractFeatures(ctx, cands, d.endState(c, cands), EndFeatures(d.Opts.Subject))
	if err != nil {
		return nil, err
	}
	curve := make([]float64, len(cands))
	for i, s := range cands {
		values[s.ID][PredicateFeature] = predicate[i]
		curve[i] = d.Opts.EndWeights.Score(values[s.ID])
	}
	return curve, nil
}

// PredicateFeature is the end weights' name for the "has the program returned"
// predicate's averaged answer.
const PredicateFeature = "predicate_returned"

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
				"program returned to its own subject matter by sentence [%s], leaving that "+
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
		b.WriteString("WHAT THIS PROGRAM IS ACTUALLY ABOUT:\n")
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

// choiceAnswer returns answers[id] after checking it is a choice among
// criteria with a complete probability distribution, and that distribution.
// A missing or malformed answer is an error: read as zeros it would look like
// confident content and move a cut.
func choiceAnswer(answers map[string]jev.Answer, id string, criteria map[string]string) (jev.Answer, map[string]float64, error) {
	a, ok := answers[id]
	if !ok || a.Type != string(jev.TypeChoice) || len(a.Probabilities) == 0 {
		return a, nil, fmt.Errorf("missing or invalid answer for %s", id)
	}
	if _, ok := criteria[a.Choice]; !ok {
		return a, nil, fmt.Errorf("unknown choice %q for %s", a.Choice, id)
	}
	if len(a.Probabilities) != len(criteria) {
		return a, nil, fmt.Errorf("incomplete probabilities for %s", id)
	}
	var total float64
	probs := make(map[string]float64, len(criteria))
	for key := range criteria {
		p, ok := a.Probabilities[key]
		if !ok {
			return a, nil, fmt.Errorf("missing probability %q for %s", key, id)
		}
		if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 1 {
			return a, nil, fmt.Errorf("invalid probability for %s", id)
		}
		probs[key] = p
		total += p
	}
	// Allow rounding in the provider's distribution, but not empty or
	// unnormalized scores being mistaken for a successful clean answer.
	if math.Abs(total-1) > 0.02 {
		return a, nil, fmt.Errorf("probabilities for %s sum to %g, want 1", id, total)
	}
	return a, probs, nil
}

// noulAnswer returns answers[id]'s noul after checking it is a noul in [0,1].
func noulAnswer(answers map[string]jev.Answer, id string) (float64, error) {
	a, ok := answers[id]
	if !ok || a.Type != string(jev.TypeNoul) {
		return 0, fmt.Errorf("missing or invalid answer for %s", id)
	}
	if math.IsNaN(a.Noul) || a.Noul < 0 || a.Noul > 1 {
		return 0, fmt.Errorf("invalid noul for %s", id)
	}
	return a.Noul, nil
}
