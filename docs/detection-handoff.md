# Detection handoff, 2026-09-27

Start here if you're picking up ad-detection accuracy work. This covers what
we're trying to fix, what changed in the last session, what the measurements
said, and what's still broken. Everything below is committed on the
`experiment/detection-evaluation` branch.

## The goal

Cut sponsor reads and self-promotion out of episodes without cutting real
content. Cutting content is the worse error. A few seconds of pitch left in is
annoying. Losing half a minute of an interview answer is what makes people stop
trusting the feed.

The user wants to reach something close to Opus-level accuracy by tuning
what surrounds Jev (edge rules, prompts, fitted weights), not by adding
per-channel tricks. They turned down remembering ad copy across episodes
because it doesn't scale to new shows or to audio-only podcast sources, which
are wanted later (see the RSS memory note).

## How detection works, briefly

1. **Pass 1, scan** (`scan` in `internal/detect/detect.go`). The transcript
   is tiled into 30 to 45s windows, 24 per Jev request. Each window gets one
   choice question (content, sponsor or selfpromo). Windows at 0.85 or above
   become anchors, and same-rule anchors within 90s merge.
2. **Stage 2, bound** (`bound` in the same file). For each anchor, sentences
   around each edge get scored and a two-level step (`changepoint` in
   `internal/detect/edge.go`) is fitted to find the boundary. The start edge
   uses the fitted feature weights in `data/weights.json` (every channel except
   gabfest). The end edge uses the "has the program returned" predicate.
3. `verify` was deleted on 2026-09-28. It asked whether a cut swallows content,
   nothing read the answer, and it didn't separate good cuts from bad (bad cuts
   scored 0.42 to 0.69, good ones 0.24 to 0.61). The `verify` config key is now
   ignored.

## Ground truth and how to measure

- **`data/opus-labels/`** holds 44 videos, one per cached render, with 125
  segments (27 marked borderline). Opus subagents labeled them blind from
  captions. The prompt is in `fit/label_prompt.md`.
- **Agreement with SponsorBlock** where both exist: within 5s at both edges on
  42 of 46 ad blocks, median gap 0.5s. The larger disagreements were
  segmentation (SponsorBlock merging or splitting adjacent ads), not
  boundaries. Opus also found 24 promos SponsorBlock lacks, mostly short
  subscribe asks and five-second sponsor credits.
- **`fit/score_labels.py [RENDERS_DIR] [-v]`** scores render records against
  the labels in under a second. It merges back-to-back labels into one block,
  because a cut spanning two adjacent ads is correct. It treats borderline
  labels as optional. The first scoring pass didn't do this and reported
  phantom 60s content cuts.
- **To measure a code change**, re-render into a scratch directory so the
  cache and the real state file stay untouched:

      ./shearcast render "https://www.youtube.com/watch?v=$ID" -channel $CH \
        -out $SCRATCH/renders/$CH/$ID/render.m4a -state $SCRATCH/state/$ID.json

  Use the URL form, because some IDs start with "-". A render takes about
  2 minutes (it re-downloads audio and re-encodes) and costs about $0.004 to
  $0.01 in Jev calls. All 44 run four at a time in about 30 minutes. YouTube
  audio sometimes returns 403, and retrying one video at a time works.
- **Rules that only change the fit** can be replayed for free on the
  `StartCurve` and `EndCurve` stored in each render record. Anything that
  changes what the model is asked needs a re-render.
- **Edge features for refitting** are in `data/start-features.jsonl` and
  `data/end-features.jsonl`. They hold every candidate of every detected region
  in the labeled videos, with all 18 feature answers. The end rows also carry
  the recorded predicate. `go run ./cmd/featuredump [-edge end]` rebuilds them
  from the cached captions and the anchors in the cached render records, with
  no audio and no YouTube calls (about $0.10 per edge for all 44). It
  replaces the file only after every video succeeds (`-partial` writes the
  successful ones anyway). End rows skip localized regions, which have no
  recorded predicate.
  `python3 fit/fit_weights.py [-edge end] [-v] [-write]` labels the rows from
  the Opus segments, fits the weights, and replays the shipped rule on
  held-out videos and channels next to the baseline, on the same answers.
