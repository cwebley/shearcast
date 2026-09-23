# shearcast

Cuts unwanted segments out of YouTube channels and republishes each one as a
private podcast feed.

Built for listening while asleep or commuting, where a sponsor read at full
volume (or a segment you'd rather skip entirely) is the one failure that
matters.

## Why a decision model instead of a fixed vocabulary

Crowd-sourced ad-skip databases work from a fixed vocabulary of categories
someone else defined. When a video is covered, their boundaries are
excellent, because a human dragged them. When it isn't, there's nothing, and
there's no way to ask for anything outside that list.

Here the rules are prose you write yourself, so "the part where the music
sting gets loud" is a rule if you want it to be. Detection runs against
[Jev](https://openrouter.ai), a decision-model API, in four narrowing passes
(scan the whole transcript for anchors, then bound each edge with a monotone
predicate over candidate sentences) so a segue that opens with real subject
matter and only reveals itself as an ad later still gets caught. See
`internal/detect` for the detail.

## Setup

```sh
brew install yt-dlp ffmpeg
cp config.example.toml config.toml   # edit channels for your own sources
cp .env.example .env                  # OPENROUTER_API_KEY + R2 credentials
go build ./cmd/shearcast
```

Jev is served by OpenRouter as `typesafe/jev-1.13` at the same list price as
TypeSafe direct ($0.042/M input, output free). OpenRouter serves it at
`POST /api/alpha/decisions` with the same `{model, state, questions}` wire
format as TypeSafe's own `/v1/systemone`, so switching providers is a config
change (`[jev] provider = "typesafe"`).

Audio and feeds are stored in Cloudflare R2 (S3-compatible, no egress fees).
`.env.example` lists what to fill in from the R2 dashboard.

## Usage

```sh
# detect, snap to silence, and cut an episode -- writes locally, uploads nothing
shearcast render <video-url-or-id> -channel hotu

# upload an already-rendered episode and update its channel's feed
shearcast publish <video-url-or-id> -channel hotu

# check every configured channel for new uploads, render + publish each
shearcast sync

# same, but scoped to one channel (useful for a per-channel cron schedule)
shearcast sync -channel gabfest

# inspect cached captions, optionally with a region marked
shearcast transcript <video-url-or-id> -ad-start 120 -ad-end 180
```

`sync` is meant to be invoked periodically by the OS scheduler (a `launchd`
job on macOS, a `systemd` timer elsewhere), not run as a standing process: it
does one pass over each channel's public uploads listing, skips anything
already recorded in `state.json`, and exits. There's no webhook listener and
nothing that needs to stay online between runs.

Captions, metadata and rendered audio are cached in
`~/Library/Caches/shearcast/<id>/` (override with `-cache`), so each video is
only ever fetched from YouTube once.

## Feeds

Each configured channel becomes its own podcast feed
(`<slug>/feed.xml` in the R2 bucket), subscribed to independently in whatever
podcast app you use — there's no merged "sleep" or "commute" feed; that
distinction lives in which show you open, not in the feed structure. Feeds
have no authentication: the R2 object URL is unguessable but not access
controlled, which is the same posture as most personal/private podcast feeds.

## Layout

```
cmd/shearcast/        CLI: render, publish, sync, transcript
internal/segment/     cut arithmetic                          (pure, heavily tested)
internal/transcript/  WebVTT parsing and windowing
internal/jev/         System One client, parallel batching
internal/detect/      the four-pass ad-region detector
internal/render/      silence-snapping and ffmpeg cutting
internal/youtube/     yt-dlp wrapper, on-disk cache
internal/config/      TOML config (rules, channels, Jev tuning)
internal/feed/        podcast RSS generation
internal/storage/     R2 (S3-compatible) upload/fetch
internal/state/       processed-episode bookkeeping for sync
internal/pipeline/    wires the above into render + publish operations
```

## Status

Working end-to-end: caption fetch and parse, the four-pass detection
pipeline, silence-snapped ffmpeg cutting, R2 upload, per-channel RSS feed
generation, and `sync`'s new-episode discovery via each channel's public
uploads listing.

Not built: non-YouTube (RSS-sourced) podcast ingestion is untested in this
repo -- the `no_weights` config option exists because that content shape was
investigated, but whisper-transcription ingestion for feeds with no existing
captions is out of scope here. See the `shearcast` project's sibling research
repo for that investigation and the Jev accuracy findings behind the tuning
defaults in `internal/detect`.

## Known limitations

- **Rules beyond ad-like content aren't fully generic yet.** `config.Rule`'s
  shape (`id`, `prompt`) plugs any prose into the scan stage cleanly, but the
  boundary-refinement predicate (`internal/detect`'s `segueBrief`,
  `startPredicate`/`endPredicate`) always frames the question in terms of "the
  advertisement" and "a segue", regardless of which rule matched. A rule
  describing something else (a chatter segment, an off-topic tangent) would
  scan correctly but get boundary-refined with mismatched language. See the
  comment on `segueBrief` in `internal/detect/detect.go`.
- **Per-rule `threshold` is unused.** Every rule's config carries a
  `threshold`, but nothing in `internal/detect` reads it -- the only
  threshold that gates anchor formation is the single global
  `Jev.AnchorThreshold`, applied identically to every rule. See the comment
  on `config.Rule` in `internal/config/config.go`.

## Notes

- Context is 32k on OpenRouter versus 64k documented for TypeSafe direct,
  which is why every detection pass batches rather than sending a whole
  transcript at once.
- `render`'s snap window, crossfade and min-keep defaults are reasonable
  starting points, not independently tuned per channel.
