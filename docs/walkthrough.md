# Shearcast walkthrough

A high-level tour of the repository: the overall shape, every command and its
options, where the jev questions live, the logistic regression, how silence
splicing works, and how a sync moves data around. Written 2026-09-25; line
numbers drift, so treat them as starting points.

## 1. The big picture

shearcast turns YouTube channels into private podcast feeds with the ad reads
cut out. Each episode goes through five steps:

```
YouTube ──yt-dlp──► metadata + captions (.vtt) + audio (.m4a)
                          │
                 captions ▼
               ┌── detect (jev model via OpenRouter) ──┐
               │  1. scan: which 30s windows are ads?  │
               │  2. bound: where exactly does each    │
               │     ad start and end? (sentence-level)│
               └──────────────┬────────────────────────┘
                              ▼ cut regions (seconds)
               render: snap each edge to real silence (ffmpeg silencedetect)
                       → keep ranges → splice with 50ms crossfades → AAC m4a
                              ▼
               publish: upload audio + rewrite feed.xml (R2 or local folder)
                              ▼
               retention: keep newest N in the feed, delete the rest
```

The key design point: **the captions decide where the ads are, and the audio
decides exactly where to cut.** Caption timestamps are only accurate to about
half a second, so every cut point gets moved to the nearest real pause in the
audio.

Each episode's progress is tracked in `~/.local/state/shearcast/state.json`,
which moves through the stages `pending → waiting_captions → rendered →
published`. An episode removed from the feed is marked either `excluded` (you
removed it) or `pruned` (retention removed it). Every step writes a checkpoint
first, so if anything crashes or fails, the next run picks up where it stopped
without paying for the model calls again. That's why the lifecycle code is so
defensive.

## 2. Layout

| Package | Job |
|---|---|
| `cmd/shearcast/` | The CLI: one file per command, dispatched from `main.go` |
| `internal/youtube` | Wraps yt-dlp: listing, metadata, captions, audio, plus an on-disk cache |
| `internal/transcript` | Parses the caption file into cues, 30s windows and sentences |
| `internal/jev` | HTTP client for the jev model (questions, batching, retries, usage) |
| `internal/detect` | **Ad detection**: scan, boundary finding, features, weights |
| `internal/segment` | Pure interval math: clamp, merge, turn "cut" ranges into "keep" ranges |
| `internal/render` | **ffmpeg**: silence snapping, splicing, measuring duration |
| `internal/chapters` | Shifts YouTube chapter timestamps onto the shorter, edited audio |
| `internal/feed` | Builds the RSS/podcast XML |
| `internal/storage` | Publishing backends: R2 or a local filesystem folder |
| `internal/pipeline` | Ties everything together: `Runner.Run`, `SyncChannel`, `Plan`, retention |
| `internal/state` | Reads and writes `state.json`, and holds the lock so two commands can't run at once |
| `internal/config` | `config.toml`: channels, rules, weights path |

## 3. Commands and their options

Every command takes `-config`, `-state` and `-cache` for the three file
locations. Their defaults are the XDG paths.

**`render <video> -channel X`**: detect ads and cut the audio, but don't
publish. It always re-renders from scratch.

- `-out` sets the output path (default `<cache>/renders/<channel>/<id>/render.m4a`).
- `-no-snap` cuts at the caption-derived times, skipping silence snapping.
- `-snap-window 1.0` sets how many seconds either side of each cut to search for silence.
- `-crossfade 0.05` sets the length of the fade at each join, in seconds.
- `-min-keep 1.0`: a stretch of kept audio shorter than this between two cuts
  gets dropped too, so you don't get a one-second island of speech.

**`publish <video> -channel X`**: upload a finished render and update the feed.

- Without `-audio`, it uses the render in the cache. It fails if that render is
  missing, and it won't quietly re-render.
- `-audio path` imports a file you made yourself. If a `.json` record from a
  render sits beside it, that record is checked and used. If not, shearcast
  just measures the file and hashes it.

**`sync [-channel X]`**: the everyday command (section 7).

- `-dry-run` builds a read-only plan: what would be selected, the estimated
  cost and storage, what would be pruned. Nothing is changed.

**`channel add|list|update|remove`**: edits `config.toml`.

- `add` needs `-url`. `-latest` defaults to 5 and `-keep` to 10.
- `update` only changes the flags you pass.
- Other options: `-rules` (subset of rule IDs), `-bitrate-kbps`, `-no-weights`,
  `-keep-tail`, `-disabled`, and feed metadata (`-name`, `-title`,
  `-description`, `-category`).
- `remove` stops syncing but leaves the feed up. `remove -purge` also deletes
  the feed and its audio.

**`episode list|remove|restore|reprocess -channel X`**:

- `list` shows every episode's stage, errors and model usage.
- `remove <id>` marks the episode `excluded` and deletes it from the feed. Sync
  won't bring it back.