- **Run-to-run noise is real.** Rendering the same video twice with identical
  code moves sentence scores by 0.005 to 0.03 on average and can flip an edge
  on a shallow step. Decoder 8hwcDaX3ISg flipped 55s between two identical
  runs. Don't trust a single-video difference.

## Current numbers

From `fit/score_labels.py`, 89 required blocks:

| Records | Detected | Start within 5s | Start content cut | Start ad left | End content cut | End ad left |
|---|---|---|---|---|---|---|
| Live cache (mostly old code) | 64 | 40/64 | 15 blocks, 811s | 9, 68s | 0 | 14, 220s |
| Re-render, new start code | 65 | 48/65 | 4 blocks, 110s | 13, 159s | 0 | 18, 264s* |

\* That re-render also had an end-edge change that was later reverted (see
below), so its end column overstates the current end error. The shipped end
code matches the live cache row.

The re-render used a 90s interior reach. The shipped value is 60s, which a
replay on the same curves showed to be as good or slightly better. It hasn't
been re-rendered at 60s.

## What changed in the last session

**Start edge** (`bound` and `startEdge` in `internal/detect/detect.go`, with
tests in `detect_test.go`):

- `AnchorInteriorSpan` (60s). Start candidates now reach up to 60s into the
  anchor, capped at the anchor's end, instead of 30s. An anchor starts wherever
  its scan window starts, which can be half a window before the pitch. In
  Hank Green's MYa4vLcpBqg the plug began 26s into the anchor, only two scored
  sentences fell inside it, and an earlier shirt teaser won the fit.
- `StartFitSpan` (75s). The step is fitted only over candidates within 75s
  before the anchor. All 180s (`LeadInSpan`) are still scored and saved. Over
  the full 180s, any off-subject stretch could win, such as Vergecast's "90
  Seconds on The Verge" news brief, which got cut along with the ad. No correct
  edge in the data sat more than 61s before its anchor. The old code comment
  claiming segues start "100s+ before the anchor" confused lead-in with ad
  length. A 3-minute ad gets a roughly 3-minute anchor.
- `StartFallbackFloor` (0.5). When no step rises, the edge falls to the first
  sentence inside the anchor scoring 0.5 or more, instead of the window edge.

The end edge was unchanged in that session. Its candidates still start 30s
inside the anchor. For the end-edge change made on 2026-09-28, see failure 2.

**Prompts.** A spelling pass changed "programme" to "program" inside the
prompts in `detect.go` and `features.go`. All 39 videos were re-rendered, and
the effect was indistinguishable from run-to-run noise. The weights and
`EndMinStep` don't need refitting.

**Installed and published.** `go install ./cmd/shearcast` put the new code in
`~/go/bin`. The Hank Saturn episode (MYa4vLcpBqg) was reprocessed with it. Its
16:47 cut, which took 26s of a Dragonfly answer, now starts at 17:16. Two
channels were added: `nytdaily` (NYT Daily playlist) and `hank` (Hank Green).

## Experiments and verdicts

