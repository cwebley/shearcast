# Finding the edges: proposed improvements

Planning notes from 2026-10-02. These are proposed experiments, not implemented
changes or measured accuracy gains. The review covered the codebase tour,
`internal/detect`, the fitting and scoring scripts, and the local experiment
history.

## Keep the core approach

Use captions to locate unwanted passages and audio pauses to refine the cuts.
Find confident promotional anchors, then judge surrounding sentences in the
context of the promotion they may introduce or conclude. Narrow feature questions
with an inspectable fitted combination have worked better on starts than asking
the model to choose a boundary directly. The cumulative return predicate remains
useful on ends.

Cutting real content is the worse error. A few retained ad seconds are preferable
to losing an interview answer. Future experiments should measure that tradeoff
explicitly.

## 1. Make offline replay match production

`fit/fit_weights.py` approximates the production decision. Its start replay has
the changepoint and score fallback, but omits the surrounding decisions in
`Detector.bound`:

- Restrictions on moving localized starts.
- The check that the start curve recognizes the read inside the anchor.
- Sentence-classification fallback.
- Outro trimming.

The end replay substitutes the anchor start for the refined region start when
choosing an ambiguous-end fallback. Feature-only evaluation also conditions on
detected regions and does not measure all misses or false cuts.

Proposed work:

- Reuse the production decision logic for offline replay, with recorded answers
  for every branch that needs model evidence. Missing evidence should be reported
  as an incomplete replay rather than silently approximated.
- Record model, prompt/feature version, detector options, weight identity and
  label version alongside an experiment so comparisons have known inputs.
- Add whole-episode interval metrics: seconds of real content removed and seconds
  of required unwanted material retained. Count entirely missed blocks and short
  errors too; treat borderline labels as optional.
- Keep per-edge errors and block recall as diagnostics. Report detector cuts and
  final rendered keep ranges separately, since snapping and minimum kept lengths
  can change the result.

Acceptance: replay reproduces recorded production decisions for all supported
branches on the same evidence. Incomplete records are counted explicitly. A
weight change is judged by its final decisions, not sentence accuracy alone.

Relevant code: `fit/fit_weights.py`, `fit/score_labels.py`,
`internal/detect/detect.go`, `internal/pipeline/pipeline.go`.

## 2. Measure boundary uncertainty

Step height alone does not establish confidence. Two distant split locations can
have nearly identical fit error despite a tall step. A shallow step can be stable.
The changepoint always proposes a split before the surrounding guards decide
whether to accept it. The experiment history includes a 55-second edge movement
between identical runs.

Proposed measurements:

- Improvement over fitting one flat level with no boundary.
- Fit errors of alternative split locations, including how widely near-equal
  candidates are separated in seconds.
- Movement under small score perturbations and repeated model calls. Use measured
  run-to-run variation; do not assume model answers are independent.

Use these first as diagnostics. Then test whether additional questions focused on
the disputed passage reduce large mistakes at acceptable cost. Any conservative
selection policy should favor keeping content when evidence remains ambiguous.
The fitted scores are not automatically calibrated probabilities of a correct cut.

Do not simply add a larger minimum start step and fall back to the coarse anchor.
That experiment previously reduced within-five-second accuracy from 23/41 to
20/41. Weak steps can be correct, and an anchor can begin inside genuine content.

Acceptance: uncertainty measurements identify large errors or unstable boundaries
on development data, and the resulting policy improves the content-loss/ad-left
tradeoff on fresh data. No uncertainty threshold has been chosen yet.

Relevant code: `internal/detect/edge.go`, `Detector.startEdge`,
`Detector.askCurve`, and the recorded start/end curves.

## 3. Improve the evidence supplied as context

`Detector.subject` takes text immediately after the coarse anchor end and labels
it as the video's own narration. The ad can continue past that anchor, so its tail
can be presented as an example of real content. Similarly, scan windows quoted as
the advertisement can contain genuine narration.

Experiment with:

- Subject examples from confidently content-classified windows outside suspected
  unwanted regions.
- A confidently promotional core quoted separately from uncertain surrounding
  text.
- Neutral descriptions when the context is uncertain, rather than asserting that
  it is definitely content or definitely an ad.

Compare the current and revised states on the same development episodes, including
early reads, short promos, outros and adjacent reads. Changing the state changes
the extracted features, so collect new answers and evaluate whether the weights
need refitting. Replaying old feature values cannot measure this experiment.

Acceptance: fewer large content-loss errors without a disproportionate increase
in missed or retained ads. No gain is established yet.

Relevant code: `Detector.subject`, `Detector.startState`, `Detector.endState`,
and `internal/detect/features.go`.

## 4. Compare with a constrained interval model

The original start predicate is cumulative: "has this ad begun by sentence k?"
Its true answer switches once. The fitted start score instead learns sentence
membership in an unwanted passage. That score can rise and fall around a short
promo or neighboring reads. Some code comments still describe both as cumulative.

After improving evaluation, try a small offline challenger that scores possible
start/end pairs around a confirmed promotional core:

```text
content -> this ad -> content
```

Allow the ad to touch either end of the episode. Constrain the search to the
particular read so it cannot absorb neighboring reads across genuine content.
Retain the end predicate as evidence; replacing it outright with sentence scores
previously performed worse. The point of the experiment is to let both edges
inform one another while respecting what each score actually measures.

Acceptance: compare final cuts against the current independent-edge policy on
identical recorded evidence where possible. Adopt only if gains justify the added
complexity. A joint model is an experiment, not a selected architecture.

## 5. Establish a fresh evaluation set

Video- and channel-held-out fitting are useful, but they only hold data out of
weight fitting. They do not undo prompt, feature or policy choices made after
inspecting those examples.

The existing `data/test-labels` set has influenced development. The local history
explicitly records a localized-end fix prompted by a test-set failure. Keep that
set as regression/development evidence; do not describe it as untouched test data.

Before selecting the next policy:

- Freeze the cut policy and labeling definitions.
- Select fresh episodes, including unfamiliar channels, and label them without
  showing detector decisions.
- Keep final evaluation separate from tuning. Once a failure guides a fix, record
  that exposure and use fresh examples for the next independent evaluation.
- Listen to a small difficult-boundary sample, including actual edited joins.
  Blind Opus labels and SponsorBlock agreement help establish semantic boundaries,
  but transcript agreement alone does not establish listening quality.
- Report episode/channel counts and large-error cases alongside aggregate scores.
  Repeat a targeted sample to distinguish model variation from policy changes.

## Small follow-up

The `continues_previous` question in `internal/detect/features.go` contains a
literal `[%s]` that the `seam` helper never substitutes. The helper already quotes
the candidate and its predecessor, but the unresolved placeholder adds ambiguity.
Fix and verify the generated question during the next prompt experiment. Account
for the prompt change when comparing newly extracted features with existing data.

## Suggested order

1. Make replay faithful and add whole-episode error metrics.
2. Establish the current baseline and reserve fresh evaluation data.
3. Measure boundary stability and test context selection as separate experiments.
4. Test targeted extra questions for uncertain boundaries.
5. Try the joint interval model if remaining failures point to independent-edge
   decisions as the limiting factor.

For each experiment, retain the baseline, inputs, settings, label version,
per-episode errors and model cost. Change one hypothesis at a time. Use fresh
evaluation only after choosing a candidate on development data.
