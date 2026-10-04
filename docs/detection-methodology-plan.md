# Finding the edges: proposed improvements

Planning notes from 2026-10-02. These are proposed experiments, not implemented
changes or measured accuracy gains. The review covered the codebase tour,
`internal/detect`, the fitting and scoring scripts, and the local experiment
history.

Implementation update, 2026-10-02: the evaluation foundation now records complete
model evidence during `redetect`, replays it through the production detector with
`cmd/replay`, and scores whole-episode interval errors. See
[the experiment workflow](../fit/README.md). Synthetic integration checks cover
production/replay parity and missing evidence. The full 44-episode development
baseline and first neutral-context pilot have since been collected and
replay-verified. Blind labels for the reserved evaluation episodes are still
needed; no accuracy gain is claimed.

### First context pilot, 2026-10-02

The pilot used six existing development episodes across six channels, covering
an early read, short promos, adjacent reads, credits, an outro and an ad-free
control. The opt-in `redetect -neutral-context` variant changed context headings
only. Quoted source text, questions, shipped weights and cut guards stayed the
same. Each full live run used the same captions, options and label hashes.

| Detector metric | Current context | Neutral context |
| --- | ---: | ---: |
| Content seconds removed | 1.90 | 1.90 |
| Required unwanted seconds retained | 53.95 | 61.73 |
| Episodes with a continuous content cut over five seconds | 0 | 0 |
| Entirely missed required blocks | 2 of 13 | 2 of 13 |
| Calculated collection cost | $0.037833 | $0.038325 |

Both six-episode runs replayed completely with no decision mismatches. The two
episodes with changed interval scores were collected again under both settings;
all four repeat records also replayed completely. The Space Time early promo
started at 50.00 seconds under current context and 57.80 seconds under neutral
context in both pairs, retaining 7.80 more labeled unwanted seconds. A 0.01-second
NYT Daily difference reversed on repetition, and its localized anchors changed
between live runs. Model answers varied even when questions were unchanged, so
two pairs are not enough to establish a general causal effect.

Do not advance the wording-only variant on this evidence: it showed no content
loss benefit and retained more unwanted material. It remains explicitly opt-in
for reproducibility. Because the baseline had no large content cuts, this pilot
cannot test whether the variant reduces their frequency. It has no rendered view.

Outputs, input hashes, per-episode intervals, replay summaries and usage are in
ignored `work/edge-evaluation-20261002/`. `comparison.json` checks matching inputs
and labels and retains both full runs and both two-episode repeats. Total live
calculated cost was $0.100563; replay made no model calls. Four fresh episodes
across three channels, including Veritasium and Technology Connections as
unfamiliar channels, were reserved before candidate collection. Their labels and
listening review are pending; they have not been used to select this variant.

The full development baseline below is now available for testing confident-content
and promotional-core selection as separate hypotheses. Keep the unresolved
`continues_previous` placeholder cleanup separate so attribution remains clear.

Batch collection is now strictly cache-only. `redetect -check-cache` verified
usable metadata and captions for all 44 existing development episodes without
YouTube requests or model calls. Every collection validates and loads the entire
selection first and fails before collection if any cached inputs are unusable.
The 44-episode model run has now completed.

### Full development baseline, 2026-10-02

All 44 existing development episodes across 13 channels were collected using
cached inputs, current default context, shipped weights and configured channel
rules, with Gabfest's configured unweighted fallback. They cover 30.20 hours.
All 44 replayed with zero incomplete episodes, decision mismatches or file
failures. Live and replayed scores match exactly, including per-episode intervals.
Label hashes are unchanged. These are development results, not an independent
evaluation of accuracy.

| Metric | Raw regions | Final detector cuts |
| --- | ---: | ---: |
| Content seconds removed | 73.01 | 74.06 |
| Required unwanted seconds retained | 436.47 | 436.47 |
| Episodes with a continuous content cut over five seconds | 2 | 2 |
| Worst continuous content cut | 30.72s | 30.72s |
| Entirely missed required blocks | 4 of 96 | 4 of 96 |

The detector view includes clamping and merging. Merging adds 1.05 seconds of
content loss in one Hutchins episode. There is no rendered-audio view.
Collection cost was $0.375857, both calculated and provider-reported, over 842
model requests. Usage and cost observations are complete. Replay cost was zero,
and the collection made no YouTube requests.