- `restore <id>` un-excludes an episode and re-renders it.
- `reprocess <id>` re-renders a published episode and swaps in the new audio.
  The episode keeps its ID in the feed, so podcast apps don't see it as a new
  episode.

**`serve -listen`**: serves the feeds over HTTP from a local folder, for the
filesystem backend. With R2 you don't need it.

**`doctor`**: checks your setup.

- `-operation sync|publish|serve` chooses which checks to run.
- `-network` also contacts YouTube, OpenRouter and your storage.
- `-write-probe` also tests writing to storage.

**`status`**: shows each channel's last sync result and unfinished work.

**`feeds`**: prints the subscription page URL with a QR code. `-publish`
re-uploads the page, and `-no-qr` skips the QR code.

**`transcript <video>`**: prints the cached captions.

- `-from`/`-to` limit it to a time range.
- `-ad-start`/`-ad-end` mark a region, which is handy for checking a cut by eye.

**`init`**: writes a starter config and `.env`.

Under the hood, `render`, `publish`, `sync` and `episode restore/reprocess` all
go through one function, `Runner.Run` (`internal/pipeline/lifecycle.go:81`).
The action decides the branches:

- **Sync** on an already-published episode only refreshes the title,
  description and chapters. It's cheap.
- **Sync** on a new episode goes through the whole thing: metadata, captions,
  detection, cut, publish.
- **Render** forces a fresh cut and never publishes.
- **Publish** needs an existing render and never calls the model.
- If the captions aren't available yet, the episode is marked
  `waiting_captions` instead of failing, and later syncs retry it.

## 4. Where the jev questions are

All the prompts are in **`internal/detect/detect.go`**. The client that sends
them is `internal/jev/jev.go`. jev takes a block of text (called the "state")
plus a set of questions, and answers each one with probabilities. It has three
question types (`internal/jev/jev.go:45–55`):

- **Choice**: pick one option from a list.
- **Score**: a rating on a scale.
- **Noul**: a true/false probability.

The rules themselves (what to cut) come from config. The defaults are `sponsor`
and `selfpromo` (`internal/config/config.go:100`), each a plain-English
description.

Detection runs in two stages:

1. **Scan** (`internal/detect/detect.go:365`). The transcript is split into
   roughly 30s windows. For each one: "Window [W012] of this video transcript
   is best described as…", with the options `content`, `sponsor` or
   `selfpromo`. Windows go 24 per request, and each request also includes one
   neighboring window either side for context. A window at 0.85 or higher for
   an ad rule becomes an "anchor". Anchors of the same rule within 90s of each
   other are merged into one cluster.

2. **Bound** (`internal/detect/detect.go:514`). For each cluster, it finds the
   exact start and end sentences.
   - **The candidates** for the start are the sentences from 180s before the
     anchor through up to 60s into it, stopping at the anchor's end. Reaching
     inside matters because an anchor starts wherever its scan window starts,
     which can be half a window before the read does. The end uses the
     sentences from 30s inside the anchor to 180s after it.
   - **The start question** (`detect.go:677`) is asked of every candidate:
     "Does the advertisement, *counting any segue written to introduce it*,
     begin at or before sentence [L017]?" The true answers run no, no, no, yes,
     yes, so the probabilities form a step.
   - **The end question** (`detect.go:688`): "Has the program returned to its
     own subject by sentence [R004]?"
   - **Every question is asked 3 times** and the answers averaged. Then
     `changepoint` (`internal/detect/edge.go:33`) finds the best-fitting step,
     which becomes the boundary. The start fit only looks at candidates within
     75s before the anchor, because over a longer lead-in any off-subject
     stretch can win it. If no step rises, the start falls to the first
     sentence inside the anchor scoring 0.5 or more.
   - **The explanation** in `segueBrief` (around `detect.go:60`) is the long
     text telling the model what a segue is. It's the most important piece of
     prompt engineering in the project.
   - **A final check** (`detect.go:742`) asks whether the cut would remove any
     of the video's real content. The answer is recorded but doesn't change
     the cut.

The comment at the top of `detect.go` explains why it asks a yes/no question of
every sentence. The two earlier approaches, rating each chunk and asking which
sentence starts the ad, both disagreed with human-marked boundaries.

## 5. Where the logistic regression is

Only the **start edge** uses it, and only when a weights file is configured
(currently `data/weights.json`):

- **Features** (`internal/detect/features.go:32`): instead of the single start
  question, each candidate sentence gets 14 narrow true/false questions, such
  as `names_brand`, `on_topic`, `anecdote`, `benefit_claim`, `product_category`
  and `continues_previous` (which compares the sentence with the one before
  it). There are also 3 free keyword features: `lex_thanks`, `lex_cta` and
  `lex_url`.
- **Scoring** (`internal/detect/weights.go:47`): score = sigmoid(bias + Σ
  weight × feature). That's logistic regression at prediction time.
  `featureCurve` in `detect.go` scores every candidate, and the same step is
  fitted to the scores.