| Idea | Result | Verdict |
|---|---|---|
| Minimum step on the start edge, falling back to the anchor | Worse on 41 SponsorBlock starts: within-5s went from 23 to 20. Weak steps are often correct, and the anchor fallback is itself bad. It also wouldn't have fixed Hank, whose curve never rose. | Rejected |
| Cap the start fit at 75s before the anchor | Content cut at the start went from 511s to 159s on 41 starts | Shipped |
| Score deeper into the anchor, start edge | Within the Opus labels, start content cut went from 844s to 110s | Shipped (60s) |
| Same interior reach for the end edge | Listing interior sentences in the end state blurred the model's answers, and four ends moved 4 to 8s early | Reverted |
| Plain threshold instead of a changepoint | No cutoff works. At 0.3 it cut 604s of content, and at 0.7 it left 645s of ads | Rejected |
| Changepoint, then walk back while scores stay above a per-curve cutoff (a quarter from low to high, stepping over one dip) | Against Opus: within-5s 50/65 vs 48/65, but content cut 7 blocks (172s) vs 4 (110s) | Not shipped. It replays on stored `StartCurve` points, so it's cheap to retry with a higher cutoff |
| Refit `weights.json` on the Opus labels with the original 17 features (2,465 sentences, 38 videos, 13 channels) | Held out by channel: within-5s 49/66 vs 48/66, but content cut 6 blocks (161s) vs 5 (136s) and ad left 11 (222s) vs 13 (146s). Training only on fit-window rows, other L2 strengths, and shrinking toward the old weights all came out even or worse | Not shipped |
| New `leads_into_ad` feature ("belongs to a lead-in written for the advertisement"), refit with content sentences weighted 3× | Held out by channel, on the same answers: within-5s 56/66 vs 47/66, content cut 4 blocks (142s) vs 5 (136s), ad left 6 blocks (104s) vs 14 (154s). The first wording also fired on sign-offs and guest thank-yous. Excluding those in the question fixed most of it. At 1× weighting it cut 209s of content | Shipped |
| Remember ad copy across episodes | Not tried | Rejected by the user on scaling grounds |

## Credits rule and summed anchoring (2026-09-28)

The user wants show housekeeping cut: production credits, network IDs, contact
lines (email, hotline) and next-episode teasers. Sign-offs and thanks to guests
stay. What changed:

- **A `credits` rule** in `DefaultRules`, the live config's `[[rules]]` and
  `config.example.toml`.
- **Credits labels, blind.** 8 Opus agents labeled credits only, following
  `fit/credits_prompt.md`, without seeing the existing labels, into
  `data/opus-credits/`. `fit/merge_credits.py -write` merged them into
  `data/opus-labels/` as `"labeler": "opus-credits"`. The result is 34
  segments, 19 of them borderline and so optional when scoring. Two segments
  had timestamps off by a whole number of minutes. Checking each segment's
  `first_line` against the transcript found both, and they were moved; the
  files record this in `time_corrected`. Do that check again whenever agents
  label. An earlier set of 11 hand labels (by the session model, not blind)
  matched Opus on all 11 and was replaced. `fit/label_prompt.md` also defines
  the category now.
- **Anchoring on total cut probability.** Scan anchors on each window's total
  probability across every cut rule (1 − P(content)), not its strongest single
  rule. Adjacent windows join one cluster whatever their top rule, and the
  cluster takes the rule with the most summed probability. Adding a third rule
  without this lost outros that mix a subscribe ask with credits, because the
  vote split. Replayed on stored scan answers, blocks anchored went 71 → 76
  of 95, with no anchors on unlabeled content.
- **End extension floor 0.8 → 0.6**, and both weight files refit on the new
  labels.

`go run ./cmd/redetect -out DIR [-rules ...]` runs full detection on all 44
labeled videos from cached captions (about $0.30, no audio, no YouTube), then
`python3 fit/score_labels.py DIR -v` scores it. Scored against the blind
labels, the current code with three rules against the old code with sponsor
and selfpromo only:

| | Detected | Start within 5s | Start content cut | Start ad left | End within 5s | End ad left |
|---|---|---|---|---|---|---|
| Before | 65/95 | 54/65 | 3 (45s) | 8 (114s) | 50/65 | 15 (200s) |
| After | 70/95 | 60/70 | 4 (52s) | 6 (105s) | 59/70 | 11 (173s) |

The weights weren't refit on the blind labels. Held out by channel, an end
refit scores the same as the current file. The start refit's held-out
estimate moved from 57 to 53 of 67 when the labels changed, which is noise on
this few edges.

## Scan misses, investigated 2026-09-28

