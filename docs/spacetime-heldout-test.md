# PBS Space Time held-out test

Run on 2026-09-24. PBS Space Time was added as a channel the fitted start
weights had never seen. The first sync processed five episodes for about
$0.012 of model usage. The results were poor, and four detector problems
explain them. Three are fixed below; the fourth, a missed scan, is still open.

## Ground truth

The comparison uses SponsorBlock marks. Opus labeled all five transcripts
without seeing them, and agreed with SponsorBlock within about a second on every
segment except one. On the `YSwRqNCeP9k` compilation, SponsorBlock's
zero-vote mark sits about 25 seconds late, and the Opus reading matches the
transcript. Transcripts and the listening page are in the ignored
`work/spacetime-review/` directory.

## First-sync results

| Episode | Segment | Truth | Shearcast | Outcome |
|---|---|---|---|---|
| `YSwRqNCeP9k` | community-tab promo | 2:07:39–2:08:02 | 2:06:50–2:08:09 | About 56s of content lost. Content-loss score 0.73. |
| `mxpuV33vpNk` | like/Patreon/merch | 0:20–1:15 | none | Missed. Scan windows scored 0.55 and 0.50. |
| `Yid9cO7peXg` | opening promo | 0:17–1:19 | 0:22–1:17 | A few seconds of promo retained. |
| `Yid9cO7peXg` | PBS Eons plug | 15:38–16:08 | none | Missed. Scan window scored 0.63. |
| `I07RBedXRYA` | opening promo | 0:45–1:56 | 0:50–1:49 | A few seconds of promo retained. |
| `I07RBedXRYA` | Incogni read | 17:08–18:04 | 17:08–17:54 | Promo code retained. |
| `eYViePMalJE` | opening promo | 0:50–1:59 | 0:55–1:54 | A few seconds of promo retained. |
| all | uncaptioned ending | last ~35s | kept | Music. Whisper found no speech. |

## Causes and fixes

**Near-zero closing steps were accepted.** The step fitter accepted any rise.
It cut the Incogni ending on a 0.01 step and the doorbell skit on a 0.16 step.
`fitEnd` in `internal/detect/edge.go` now requires a 0.3 step. It widens past
`EndFitWindow` if no qualifying step appears, and treats a curve below 0.3 as a
read that never returns. It sends any other ambiguous curve to the earliest
candidate. The replay covered all 19 labeled closing curves
(`fit/replay_end_edge.py`, `data/end-curves.jsonl`). It fixed doorbell, Incogni
and the compilation's closing edge, and left the other 16 unchanged. Every
correct edge had a step of at least 0.46, and every incorrect edge had a step of
at most 0.16. The 0.3 threshold was chosen after inspecting these failures, so
new episodes still need to test it.

**Uncaptioned endings were retained.** Every episode kept about 35 seconds of
music after its final caption. Channels now trim audio after the last spoken
caption unless they set `keep_tail`; History of the Universe keeps its music.
The estimated speech end is the final cue start plus text length at eight
characters per second plus 1.5 seconds, capped at the cue end. That cue's
display time is not reliable: Space Time's final caption remains visible for
20 seconds after roughly four seconds of speech.

**Downloads were slow.** Downloads now prefer YouTube's native AAC stream. A
15-minute episode took 2.2 seconds, compared with 13.7 seconds for the previous
Opus-to-AAC conversion.

**Sync was silent.** Sync now reports each stage for each episode, including
download progress in 10% steps.

**The compilation lost content at its opening edge.** The promo began 49
seconds into its scan window, beyond the 30 seconds of opening candidates. The
model correctly scored every candidate low, and the code then fell back to the
scan window start. This remains open. The planned fix extends candidates
forward when the curve never rises.

**Some scans missed promotion entirely.** This also remains open. The mixed
content and promotion windows scored 0.50–0.63, below the 0.85 anchor
threshold. One unconfirmed explanation is that interaction and selfpromo split
the choice probability. On three episodes, the opening boundary landed at the
seam between those rules and retained the like/comment request. Re-running the
scan with every rule's probability recorded would test this explanation.

## Live check after the fixes

An isolated re-render (`work/verify-0924/`) did not modify the published
library.

- `I07RBedXRYA` recorded `EndReason` as "never returns". The cut runs from
  17:07.9 to the end, removing both the promo code and the music. Processing
  took 21 seconds, compared with about 39 seconds before.
- `eYViePMalJE` trimmed 32 seconds. Whisper on the final 12 seconds hears
  "…this singular space-time.", so the final words remain intact.