Caption inspection of the largest errors found:

- Space Time `YSwRqNCeP9k`, "So You Want to Understand Quantum Mechanics…":
  the optional promo ends at 7682s, but the cut continues through 7712.72s,
  removing 30.72s of closing discussion and an upcoming-episode teaser. The end
  ad scores for those candidates are only 0.04 to 0.19. The ambiguous-end
  fallback still keeps the coarse anchor end. This is a decision-policy issue
  worth isolating on the recorded evidence; better context alone is not an
  established remedy.
- Hank `TmkA_-zGNbE`, "Most of the Earth is Covered in Glass": a cut starts at
  1284.64s, 5.36s before the optional guest plug, removing thanks to the guest.
  Outro trimming finds no sentence reaching its confidence floor and leaves
  the fitted early start unchanged. This is another disputed-outro case.
- Decoder `4t8RhKbkb70`, "Can Cloudflare save the web from AI?": only the
  introduction of the required EY-sponsored segment is removed, retaining
  79.60s of sponsored dialogue. The post-anchor subject sample is that dialogue
  described as the video's own narration; the quoted "advertisement" is mostly
  the preceding real interview. This directly motivates the separate
  context-selection hypothesis.

The other largest retained-material episodes are Space Time `568-39aOZkw`
at 64.40s, Space Time `Yid9cO7peXg` at 42.96s, Space Time `I07RBedXRYA`
at 31.20s, and Cool Worlds `FKSdkVIySq0` at 24.79s. The four entirely missed
required blocks are two opening Space Time credits of 2s and 3s, plus two
T3 self-promos of 16s and 3s in `jgGyX7MPPVg`.

Unchanged-policy live runs also vary. Of the six episodes shared with the pilot,
T3 `jLgpzgpsWPc` now detects the previously missed 12s plug, and the HoTU outro
in `uoEffocWP4A` retains 5.09 additional seconds. Inputs, labels and resolved
settings match the pilot. Repeated live controls remain necessary for attributing
a prompt/context gain; exact offline replay does not imply deterministic live
model answers.

Records and scores are in ignored `work/edge-evaluation-20261002/full-baseline/`
and `full-baseline-replayed/`. `full-baseline/analysis.json` verifies input and
replay provenance, preserves channel metrics, ranks errors and includes caption
context. `full-baseline-plan.md` records commands, findings and the recommended
next experiment. The four reserved evaluation episodes remain unused.

Recommended next bounded experiment: evaluate conservative ambiguous-outro
decisions on the complete recorded development set, honoring the agreed
preference for retaining disputed content. Keep its selection-policy changes
separate from a newly collected context-selection experiment for mixed anchors
such as Decoder. No candidate policy or context-selection change has been
implemented or selected by this baseline run.

### Conservative ambiguous-end experiment, 2026-10-04

The default code first reproduced all 44 recordings exactly. Inventory found
seven ambiguous boundaries across six episodes, including one already dropped
HoTU anchor. Five had an in-anchor ad-scored candidate at or after the refined
start. Only Space Time `YSwRqNCeP9k` and Decoder `4t8RhKbkb70` reached the
no-ad-candidate fallback.

The opt-in `ConservativeAmbiguousEnd` candidate changes only that fallback.
With fitted end scores, it preserves the first remaining in-anchor sentence
when every remaining in-anchor candidate scores at most `1 - EndExtendFloor`
and the cut still meets `MinRegion`. The ceiling is 0.4 with the existing 0.6
extension floor. The rule was frozen before scoring, with no threshold search.
It requires `0.5 < EndExtendFloor <= 1` to keep a gap between low-ad and ad
evidence. Missing weights, disputed scores and too-short cuts retain the old
fallback. These scores are not calibrated probabilities.

All 44 candidate episodes replayed completely, with baseline verification
enabled and zero new model or YouTube calls. Label hashes, captions, recorded
answers, prompts, context, weights, starts and localization stayed identical.