- **The trained model** is `data/weights.json`: 329 labeled sentences from 12
  videos on 2 channels (History of the Universe and PrimeTime). The biggest
  weights are `on_topic` at −4.15, `anecdote` at +2.23 and `product_category`
  at +1.95.
- **The training code isn't in this repo.** It lives in
  `~/src/jev-test/fit/export_weights.py`, a small hand-written gradient-descent
  logistic regression with L2 regularization, trained on
  `~/src/jev-test/data/features.jsonl`. Retraining means going back to
  jev-test.

Why the start edge only: the comment at `detect.go:113` records the test where
they fitted on one channel and scored another. The features were off by 2.7s
against 17.2s for the single question on start edges, but 38.4s against 1.0s on
end edges. So each edge uses whichever method won. The end edge has its own
tuning, `fitEnd` (`internal/detect/edge.go:140`), fitted against labeled end
points by the scripts in this repo's `fit/`. Those scripts set the end-edge
thresholds; they aren't logistic regression.

## 6. How the silence splicing works

This happens in `RenderEpisode` (`internal/pipeline/pipeline.go:55`), with the
ffmpeg work in `internal/render/render.go`:

1. **Tail trim.** Everything after the last spoken caption (end cards, outro
   music) becomes one more cut, unless the channel has `keep_tail` set
   (`transcript.SpeechEnd`).
2. **Snap** (`internal/render/render.go:61`). For each cut edge, it runs
   `ffmpeg silencedetect` over ±1s of audio. Silence means below −30 dB for at
   least 0.2s. The 0.2s floor exists so a breath in the middle of a sentence
   doesn't count. The edge moves to the **midpoint of the nearest silence**,
   which leaves quiet on both sides of the join. If there's no silence in the
   window, the edge stays where it was. An edge at the very end of the file
   isn't snapped.
3. **Interval math** (`internal/segment/segment.go`, `Prepare`). It clamps the
   cuts to the file's length, merges cuts less than 2s apart, and turns them
   into keep ranges, dropping any kept piece under `min-keep`.
4. **Cut** (`internal/render/render.go:173`). One ffmpeg command:
   - `atrim` slices out each keep range, using at most 16 file readers.
   - `acrossfade` joins them with 50ms fades, combined as a balanced tree.
   - The result is encoded to AAC at the channel's bitrate (128k).
   - Source chapters are stripped, since they'd point at the wrong times after
     cutting.
5. **Verify and install.** ffprobe measures the output, it gets a SHA-256 hash,
   and a `render.m4a.json` record is written beside it. The record holds every
   setting, the detection results and the keep ranges, and it's installed along
   with the audio in one step that survives a crash. Chapters are later shifted
   using those keep ranges.

## 7. How a sync flows

`sync` → `Runner.SyncChannel` (`internal/pipeline/sync.go`), for each channel:

1. **Recover.** Tidy up anything a crashed run left behind: half-installed
   renders, uploads not yet confirmed in the feed, removals still in progress.
2. **Retention.** Apply `keep` to the current feed.
3. **Publish retries first.** Episodes already rendered but not published
   (`ReadyToPublish`) are published now. No model calls needed.
4. **List uploads.** yt-dlp lists the channel with `--flat-playlist`.
   Members-only, private and upcoming videos are skipped, and the newest
   `latest` of the rest are selected.
5. **`Run(Sync)` on each selected video:**
   - Already published: refresh the title, description and chapters, then stop.
     Free.
   - New: fetch metadata, then check the admission rule: *would this video even
     make the newest `keep` by publish date?* If not, it's skipped before any
     spending. That's why old videos never get processed.
   - Captions: fetch them, or mark `waiting_captions` if there aren't any yet.
   - Detect with jev, which is the paid part. A usage checkpoint is written to
     state before the first request.
   - Download the audio, snap, cut, and write the render record. The downloaded
     source audio is deleted afterwards.
   - Publish: upload `slug/<id>.m4a`, rewrite `slug/feed.xml` (items ordered by
     source date, same-day ties broken by YouTube's upload order), and journal
     the publication so a lost response can be reconciled later.
   - Retention again: whatever falls beyond `keep` is marked `pruned`, and its
     audio is deleted from storage.
6. **After all channels**, the subscription page and OPML file are
   republished.

Data locations:

- **Cache** (`~/Library/Caches/shearcast`): yt-dlp metadata, captions, source
  audio (temporary) and renders with their records.
- **State** (`~/.local/state/shearcast/state.json`): episode stages, errors,
  usage and sync history.
- **Storage** (R2): `feed.xml`, the `.m4a` files, chapter JSON and the
  `subscribe/` page.

## Suggested reading order

1. `internal/detect/detect.go`: the top comment, then `bound()`
2. `internal/detect/edge.go`
3. `RenderEpisode` in `internal/pipeline/pipeline.go`
4. `internal/render/render.go`
5. `SyncChannel` in `internal/pipeline/sync.go`
6. `Runner.Run` in `internal/pipeline/lifecycle.go`, the most complex part
