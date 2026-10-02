# Renderer resource baseline

Measured on 2026-09-23 before and after the cut-heavy performance fix.
These results cover the final audio cut and encode, using local synthetic media.
They do not establish a minimum RAM or CPU requirement for a full sync.

The current renderer finishes the 90-minute heavy case in about 32 seconds,
down from 585 seconds. The three-hour heavy case now completes in 114 seconds.

## Environment and method

- Apple M4 Pro, 14 logical CPUs, 48 GiB RAM, macOS 15.7.7 arm64.
- Go 1.27.1 and Homebrew FFmpeg 8.0.1.
- Source is independent stereo pink noise at 48 kHz. Generate 60 seconds of
  seeded PCM, loop that PCM, then encode the entire source to AAC at 192 kbps.
- Output is AAC at 128 kbps in M4A, with 0.05-second crossfades and fast-start
  metadata. Other bitrate measurements appear below.
- `cmd/renderbench` calls production `render.Cut` and `render.Probe`. It checks
  that output duration matches retained ranges minus crossfade overlaps within
  0.1 seconds. Episodes run sequentially.
- `scripts/measure-render.py` sums RSS across the driver and all descendants,
  including FFmpeg and ffprobe. It sleeps 50 ms between samples; collecting
  process information adds overhead. This is sampled RSS, not an exact peak.
  Shared pages can appear in more than one process's RSS.
- Working disk is sampled logical bytes for the source, renderer scratch and
  outputs. It excludes the fixture seed, report, filesystem cache and existing
  library. File allocation and exact short-lived disk peaks are not measured.
- Wall time includes initial source probing, rendering and output verification.
  Fixture generation, downloads, detection and boundary snapping are excluded.
  The benchmark calls the renderer directly, so it also excludes pipeline JSON
  sidecars and the extra old output retained during a replacement render.

There are three cut patterns. `none` keeps the whole source. `typical` removes
60 seconds every 15 minutes, starting at 7:30. `heavy` removes 15 seconds every
minute, starting at 0:30. These are controlled patterns, not measured detector
results from real episodes.

## Current cut-heavy results

The final implementation uses up to sixteen seeked source readers and balanced
crossfade joins. All runs use the same fixture recipe, bitrate and cut patterns
as the original baseline. MB means 1,000,000 bytes.

| Source minutes | Cuts | Before | After | Sampled tree RSS MB | Output MB | Output seconds |
| ---: | ---: | --- | --- | ---: | ---: | ---: |
| 90 | 90 | 584.55 s | 32.20 s, then 32.07 s | 241.7 across both | 65.67 each | 4045.50 |
| 180 | 180 | stopped at 180 s, incomplete | 113.59 s | 355.0 | 131.35 | 8091.00 |

The two 90-minute renders ran consecutively in one driver process. Total wall
time including the initial probe was 64.37 seconds. Working disk reached
262.21 MB with both outputs retained. The three-hour run used 393.08 MB of
working disk. All completed outputs passed the duration assertion. The
90-minute improvement is about 18 times faster. There is no completed original
three-hour measurement from which to calculate a speedup.

### Cause and experiments

The original graph fed every trim branch from the entire source, then appended
each retained section to a growing chain of crossfades. Early audio passed
through every later crossfade. Increasing the number of cuts increased both
trim-branch work and repeated crossfade processing.

A reduced case exposed the problem in seconds. An 18-second fixture took
0.16 seconds with three retained sections and 1.89 seconds with ninety.
Limiting filter threads to one made no difference. Replacing crossfades with
hard joins reduced the ninety-section case to 0.29 seconds. This was a
diagnostic experiment; the final renderer still uses 50 ms crossfades.

Balancing the crossfades reduced the full 90-minute heavy case to 139 seconds.
Seeking directly to every retained section reduced it to 30 seconds, but
opening 91 readers raised sampled memory to 795 MB. Each reader holds its own
media index. The final renderer partitions ranges among at most sixteen
readers, limiting that duplication while avoiding full-source work per trim.
An eight-reader trial used less RAM but left the three-hour heavy case too slow.

Each reader decodes at least one second of preroll for AAC overlap state.
Seeks use whole seconds to avoid fractional-sample rounding differences, and
the first reader does not seek to zero, which can skip an M4A priming packet.
Trims still use the original millisecond-rounded boundaries. Balanced joins
retain the original audio order. Sections with overlapping fade regions use
the original sequential join order to preserve their mix.

The regression tests compare decoded output against the original sequential
graph for PCM and AAC at 48 kHz and AAC at 44.1 kHz, including uneven trees,
fractional boundaries and overlapping fades. The AAC comparison fixtures
disable perceptual noise substitution, whose random decoder state changes
after seeking, so byte equality tests alignment and mixing rather than
synthesized noise. Production encoding settings are unchanged.

An opt-in timing test checks both ninety and 180 retained sections against a
three-section baseline. It is separate from normal CI because it has a
wall-clock budget:

```sh
SHEARCAST_RENDER_PERF=1 go test ./internal/render -run '^TestCutManyRanges$' -v -count=1
```

## Original duration and cut matrix

MB below means 1,000,000 bytes. Times are seconds. Each completed row is one run.