| Metric | Baseline | Conservative end |
| --- | ---: | ---: |
| Content seconds removed, raw regions | 73.01 | 42.29 |
| Content seconds removed, final detector cuts | 74.06 | 43.34 |
| Required unwanted seconds retained, both views | 436.47 | 436.47 |
| Worst continuous content cut | 30.72s | 5.36s |
| Episodes with a continuous content cut over five seconds | 2 | 1 |
| Entirely missed required blocks | 4 of 96 | 4 of 96 |
| Additional optional promo material retained | 0 | 0.32s |

Only Space Time `YSwRqNCeP9k` changes. Its start remains 7658.87596s; the end
moves from 7712.72s to 7681.68s. That saves all 30.72s of lost closing discussion
and retains 0.32s more of the optional promo, labeled through 7682s. The first
preserved sentence begins "tab. The delayed choice quantum eraser." The small
promo remnant comes from the caption sentence crossing the labeled boundary.
The 23-second optional promo otherwise remains cut.

The other 43 episodes have identical decisions and error intervals, including
Decoder's sponsored introduction, Hank's early start, Gabfest's unweighted
fallback and the five ad-seeded ambiguous ends. No additional required material
is retained, no content cut worsens and no new region is dropped. Merging still
adds the same 1.05s of content loss in Hutchins. Per-episode intervals, entirely
missed blocks, dropped regions, hashes and unchanged-evidence assertions are in
`work/edge-evaluation-20261002/ambiguous-end-candidate/comparison.json`.

Keep this candidate for fresh evaluation, but leave it explicitly opt-in. It
repairs the target on development evidence with a 0.32s optional-tail tradeoff;
only one episode exercises its new decision in this set. No independent accuracy
or listening-quality gain has been established. Start-boundary changes and better
context selection remain separate experiments, and the four reserved evaluation
episodes remain unused. The frozen rule and commands are in ignored
`work/edge-evaluation-20261002/ambiguous-end-plan.md`; regenerate the comparison
with `python3 work/edge-evaluation-20261002/compare_ambiguous_end.py`.

Verification passed: `go test -race ./...`, `go vet ./...`, `go build ./...`,
seven Python scorer tests, Go formatting and `git diff --check`. Separate
standards and specification reviewers found no actionable issues in the bounded
change. Review results are saved beside the candidate comparison.

### Independent end-policy evaluation, 2026-10-04

The four reserved episodes were labeled by isolated caption-only agents before
fresh detector collection. Label definitions, source inputs and the candidate
rule were frozen. Three missing episodes had metadata/captions acquired in a
separate user-authorized setup step. Collection itself remained strictly
cache-only. Active channel configuration was preserved; unfamiliar channels used
a scratch config with global rules and shipped weights.

All four baselines replayed exactly and all four candidate episodes completed.
Both policies remove 10.359s of labeled content and retain 9.345s of required
unwanted material in both raw and final views. No required blocks are entirely
missed. Cuts, error intervals and dropped regions are identical. The key coverage
limit is zero eligible shortening decisions. The retained Technology Connections
cross-plug before bloopers has an ambiguous end with an ad seed and keeps its walk.
An already dropped T3 boundary has a second ambiguous end and reaches the
no-ad-seed fallback. Its first low-ad candidate starts at the refined cut start,
leaving zero seconds against `MinRegion=8`, so the candidate's guard rejects
shortening. Count retained and dropped boundaries in coverage reports.

A separate two-episode cache-ready follow-up covered Cool Worlds and NYT Daily,
selected by metadata before blind labeling. Both baselines replayed exactly and
both candidate episodes completed. Both policies remove 6.422s of content and
retain 18.526s of required unwanted material. Three short required blocks are
entirely missed. Cool Worlds supplies genuine closing credits/support promotion;
NYT supplies credits followed by a sign-off. Both closing ends use `never returns`,
so neither exercises the new ambiguous fallback. All cuts again match.

Across the original four and separate follow-up two, the candidate saves no
additional content and retains no additional required or optional material.
There are no new dropped regions or observed regressions. This verifies neighboring
behavior and the minimum-region guard, but adds no positive shortening example.
Keep the candidate frozen and opt-in, rather than adopting it on six unchanged
episodes. Next seek a bounded, separately reported transcript-screened sample of
promos followed by genuine closing discussion, with blind labels before cuts.