The 25 blocks with no cut, by what the stored scan answers said:
- **Flagged but lost after the scan (5).** The end rule's "ambiguous"
  fallback took the first end candidate, 30s inside the anchor. On a read that
  ends the video, that shrank the region below `MinRegion` or to a negative
  length, and it was dropped without a trace. Now an ambiguous end starts at
  the first sentence after the region's start that the end weights score as
  ad and walks forward from there, or keeps the anchor's end without weights.
  Dropped regions are recorded in `Result.Dropped`. Two cases sit right at the
  0.85 threshold (0.83 to 0.86 between runs), so whether they're caught is
  noise.
- **Diluted (about 15).** A 3 to 12s promo fills 4 to 27% of a 30 to 46s
  window, which scores 0.3 to 0.7.
- **The model is unsure (4).** Decoder 4t8RhKbkb70's EY "Decoder Sessions"
  (92s) and Lost Debate fui01uAxLHU's outro (97s) score about 0.45 over whole
  windows. NYT Daily icoL45kZuiM at 1064 scores 0.16.

Lowering `AnchorThreshold` alone doesn't work. Replayed on stored scan answers,
it anchors 81 of 95 blocks at 0.6 and 84 at 0.5, with one false anchor (a 40s
Vergecast intro). But in full runs at 0.6, content cut rose from 70s to 301s at
the start and from 11s to 114s at the end. A weak anchor is a whole window with
a short promo somewhere inside, and the edge rules assume the anchor sits
inside the ad. Klatt -tmGR979z7g's 6s sponsor credit became a 60s cut. Weak
anchors need localizing to the promo's sentences before they're bounded.

Full runs, scored against the blind labels:

| | Detected | Start content cut | End content cut | End ad left |
|---|---|---|---|---|
| Before these fixes | 70/95 | 4 (52s) | 0 | 173s |
| Fallback fix, 0.85 (shipped) | 71/95 | 5 (70s) | 1 (11s) | 173s |
| Fallback fix, 0.6 | 76/95 | 9 (301s) | 4 (114s) | 265s |

**Contained ambiguous fallback.** The first version of the ambiguous-end
walk searched for the first ad-scored sentence anywhere after the region's
start, and on Vergecast UNa7GqyvVug it jumped 130s past the anchor to "You can
read more at theverge.com". It now searches only inside the anchor. That run
(0.85): 72/95 detected, start content cut 4 (52s), end content cut 0,
end ad left 12 (189s).

**Weak leads (`weak_anchor_threshold`, localize.go).** Windows scoring
between the weak threshold and `AnchorThreshold` become leads, not anchors.
Each lead's sentences get the scan's own choice question, and each run of
sentences at `AnchorThreshold` or above becomes a tight anchor. Those aren't
bridged, and they use `LocalizedMinRegion` (3s) instead of `MinRegion` (8s):
real sponsor credits run 3 to 8s, and the first trial found Klatt's 6s Hilton
credit exactly (31.2 to 36.8) but dropped it for being under 8s. At 0.5: 79/95
detected, start within 5s 66/79, end 65/79, start content cut 6 (75s), end
content cut 2 (24s). The seven new blocks are sponsor credits, subscribe asks,
intro promos and a teaser/contact outro, all localized within a few seconds.
The added content cut is mostly Hutchins dF1D619lv7I's contact line taken with
its sign-off (about 18s). There's also one debatable localization: Vergecast
UNa7GqyvVug's producer introducing themselves at the top (0.92 as credits, 6s
cut). "End ad left" rises to 265s because Lost Debate fui01uAxLHU's 97s outro
now counts as detected with a short end, where before it counted as missed.
The extra calls cost about $0.001 per episode. It's off by default; enable it
with `weak_anchor_threshold = 0.5` under `[jev]`.

The new content cut at 0.85 (before the contained fallback) is NYT Daily
icoL45kZuiM. The start weights barely
recognize a credit roll (it peaks at 0.50), so the start stays at the anchor
18s early, and the end weights score "That's it for The Daily" as ad.