| Source minutes | Pattern | Kept ranges | Wall s | Sampled tree RSS MB | Working disk MB | Output MB | Output seconds |
| ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 30 | none | 1 | 16.94 | 44.8 | 72.9 | 29.29 | 1800.00 |
| 30 | typical | 3 | 13.18 | 47.8 | 70.9 | 27.29 | 1679.90 |
| 30 | heavy | 31 | 15.97 | 49.1 | 65.5 | 21.89 | 1348.50 |
| 90 | none | 1 | 50.39 | 76.9 | 218.7 | 87.88 | 5400.00 |
| 90 | typical | 7 | 37.27 | 74.3 | 212.7 | 81.82 | 5039.70 |
| 90 | heavy | 91 | 584.55 | 85.4 | 196.5 | 65.67 | 4045.50 |
| 180 | none | 1 | 100.38 | 119.2 | 437.5 | 175.77 | 10800.00 |
| 180 | typical | 13 | 73.91 | 107.9 | 425.4 | 163.62 | 10079.40 |
| 180 | heavy | 181 | timed out at 180.02 | 82.0 before stop | 262.5 before stop | incomplete | unverified |

All eight completed cases passed the duration check. The 90-minute heavy case
took over fifteen times as long as the typical case. The three-hour heavy case
hit an explicit 180-second measurement timeout. Its memory and disk numbers
are observations before cancellation, not estimates of a completed render.
These are the pre-fix results. The current results and diagnosis appear above.

The first all-duration command hit the calling shell's 900-second timeout after
the six 30/90-minute cases. Its orphaned three-hour render was stopped. The
three-hour matrix above comes from a separate run using the already-generated
three-hour fixture, with a per-case timeout and process-group cleanup.

## Original bitrate and consecutive episodes

Each row renders the same 30-minute source twice in one driver process, keeping
both outputs until the case ends. Source size is 43.62 MB. Both outputs in each
row had the same byte count and passed the duration check.

| Target kbps | Output per episode MB | Two-render wall s | Sampled tree RSS MB | Working disk MB |
| ---: | ---: | ---: | ---: | ---: |
| 64 | 14.83 | 28.27 | 46.5 | 73.3 |
| 96 | 22.06 | 30.49 | 45.7 | 87.8 |
| 128 | 29.29 | 33.73 | 45.8 | 102.2 |

The selected bitrate changed storage as expected. It did not meaningfully
reduce sampled memory for this fixture. These noise-based measurements say
nothing about speech or music quality. Listen to real episodes at 64, 96 and
128 kbps before recommending a lower default.

## Reproduce

Requires Go, Python 3, `ps`, FFmpeg and ffprobe. Use an existing scratch directory
outside the served media directory. The script creates and removes its own
temporary subdirectory. Each report path must be new.

```sh
go build -o /tmp/shearcast-renderbench ./cmd/renderbench

# Run source lengths separately to keep each invocation bounded.
python3 scripts/measure-render.py \
  --binary /tmp/shearcast-renderbench --work-dir /tmp \
  --report /tmp/render-30m.jsonl --durations 1800

python3 scripts/measure-render.py \
  --binary /tmp/shearcast-renderbench --work-dir /tmp \
  --report /tmp/render-90m.jsonl --durations 5400

python3 scripts/measure-render.py \
  --binary /tmp/shearcast-renderbench --work-dir /tmp \
  --report /tmp/render-3h.jsonl --durations 10800 --timeout 180

python3 scripts/measure-render.py \
  --binary /tmp/shearcast-renderbench --work-dir /tmp \
  --report /tmp/render-bitrates.jsonl --durations 1800 \
  --patterns none --bitrates 64,96,128 --repeat 2

# Repeat the final heavy-case measurements.
python3 scripts/measure-render.py \
  --binary /tmp/shearcast-renderbench --work-dir /tmp \
  --report /tmp/render-heavy-90m.jsonl --durations 5400 \
  --patterns heavy --repeat 2 --timeout 180 --max-rss-mib 1024

python3 scripts/measure-render.py \
  --binary /tmp/shearcast-renderbench --work-dir /tmp \
  --report /tmp/render-heavy-3h.jsonl --durations 10800 \
  --patterns heavy --timeout 180 --max-rss-mib 1024

# Use a cached real source without downloading or calling the model.
python3 scripts/measure-render.py \
  --binary /tmp/shearcast-renderbench --work-dir /tmp \
  --report /tmp/render-real.jsonl --input /path/to/cached/audio.m4a
```

The default sampled-RSS abort ceiling is 8192 MiB. `--max-rss-mib` changes it.
This is an observation-triggered cancellation, not an enforced OS memory limit.
The script exits nonzero if a render fails, times out, or exceeds that ceiling.
Each completed attempt still gets a JSONL measurement row.

An early fixture generator looped compressed AAC with stream copy. It introduced
timestamp gaps: a 15-minute, one-cut case measured 839.821 seconds instead of
839.960. Looping PCM before a single AAC encode changed the same case to
839.950 seconds, exactly its new timeline calculation. The tables above use
the corrected fixture. Keep the duration assertion when changing generators.

## Remaining measurements

- Measure one of the user's longest real episodes and representative cut lists.
- Validate a Linux configuration under a real container or cgroup memory limit.
  Docker was installed but its daemon was stopped during this session.
- Measure a complete sync, including captions, detection, boundary snapping,
  publication retries and retained disk across episodes.
- Compare listening quality at the three candidate bitrates.

No Linux minimum, universal render-time estimate, or verified model-price claim
follows from these results.