Inspection also found existing errors beyond this experiment's branch: Veritasium
cuts 9s of an acknowledgement excluded by the blind labels under a stepped end;
NYT cuts 6.24s of its sign-off under `never returns`. Preserve their intervals as
separate diagnostic evidence. No label, start, prompt, context or additional end
policy changes were made in response.

Detector collection cost was $0.038800566 over 115 requests, with complete usage
and matching provider cost. This excludes labeling-agent harness usage. Replays
made zero model or YouTube calls. No audio rendering/listening evaluation was
performed. Whole-second labels limit interpretation of subsecond differences.
Results and reproducible provenance checks are in ignored
`work/edge-evaluation-20261002/independent-end-evaluation/results.md` and the
`comparison.json` files there and in `independent-end-followup/`. The original
four-episode reservation is now consumed evaluation evidence, not an untouched
set for future tuning.

Evaluation tooling passed Go vet/build and `git diff --check`; frozen-label and
source-provenance checks passed. Standards review found no actionable issues.
Specification review caught a coverage report that omitted dropped regions; the
analysis and narrative were corrected, then independently reverified with no
remaining findings. Production detector code and fitted weights stayed frozen.

### Opt-in configuration, 2026-10-04

`[jev] conservative_ambiguous_end` now sets `ConservativeAmbiguousEnd` through
`Config.DetectOptions`, so `render`, `sync` and `cmd/redetect` collection all
resolve it the same way. It defaults to false. No rule, threshold, prompt,
feature, weight or start-selection logic changed. Config tests cover the
omitted, false and true cases and check that no other option moves.

`internal/pipeline/ambiguous_end_test.go` runs `RenderEpisode` on generated
180s audio with silences near both joins, using a local model fixture that
matches the detector's ambiguous-closing case. Off, the 90-180s cut runs to
the end of the file and the keep range is `[0, 89.9]`. On, the cut ends at
150s, and after snapping the keep ranges are `[0, 89.9]` and `[150.5, 180]`.
The rendered duration matches the keep ranges, and the render record saves the
setting. With the setting on, missing end weights and `min_region = 61` both
reproduce the default cut.

Replay doesn't read live config, so recorded decisions should be unaffected.
Fresh replays of the 44 development episodes and the six independent episodes
confirm it. The default and candidate outputs in
`work/edge-evaluation-20261002/config-integration-20261004/` match the earlier
`ambiguous-end-baseline-verified`, `ambiguous-end-candidate`, `baseline-replayed`
and `candidate` decisions, with no mismatches or incomplete episodes. The only
default/candidate difference is still the Space Time `YSwRqNCeP9k` repair.

The frozen evaluation code is committed as `fd7640a`. The independent
`compare.py` used to hash frozen files from the working tree, so this change to
`internal/config/config.go` broke it. It now hashes them from that commit with
`git show`, which checks the same bytes against the same `settings-freeze.json`.
Both evaluations pass again and produce identical `comparison.json` files.

Limits: the rendered check uses synthetic noise, so it shows the joins land
where intended but says nothing about how the Space Time repair sounds. No
cached source audio was checked for a listening pass.

### Default on, 2026-10-04

`config.Default` now turns the setting on, and only an explicit
`conservative_ambiguous_end = false` turns it off. `detect.Defaults` still
leaves it off, so replays of recorded options and direct detector callers are
unchanged. This was a product decision on thin evidence: the rule changed one
of 97 development cuts and none of six independent episodes. Its only effect
is to keep more audio under guarded conditions, so the risk is leaving a short
promo tail in, not cutting content. Watch new renders whose `EndReason` reads
"preserved low-ad anchor tail" for that failure, and keep collecting a
transcript-screened sample of promos followed by real closing discussion.

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

Agreed ambiguity policy: when a segue remains genuinely disputed, retain the
disputed setup and cut the confirmed promotional core. This sets the direction
for future uncertainty experiments; this evaluation work does not add a new cut
selection policy.

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

The first implementation uses recorded model answers at the detector's client
interface, so the offline command runs `Detector.Run` itself. It records only
visited branches and explicitly rejects missing evidence. The old Python fitter
remains an approximate proposal tool; use the Go replay for final decisions.
Stable weight accumulation removes map-order-dependent rounding. None of the
boundary thresholds, prompts or fitted coefficients changed in this milestone.

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
