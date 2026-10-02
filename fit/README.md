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
those source inputs, configured channels and an OpenRouter key. The cache helpers
can fetch missing metadata and captions from YouTube.

## Fit and inspect without model calls

```sh
python3 fit/fit_weights.py -v
python3 fit/fit_weights.py -edge end -v
```

These commands report shipped weights and video/channel-held-out refits. They do
not modify the weight files unless `-write` is supplied. A refit need not reproduce
the shipped file, since labels and exported rows can change between experiments.

The replay is approximate. It omits production start guards, sentence fallback
and outro trimming, and substitutes the anchor start in the ambiguous-end path.
It evaluates already-detected regions, not the complete detection pipeline.
See [the methodology plan](../docs/detection-methodology-plan.md) for the proposed
replay and metric improvements.

## Run the current detector

This makes paid model calls but does not download audio, encode or publish it.
Choose a new scratch output directory for each run. Supply a config containing
the channels named in the selected labels and the intended rules and weights.

```sh
go run ./cmd/redetect -config /path/to/config.toml -out work/experiment/renders
python3 fit/score_labels.py work/experiment/renders -v
```

Both tools accept `-labels data/test-labels` for the existing regression set.
`redetect` also accepts `-video` to select comma-separated video IDs or channels.
The score script skips videos with no output record; check the reported video
count and the detector's exit status before comparing runs. Its input is detected
regions, not the final audio cuts after snapping and tail trimming.

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