## Credits narrowed, and follow-ups (2026-09-28, later)

The user narrowed credits to **credit rolls, network IDs and contact lines**.
Next-episode teasers stay (fuzzy, and often real commentary), and so do
sign-ons, sign-offs and thanks to a guest. That changed:
- **The prompts:** the rule prompt in `DefaultRules`, the live config and
  `config.example.toml`, plus `fit/label_prompt.md` and
  `fit/credits_prompt.md`.
- **The labels:** 19 of Opus's 34 credits segments (teasers, sign-ons and
  sign-offs, one engagement ask, one channel announcement) are marked
  `"excluded"` in `data/opus-credits/`. `merge_credits.py` skips them, which
  leaves 15.

**Feature rows now come from current regions.** `featuredump -renders DIR`
takes anchors from a `redetect` run, not from the cached renders (which
predate the credits rule and weren't bounded by the current code). Both
feature files were rebuilt from a full run at 0.85 with the narrowed rule.

**End question excludes sign-offs.** `winds_down_ad` now says a sign-off
("that's it for today", "see you next week") isn't part of an ad's wind-down.
It used to score NYT Daily's "That's it for The Daily" at 0.75 and cut it.
Now that episode's credits end 0.4s from the label. The end weights were refit
on the new answers; held out they tie the old ones (59/69 within 5s, no
content cut).

**Rejected: the plain start question for credits regions.** It skips the
start weights and asks the start predicate with the credits prompt. Four
credits blocks started 12 to 17s early, and NYT Daily icoL45kZuiM's credits
were missed. Removed.

**Still open: NYT Daily icoL45kZuiM's credits start 17.6s early.** The anchor
window starts inside the last news item. The start weights never rise on the
credit roll (their features describe pitches, not lists of names), so the
start falls back to the anchor's edge. A refit that includes the 7 credits
regions now in the data doesn't change it. A structural fix would be, when the
start has no step, to find the read's first sentence inside the anchor with
the sentence classifier from `localize.go`.

**Side effect of keeping teasers:** Klatt o1NgBJ9r_H0 cuts its 30s "huge
weekend of games tomorrow" teaser along with the subscribe ask after it
(start 30.6s early). The teaser is content now.

Latest full run (0.85, narrowed credits, current weights), against 94
required blocks: 70 detected; start within 5s 58/70, content cut 7 (108s);
end within 5s 58/70, content cut 0.

## Held-out test set (2026-09-28)

`data/test-labels/` holds 19 videos across all 13 channels (10.2 hours, 38
required blocks), labeled blind by 4 Opus agents with `fit/label_prompt.md`,
the narrowed credits category included. They were picked from recent uploads
that appear nowhere in the repo's data, docs or `~/src/jev-test`. **Never fit
or tune on them.** Score a change with:

    go run ./cmd/redetect -labels data/test-labels -out DIR
    python3 fit/score_labels.py DIR -labels data/test-labels -v

Captions were fetched 8s apart with no errors; no audio was downloaded. Run
`python3 fit/check_label_times.py data/test-labels` after any agent labeling.
It caught one segment off by exactly 20 minutes, which was moved; the file
records it in `time_corrected`.

**Results on the test set:**

| | Detected | Start within 5s | Start content cut | End within 5s | End content cut |
|---|---|---|---|---|---|
| 0.85 (installed) | 26/38 | 20/26 | 5 (112s) | 24/26 | 0 |
| Weak leads 0.5, localized end | 33/38 | 27/33 | 5 (97s) | 30/33 | 0 |

Weak leads catch 7 more blocks with less content cut on videos never tuned
on. They're enabled in the live config (`weak_anchor_threshold = 0.5`) as of
2026-09-28. Caveat: the localized-end fix was prompted by a test-set failure
(Klatt tG_DkBvgQII), so the weak-lead test numbers are slightly optimistic.
The 0.85 row is clean. On the training set they give 73 against 68, start content cut 72s
against 93s, and 2 false cuts of 6s each: Vergecast's producer introducing
themselves, and a T3 subscribe ask that Opus folded into a teaser.

**Two changes from this round:**
- **Localized anchors end at their sentence run.** The end fit is built for
  scan windows. Its candidates start a window before the anchor's end, and on
  Klatt tG_DkBvgQII it widened from the Hilton credit across 28s of content
  to the next read.
- **Start fallback by sentence.** When the start has no step and nothing
  inside the anchor clears the fallback floor, the anchor's sentences get the
  scan's question, and the start moves to the first confident one. Neutral on
  the training set. Not yet exercised on NYT Daily icoL45kZuiM: its credits
  window scored 0.75 to 0.84 with the narrowed prompt, under the threshold.
  With weak leads they localize exactly (2000.4 to 2026.6, against 2000 to
  2027).

**Known start-weights weakness:** PrimeTime dCwWXEI1-lA's outro starts 54s
early. The host's banter before the subscribe ask ("you're making a lot of
comparisons, guy who programs in React") scores 0.77 to 0.87 on the start
weights.

**Test misses still left with weak leads (5):** Klatt's 5 to 7s Hilton credits
and plugs (HVJ_PsFX2Kg 30 and 786, tG_DkBvgQII 1135), PrimeTime
ZKxq7lgqpIM's 26s Spotify and subscribe outro, Lost Debate dPW-vr30n7Q's 4s
closing ask, and Hutchins jJTN3fBI618's 6s subscribe ask.

## Short promos, outros and NYT breaks (2026-09-30)

Three problems, all fixed in `internal/detect` with tests. Nothing committed.

**Short promos are obvious, but windows hid them.** Almost every missed promo
was a stock line ("This show is sponsored by Hilton", "Thank you to Incogni for
supporting PBS", "rate, review, subscribe"). Asked about alone, they score near
1. The scan asks what a 30 to 45s window mostly is, so most missed promos sat
in windows scoring 0.15 to 0.46, under the 0.5 weak-lead threshold. Three more
were found and then dropped: their cut came out at 7 to 8s, under `MinRegion`.
Changes:
- `WeakAnchorThreshold` defaults to 0.01, so nearly every sentence outside a
  strong anchor is classified. Jev bills input tokens only, so this costs about
  $0.001 more per video.
- `LocalizedThreshold` (0.95) is how sure a classified sentence must be to cut.
  At 0.85, sign-ons, teasers and shout-outs made 8 false cuts on the training set.
- A scan region bounded under `MinRegion` is localized instead of dropped.
- A localized anchor's start never moves past its own run's end (three NYT
  break bumpers were dropped that way), and moves earlier only on a step of
  `LocalizedMinStep` (0.3) or more. Steps of 0.06 to 0.21 had dragged Hutchins'
  starts 6 to 26s into content to catch 5 to 9s promos.

**Outros.** 9 of the 11 early starts on 2026-09-28's runs were reads ending
the video. The start weights score wrap-up talk (sign-offs, teasers, banter, a
farewell to a guest) like a promo's lead-in. Adding a narration sample from
earlier in the video, since none follows an outro, changed nothing. Now
`OutroTrimFloor` (0.9) moves an outro's start later to the first sentence the
scan question scores at 0.9. It never moves a start earlier. 0.9 was picked on
training: 0.5 left two early starts.

**Uninformative start steps.** When the start fit pulls a scan anchor's start
earlier but no candidate inside the anchor scores `StartFallbackFloor`, the
curve doesn't recognize the read. The existing sentence fallback now takes
over. The start weights score a break bumper at 0.08, and a 0.17 step had
taken an NYT break's start 28s into the news.

**NYT Daily's ads aren't in the YouTube audio.** All 10 breaks in 5 cached
episodes are a music bed, then "We'll be right back", then the show resumes
at once, with no ad copy. The user decided to cut these bumpers as
housekeeping. The credits rule now includes "an announcement of a break (\"we'll
be right back\") with any music around it" in `DefaultRules`,
`config.example.toml`, `fit/label_prompt.md` and `fit/credits_prompt.md`. Labels
changed for the new definition (`"labeler": "break-rule"` or a `relabeled`
note): two bumpers added to IdcjplwJUQ8 (training) and two to D8kGlis5-HE
(test), and 7uYyXVrCpdw's two borderline sponsor marks made required credits.
Those four files were rewritten with one field per line.

`go run ./cmd/redetect` with these defaults and the new credits prompt:

| | Detected | Start within 5s | Start content cut | End content cut | False cuts |
|---|---|---|---|---|---|
| Training, before | 76/96 | 66/76 | 5 (83s) | 1 (11s) | 3 |
| Training, after | 90/96 | 84/90 | 0 | 0 | 2 |
| **Test, before** | 32/42 | 28/32 | 4 (71s) | 0 | 0 |
| **Test, after** | 40/42 | 37/40 | 3 (32s) | 1 (13s) | 0 |

"Before" includes the localized-start fix. The test set's remaining errors:
- Decoder 1n-YwlVoUC4 (−17s) is mostly a guest's own podcast plug, labeled
  as a separate block, so only about 4s of the host thanking the guest is lost.
- Vergecast IAfbYVRgOhQ's end runs 12.8s into a sign-off and banter.
- Hutchins jJTN3fBI618's "send us your stories" feedback request (−8.7s) is
  arguably contact info.

Training still misses Decoder 4t8RhKbkb70's EY segment (92s). It's caught on
some runs and not others.

**The live config overrides the new defaults.** It pins
`weak_anchor_threshold = 0.5` and has the old credits prompt. Remove the
first and update the second when installing this.

## What's still broken, biggest first

1. **Missed promos.** 24 or 25 of 89 labeled blocks get no cut at all. 17 are
   under 15s: subscribe asks, "thanks to Hilton" credits, quick plugs. A 30 to
   45s scan window dilutes them below 0.85. Longer misses include:
   - decoder 4t8RhKbkb70, an EY-branded "Decoder Sessions" segment (92s)
   - spacetime Yid9cO7peXg, a PBS Eons cross-plug running to the end (67s)
   - primetime W9-UG1hvMYs, a Spotify and subscribe outro (26s)
   - vergecast UNa7GqyvVug, a review and Verge subscription outro (23s)
   - t3 jgGyX7MPPVg, a subscribe request (16s)

   Stage 2 only runs where pass 1 found an anchor, so no edge work fixes these.
2. **End edge leaving ad in, partly fixed on 2026-09-28.** The "has the program
   returned" predicate reads a pitch's tail ("Head to zakdoc.com/…", "all the
   promo codes from our partners") as half returned and ends the cut early.
   `data/end-weights.json` now pushes the end later past each sentence a fitted
   ad score puts at 0.8 or above (`Options.EndWeights`, `EndExtendFloor`). It
   never moves the end earlier. The model uses the end feature set, with
   `winds_down_ad` swapped in for `leads_into_ad` and the predicate's own
   answer as an input. Held out by channel over 66 ends:
   - within 5s: 54 vs 50
   - ad left: 12 blocks (162s) vs 16 (233s)
   - content cut: none

   A floor of 0.5 got ad left down to 123s, but it cut Vergecast's production
   credits (10s). Replacing the predicate with the fitted curve lost (337s or
   more of ad left, whatever the thresholds). A per-sentence score can't tell
   content before a short read from content after it, while the predicate is
   cumulative. Live on cached captions:
   - Hutchins dF1D619lv7I's last ad ended at 2477.1 (was 2456.6, Opus 2492)
   - Lost Debate ipvFXLUmgiU ended at 3109 (was about 3104, Opus 3133)

   The extension adds the end features to every region, about $0.005 per
   video. The remaining end misses are mostly outros and self-promos.
   The sentence splitter used to break "zakdoc.com" at the dot, so `lex_url`
   almost never fired. Fixed on 2026-09-28: web addresses stay whole, both
   feature files were rebuilt and both weight files refit. `lex_url` now fires
   on 51 end sentences instead of 1, but both fits give it almost no weight,
   and held-out accuracy didn't change. Other features already caught those
   sentences.
3. **Start edge.** 4 blocks still cut content (110s), and 13 start more than 5s
   late (159s). Spacetime 568-39aOZkw's selfpromo starts 22s late because its
   promo has stray low-scoring captions. Hank MYa4vLcpBqg's outro still starts
   18s early, depending on whether the 4 by 3 game recap counts as promo, which
   is labeled borderline.
4. **NYT Daily's ads aren't in the captions.** The YouTube uploads have
   uncaptioned gaps after "We'll be right back" (10 to 21s). Captions alone
   can't find them.
5. **Start weights refit on 2026-09-27.** `data/weights.json` is now fitted on
   all the Opus labels with the new `leads_into_ad` feature, which carries the
   largest coefficient (+8.0). `anecdote` went from +2.2 to −0.5, so HOTU-style
   story segues now depend on `leads_into_ad`. It's not re-rendered across
   the set yet. Live detection on HOTU sXRPkBKqp-A (the Voyager/AnyDesk segue)
   started at 168.2s against Opus's 168. PrimeTime iuccfEQgIeY started at 94.8s
   against 95, keeping the intro teaser. Remaining start content cuts in the
   replay: klatt o1NgBJ9r_H0 (71s, the old weights cut it too), HOTU
   pf7Yxsrt0Qw's closing lines before its outro (27s), decoder 8hwcDaX3ISg
   (31s), lostdebate ipvFXLUmgiU (13s). Vergecast 2SyX3sudRrY's
   credits-and-hotline outro ahead of its closing ad counts as content by the
   labels, which is a judgment call.

   The live config reads `data/weights.json` from the repo, so the weights
   file and the installed binary's feature set have to change together.
   Weights with a coefficient for a feature the binary doesn't ask about
   score that feature as 0.

## Suggested next steps

- **Staggered pass-1 windows**, aimed at failure 1. Keep windows 30 to 45s but
  label the text in half-size chunks and ask about overlapping pairs, so every
  chunk appears once in the state and the question count roughly doubles (pass
  1 is about 2 of 21 requests per video, so it's cheap). A chunk is promo when
  every pair containing it is flagged. Anchor edges come from the end of the
  last clean pair, not the start of the first flagged one. Staggering can only
  catch a short promo if half a pair of it is enough to flag, which needs
  measuring. A pass-1-only script run on a few videos against the labels would
  answer that without full renders.
- ~~Refit `weights.json` from the Opus labels~~. Done with the new
  `leads_into_ad` feature (see failure 5). A plain refit didn't help. The gain
  came from the new question. To try another feature, add it to `Features`,
  run `go run ./cmd/featuredump` (about $0.11), then `python3
  fit/fit_weights.py`. Write the weights and `go install` together.
- **Confirm with a full re-render** of the labeled set when YouTube audio
  downloads work again. On 2026-09-27 audio hit "Sign in to confirm you're not
  a bot".
- **End edge, next.** Look at the remaining late ends with
  `python3 fit/fit_weights.py -edge end -v`. Space Time's PBS outros are the
  worst.

## Other open threads, not detection

- **Sync output.** The user wants the subscription page link surfaced after
  every sync. Sync already prints `subscription page: <url>` on stderr, but it
  gets lost among the progress lines. The design discussed:
  - end sync with a short stdout summary, with the link last
  - call out "New on your subscription page: <show>" plus a QR code (only when
    output is a terminal) the first time a channel's feed publishes
  - have `channel add` and `init` print the next command to run

  Not built yet.
- **Scheduling.** No launchd job or cron entry runs `shearcast sync`, so new
  episodes only appear when the user runs it.
