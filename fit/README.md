# Detection experiments

Run these commands from the repository root. Python scripts use the standard
library. The saved feature rows let fitting experiments run without model calls.

## Saved data

- `data/weights.json` and `data/end-weights.json` are the shipped fitted models.
- `data/start-features.jsonl` and `data/end-features.jsonl` hold previously paid
  feature answers, candidate text, anchors and source identifiers. End rows also
  carry the recorded return predicate. These are experiment snapshots, not a
  complete replay of every branch in the current detector.
- `data/opus-labels/` contains caption-based labels used for development and
  fitting. Labels include later corrections and cut-policy changes. The labeling
  instructions are in `label_prompt.md`; borderline segments are optional.
- `data/test-labels/` was originally held out. Failures from it have since guided
  fixes, so treat it as regression/development evidence, not an untouched test set.
  The fitter reads only `data/opus-labels/`.

The source audio, full caption cache, live configuration, render cache and scratch
experiment outputs are not part of this repository. Rebuilding features needs
those source inputs, configured channels and an OpenRouter key. `redetect` uses
only cached metadata and captions. Other commands' cache helpers can fetch
missing source data, so acquire any new episode inputs separately before a batch
experiment.

## Fit and inspect without model calls

```sh
python3 fit/fit_weights.py -v
python3 fit/fit_weights.py -edge end -v
```

These commands report shipped weights and video/channel-held-out refits. They do
not modify the weight files unless `-write` is supplied. Use `-write -out
work/candidate.json` to save a candidate without replacing shipped weights. The
output's parent directory must exist. A refit need not reproduce
the shipped file, since labels and exported rows can change between experiments.

The fitter's Python replay is approximate. It omits production start guards, sentence fallback
and outro trimming, and substitutes the anchor start in the ambiguous-end path.
It evaluates already-detected regions, not the complete detection pipeline.
Use it to propose weights, then evaluate their final decisions with the recorded
production replay below. Its held-out edge scores alone are not adoption evidence.

## Run the current detector

`redetect` is strictly cache-only and never invokes yt-dlp. It loads and validates
metadata and captions for every selected episode before starting any model calls
or creating output. Missing, outdated or invalid metadata and missing, malformed
or empty captions fail the entire preflight, with all failed episodes reported.
Cached files are left intact. Collection uses the loaded inputs, so it cannot
fall back to a download if a file disappears after preflight.

Check input readiness without a config, credentials, model calls or output:

```sh
go run ./cmd/redetect -check-cache
# Use the same -labels, -video and -cache selection as the intended collection.
```

Collection makes paid model calls but does not download audio, encode or publish it.
Choose a new scratch output directory for each run. Existing output directories
are rejected so stale successes cannot hide failed episodes. Supply a config containing
the channels named in the selected labels and the intended rules and weights.

```sh
go run ./cmd/redetect -config /path/to/config.toml -out work/baseline/renders
python3 fit/score_labels.py work/baseline/renders -v --json work/baseline/scores.json
```

Both tools accept `-labels data/test-labels` for the existing regression set.
`redetect` accepts `-video` to select comma-separated video IDs or channels. Pass
the same selection to the scorer with `--video`. The scorer reports missing
episodes and fails by default. `--allow-missing` explicitly permits a partial
report; its JSON still marks coverage incomplete.
Both commands reject selectors that match no labeled video or channel.

`redetect -neutral-context` opts into the first context experiment. It preserves
the quoted title, post-anchor subject sample, anchor excerpt and candidate
sentences, but describes those coarse excerpts as uncertain. Questions, weights,
classification prompts and cut guards stay the same. The setting is recorded in
the detector options so the candidate can verify its own replay. Collect fresh
answers on the same development selection and compare coverage and per-episode
errors. Full live runs can also vary in scan/classification evidence; repeat
affected episodes under both settings before attributing a difference to context.
The unresolved `continues_previous` placeholder remains a separate prompt-cleanup
experiment so it does not confound this wording comparison.

Each new redetect record contains the captions, duration, resolved options and
full weight values, exact model questions and states, recorded answers, label-file
SHA-256, requested model, code identity, and per-episode collection usage. Predicate
repeats remain separate. The executable hash identifies dirty or `go run` builds
even when Go's build metadata cannot provide a revision.

## Replay production decisions without model calls

```sh
go run ./cmd/replay -in work/baseline/renders -out work/replayed/renders
python3 fit/score_labels.py work/replayed/renders --json work/replayed/scores.json
```

Use a fresh output directory each time. Replay executes `Detector.Run`, including
scan and localization, both boundary policies, fallback classification, outro
trimming, dropped-region handling, clamping and merging. It uses only saved
answers and never contacts a model or loads live configuration.

By default it first verifies the saved decisions with the recorded settings.
It compares kept and dropped regions, boundary choices and final detector segments.
The summary counts complete episodes, missing evidence, decision mismatches and
file failures separately. Any incomplete run exits nonzero. Failed episodes do
not receive a scorable output record; inspect `replay-summary.json` and the scorer's
coverage before comparing aggregate results.

For a weight experiment:

```sh
python3 fit/fit_weights.py -write -out work/candidate-start.json
go run ./cmd/replay -in work/baseline/renders -out work/candidate/renders \
  -start-weights work/candidate-start.json
python3 fit/score_labels.py work/candidate/renders --json work/candidate/scores.json
```

`-end-weights` supplies end weights. The candidate uses the same recorded feature
answers as the baseline. Its resolved weights are embedded in the output, and
the source record's hash links it to its input. Collection usage is historical;
the replayed detector's model usage is zero. Never sum historical collection usage
across baseline and candidate as though they were separate paid runs.

An altered policy may enter a branch the original run never visited. Missing
questions, changed context, malformed answers or exhausted repeats make that
episode incomplete. Replay does not substitute zeros or make live calls. Collect
new evidence to evaluate it; do not select a candidate using only its easiest
replayable episodes. Existing feature snapshots and older render records lack a
complete recording and cannot be retroactively treated as faithful replays.

Matching uses the full logical collection of state/question pairs for each
`AskAll`, independent of batch partition and scheduling. Original batches remain
in the record. This accommodates the current feature extractor's unordered map
iteration; it does not predict how a new live batching strategy would affect
model answers. Weight accumulation now uses a stable key order to remove that
source of floating-point variation.

For a deliberate code-policy experiment, `-verify=false` permits decisions to
differ from the saved baseline. It still requires evidence for every visited
branch. Save and score the verified baseline first. Prompt/context experiments
need newly collected answers, not replay of old feature values.

For the opt-in conservative ambiguous-end experiment:

```sh
go run ./cmd/replay -in work/baseline/renders -out work/conservative-end/renders \
  -conservative-ambiguous-end
python3 fit/score_labels.py work/conservative-end/renders --json work/conservative-end/scores.json
```

Baseline verification stays enabled. The candidate changes only the ambiguous
end fallback when no in-anchor ad-scored candidate remains at or after the
refined start. With end weights and an extension floor above 0.5 and at most 1,
it preserves the first remaining in-anchor candidate if the cut still meets
`MinRegion` and every remaining in-anchor candidate scores at most
`1 - EndExtendFloor`, 0.4 under default settings. Otherwise the old fallback
stands. Confident ends, ad-seeded walks and localized anchors keep their policy.
The resolved `ConservativeAmbiguousEnd` option is saved in candidate outputs;
it defaults to false. These fitted scores are not calibrated probabilities.
Report additional optional material retained as well as required-material errors.

## Whole-episode scoring

The scorer reports three views when available:

- `regions`: raw bounded regions before merging.
- `detector`: the detector's clamped, merged `Segments`.
- `rendered`: cuts derived from final retained `keep` ranges in real render records.

Detection-only experiments have no rendered view. Replayed records never reuse
old audio keep ranges. Compare the same view and the same episode coverage.

All interval metrics operate on unions of intervals, clamped to the label's
episode duration. They count every second, including errors under five seconds,
entirely missed blocks, false cuts on ad-free episodes, and content removed
between neighboring ads. Required labels must be removed; borderline labels can
be kept or removed without penalty. Content is everything outside the union of
required and borderline labels. Labels therefore need to cover the full episode,
including any cuttable uncaptioned tail, to score rendered output fairly.

Reports include content seconds removed, required unwanted seconds retained,
the worst continuous content cut, and episodes with a content cut over five
seconds. `--large-error-seconds` changes that reporting threshold, not the loss
calculation. Interval block counts refer to contiguous required-label unions.
The older per-edge and three-second-merged block diagnostics remain separately
labeled for comparison with historical fitting runs.

`--json PATH` saves per-episode error intervals, aggregate metrics, channel
coverage, label and record hashes, missing/invalid episodes, and whether labels
changed since collection. Label-change status is `null` when the record has no
collection-time label hash, rather than claiming the labels are unchanged.
`-v` prints per-episode errors. A missing rendered or
detector view is reported as unavailable, never as zero error.

Checks for the replay and scorer:

```sh
go test ./internal/detect ./cmd/replay
python3 -m unittest discover -s fit -p 'test_*.py'
```

## Export new feature answers

This also makes paid model calls. It uses anchors from existing detection records
and rebuilds candidate sentences and prompt states with the current code/config.
End export skips localized regions with no recorded return predicate.

```sh
go run ./cmd/featuredump -config /path/to/config.toml \
  -renders work/experiment/renders -edge start -out work/start-features.jsonl
go run ./cmd/featuredump -config /path/to/config.toml \
  -renders work/experiment/renders -edge end -out work/end-features.jsonl
```

Output is replaced only after a complete, nonempty export unless `-partial` is
requested. Review the experiment before replacing the canonical `data/` files;
the fitter currently reads those fixed paths.

## Check label timestamps

Build the local CLI, then compare quoted first lines with caption timestamps:

```sh
go build -o shearcast ./cmd/shearcast
python3 fit/check_label_times.py data/opus-labels
```

This uses the local CLI's default cache and may fetch missing source data. It
prints cases needing review; it does not adjudicate semantic boundaries or return
a failing exit status for every reported mismatch. Check the output manually.
