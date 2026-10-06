# shearcast

Cuts unwanted segments out of YouTube channels and republishes each one as a
podcast feed, hosted on your own disk or Cloudflare R2.

Built for listening while asleep or commuting, where a sponsor read at full
volume (or a segment you'd rather skip entirely) is the one failure that
matters.

## Why a decision model instead of a fixed vocabulary

Crowd-sourced ad-skip databases work from a fixed vocabulary of categories
someone else defined. When a video is covered, their boundaries are
excellent, because a human dragged them. When it isn't, there's nothing, and
there's no way to ask for anything outside that list.

The rules are prose you write yourself. Detection runs against
[Jev](https://openrouter.ai), a decision-model API. It scans caption windows for
confident anchors, localizes weaker leads sentence by sentence, then refines the
edges using fitted sentence features and boundary predicates. Quoting the ad
alongside its candidate lead-in helps identify a segue that initially sounds like
real subject matter. Detection uses text, so it cannot judge audio loudness from
captions alone. See `internal/detect` for the implementation and
[the detection methodology plan](docs/detection-methodology-plan.md) for proposed
accuracy and evaluation work.

## Setup

```sh
brew install yt-dlp ffmpeg
go install ./cmd/shearcast   # installs to ~/go/bin; rerun after pulling changes
shearcast init               # writes a starter config.toml and .env
shearcast doctor             # shows the paths in use and checks the setup
```

`init` writes `~/.config/shearcast/config.toml` and a private `.env` beside it,
and never overwrites either. Replace the example channels, or start from an
empty config with `shearcast channel add`. State lives in
`~/.local/state/shearcast/state.json`, and the cache in
`~/Library/Caches/shearcast` on macOS. `$XDG_CONFIG_HOME` and `$XDG_STATE_HOME`
move the first two, `SHEARCAST_CONFIG` names a different config file, and the
`-config` and `-state` flags override everything. Relative paths inside the
config resolve beside it.

Fitted start-edge weights are optional. `weights.json` beside the config is
used when present. Otherwise the opening edge uses the plain predicate, and
renders say so. Point `[jev] weights` at a fitted file to use it from elsewhere.
Fitted end-edge weights (`end-weights.json`, found beside the start weights, or
`[jev] end_weights`) are optional too. They only move a closing edge later, past
a pitch's tail that the predicate reads as the program returning.

`[jev] conservative_ambiguous_end` controls a closing-edge rule that is on by
default. Sometimes the model can't tell where a promo ends and its last window
runs into closing talk. If the end weights score all of that talk as program,
Shearcast keeps it rather than cutting through to the window's end. The rule
does nothing without end weights, and it applies only when the shorter cut
still meets `min_region`. On the development set it changed one cut out of 97,
and on six further videos it changed none. Set it to `false` for the older
behavior. The render record saves the setting under `detection_options`.
Changing it doesn't touch episodes that are already rendered. To apply it to
one, run `shearcast episode reprocess <video-id> -channel <slug>`, or `render`
and then `publish`. Either reruns detection, so it costs model tokens.

When working on Shearcast itself, run `go run ./cmd/shearcast` or build with
`go build -o shearcast ./cmd/shearcast` and use `./shearcast`, so the installed
copy keeps working while you change the code.

Shearcast uses OpenRouter's `typesafe/jev-1.13` decision model at
`POST /api/alpha/decisions`. Set `OPENROUTER_API_KEY` in the environment or
in `.env` next to your config. The older `OPENROUTER_KEY` name remains a
fallback; the canonical name takes precedence.

Upgrading an existing config? Remove `provider` and `base_url` from `[jev]`.
These settings now produce a migration error. OpenRouter is the only supported
endpoint. The model's vendor namespace is still `typesafe`.

OpenRouter's published Jev rate was verified on 2026-09-23: $0.042 per million
input tokens, with free output tokens. Reports distinguish calculated token
cost from OpenRouter's returned cost and flag missing observations. See
[model pricing and usage reporting](docs/model-pricing.md) for sources and scope.

Choose filesystem publishing for a NAS, desktop or VPS serving from its own
disk. R2 remains the default for existing configurations; `.env.example` lists
its credentials. Both choices keep RSS and audio together.

For the built-in server on a home LAN, add these top-level tables to your config:

```toml
[publishing]
backend = "filesystem"
directory = "/srv/shearcast/public"
base_url = "http://192.168.1.20:8080"

[serve]
listen = "0.0.0.0:8080"
```

Replace the address with your server's LAN address and choose a writable
directory separate from the cache, state and config. Run `shearcast serve`
alongside scheduled `shearcast sync` runs. Subscribe to
`http://192.168.1.20:8080/<slug>/feed.xml` in a client that fetches feeds directly,
such as AntennaPod. A real-device LAN smoke test passed with generated speech;
see the [test record](docs/antennapod-lan-test.md) for its scope and results.
See [publishing and serving](docs/publishing.md) for complete commands, R2 and
existing-web-server setups, scheduler examples and the phone smoke test.

## Usage

```sh
# detect, snap to silence, and cut an episode -- writes locally, uploads nothing
shearcast render <video-url-or-id> -channel hotu

# upload an already-rendered episode and update its channel's feed
shearcast publish <video-url-or-id> -channel hotu

# retry pending work and process selected new uploads
shearcast sync

# explicitly refresh existing episode metadata and channel artwork too
shearcast sync -refresh-metadata

# same, but scoped to one channel (useful for a per-channel cron schedule)
shearcast sync -channel gabfest

# inspect selection and estimate model cost/storage before processing
shearcast sync -channel hotu -dry-run

# keep local feeds and audio available between sync runs
shearcast serve

# check local setup, or inspect saved sync results while a sync is running
shearcast doctor
shearcast status

# print the subscription page address as a QR code, plus every feed URL
shearcast feeds

# manage a channel and its published library
shearcast channel list
shearcast episode list -channel hotu

# inspect cached captions, optionally with a region marked
shearcast transcript <video-url-or-id> -ad-start 120 -ad-end 180
```

Run `sync` periodically with the OS scheduler, such as `launchd` on macOS or a
`systemd` timer on Linux. Each run recovers interrupted work, retries unfinished
publication, checks the newest `latest` uploads and exits. The default
selection is five. Up to four channels run concurrently; episodes within each
channel stay sequential so its feed has one writer. The defaults allow two
yt-dlp operations and one AAC encode at a time across the run. `[jev].parallel`
also caps model requests across all workers. Tune the worker and resource limits
with `-jobs`, `-youtube-jobs` and `-encode-jobs`; all must be positive. Use
`-jobs 1` for sequential channel processing.

Progress reports elapsed time for discovery, metadata, captions, detection,
snapping, encoding and publication, plus each channel and the whole run. Stage
times include waiting for shared resources. Unchanged episodes are counted in
the channel summary instead of printing repeated zero-usage lines.

New renders fetch and validate captions first, then download source audio and
check that FFprobe reports a positive duration. Jev detection starts only after
both inputs are ready. Missing captions wait without downloading audio; caption,
audio-download and source-probe failures make no model requests.

For selected episodes already published, normal sync reuses saved metadata and
artwork. A changed title in the upload listing triggers a metadata refresh for
that episode. `sync -refresh-metadata` refreshes every selected published
episode's source title, description, artwork and supported chapters, plus show
artwork. There is no automatic refresh interval. These updates preserve GUIDs,
enclosures and edited durations and fetch no captions or source audio for
published episodes. Pending work still retries normally with either invocation.
Local feed-setting edits apply on ordinary sync without a source refresh.

Each channel's feed is fetched once per invocation and reused by recovery,
retention and episode updates. Unchanged feeds, published chapter revisions,
and subscription documents are not uploaded again. Failed feed writes invalidate
the cached snapshot so recovery checks the actual remote outcome.
New publications also include source chapters adjusted to the edited audio.
See [source chapters](docs/source-chapters.md) for format support and refresh behavior.

### Subscribing on a phone

Every `sync` also publishes `subscribe/index.html` and `subscribe/feeds.opml`
beside the feeds, listing each enabled channel that has a published feed. Run
`shearcast feeds` and scan its QR code with your phone, then bookmark the page.
Each show has a Subscribe button that opens AntennaPod's subscribe screen, and
the OPML download adds them all at once through Add podcast, Import OPML file.
`shearcast feeds -publish` rewrites the page immediately, such as after
removing a channel. The page reveals every channel's feed address to anyone
who has its URL. The slug `subscribe` is reserved.

### Selection and retention

```toml
[[channels]]
name = "Example channel"
slug = "example"
url = "https://www.youtube.com/@example"
latest = 5
keep = 10
```

`latest` limits source discovery. If ten uploads appeared between runs and
`latest = 5`, only the newest five are inspected. Existing episodes receive
metadata updates; missing episodes are processed. Waiting captions retry only
while selected. Verified publication retries can finish outside that window.

Uploads shearcast cannot access (members-only, private, premium-only,
sign-in required, upcoming premieres and live streams in progress) do not
count toward `latest`: with `latest = 1` and a members-only newest upload, the
newest public upload is selected instead. Sync prints one `skipped (reason)`
line per such upload and does not treat it as a failure; `sync -dry-run`
lists them too. The upload listing flags most of these. When it does not,
yt-dlp's refusal to load the video's metadata is caught the same way, though
that video still used up a slot. Nothing is recorded for a skipped upload, so
it is picked up normally if it becomes public while still in the window.
Explicit `render` or `publish` of such a video still fails.

`keep` limits published episodes and their managed local audio copies. Its
default is ten. Both settings must be positive integers, and `keep` must be at
least `latest`. There is no unlimited mode. For a thirty-episode selection, set
both values to at least thirty.

Retention uses source publication dates and timestamps, with upload-list order
resolving same-day metadata. It runs on sync and publication, including after
metadata refresh. A newly discovered episode that would fall outside retention
is skipped before captions, detection or audio download. Pruned history remains
in state, so enlarging `keep` does not automatically restore old episodes.
Unknown publication dates fail before paid processing and refresh on retry.
For an explicit episode outside the selected window, ambiguous same-day ordering
can require increasing `keep` before publication.
An overfull legacy feed with day-only dates may need a larger `latest` window
to establish the order at the retention cutoff. Sync reports unresolved ordering
and leaves those entries intact until it can choose by source chronology.

Existing `watch_limit` values still work as an alias for `latest`. Use one name,
not both. Rename the field when editing by hand; `channel update -latest N`
does this automatically. A legacy value above ten also needs an explicit
`keep` at least that large. The first sync with these settings can prune an
existing feed down to `keep`; inspect `sync -dry-run` first.

### Channel and episode management

```sh
shearcast channel add example -url https://www.youtube.com/@example \
  -name "Example channel" -latest 5 -keep 10
shearcast channel update example -keep 20 -bitrate-kbps 96
shearcast channel update example -title "Example, sheared" -rules sponsor,selfpromo

# stop automatic synchronization, leaving the published feed available
shearcast channel remove example
shearcast channel update example -disabled=false

# stop synchronization and delete the feed and managed audio
shearcast channel remove example -purge

# persist an exclusion, remove the feed entry and delete managed audio
shearcast episode remove <video-id> -channel example
shearcast episode list -channel example

# explicitly restore an excluded or pruned episode
shearcast episode restore <video-id> -channel example

# rerun detection and rendering with current settings, then replace publication
shearcast episode reprocess <video-id> -channel example
```

Channel removal writes `disabled = true`, keeping the channel's configuration
available for later management or resumption. `-purge` also removes its feed,
published audio and channel-managed renders, including local-only renders.
It keeps small records and exclusions. Resuming the channel allows new uploads;
previously purged episodes need explicit restoration. An interrupted purge
resumes on the next sync, including completion of the channel-disable step.

Episode removal works even before an episode has been processed. Sync skips
excluded and pruned IDs without fetching metadata or making model calls.
`render` and `publish` also respect exclusions. Removal intent is saved before
the feed changes, then media is deleted after the feed entry is gone. Failed
deletions remain visible in `episode list` and retry during recovery.

Restore downloads source audio again and runs detection, rendering and
publication with current settings. Reprocess does the same for an active
episode. Both retain the original GUID, may spend model tokens, and must fit
within `keep`; increase it first for an older restoration. Once a render is
complete, publication retries reuse it. An unfinished restore or reprocess
retries automatically only inside the latest-N window. Outside that window,
use `episode reprocess` to retry unfinished processing or `publish` for a
completed render. For a failed restore that still shows `excluded` or `pruned`,
retry `episode restore`.

All these commands accept `-config`, `-state` and `-cache`. Use the same paths
as sync. Channel edits preserve rules, detector tuning and unknown TOML values,
but rewrite formatting and omit comments. Slugs identify storage paths and
cannot be renamed by `channel update`. Imports passed to `publish -audio` and
custom `render -out` files remain caller-owned; retention deletes only the
channel's default managed render and published object. Small metadata, caption
and processing records remain. Removing server files cannot recall audio
already downloaded by a phone.

### Pre-sync planning

`sync -dry-run` reads the finite upload listing, source metadata, feed and local
processing records. R2 requires publishing credentials; filesystem publishing
requires access to the directory. Neither needs a model key. It does not fetch captions or audio, run recovery, change
episode state, or write published objects. It acquires the shared state lock.

The plan lists selected, new, existing, waiting, excluded and pruned episodes,
plus verified publication retries outside the selection. It shows known total
source duration, duration needing processing, current published bytes,
estimated uploads, final retained bytes and retention removals. Upload estimates
include retries that will be published before newer episodes later prune them.
Missing feed entries and invalid artifacts are shown as blocked work. Pending
recovery is called out because it can change the plan.

For new audio, storage uses source duration times the effective AAC bitrate,
plus a 3% container allowance. Cuts usually reduce the result; the encoder
target and allowance are estimates, not limits. Verified outputs use their
actual bytes. R2 and the managed local cache each retain a copy. Filesystem
publishing keeps one managed audio copy after successful publication, with
processing records in the private cache. Temporary source downloads, publication
copies and replacement renders need additional disk, whose size is
not known from this plan. Local-only and caller-owned files are not included
in the published-library totals.

Model estimates use input tokens per source second from the same channel,
model and rule set. The range spans half the lowest observed rate to twice
the highest, multiplied by planned processing duration. This is a rough
planning range, not a statistical confidence interval. Without matching
history, usable durations or verified model pricing, the plan reports
insufficient data. It assumes waiting episodes receive captions and new
processing succeeds. Jev's input rate of $0.042 per million tokens was verified
on 2026-09-23. New records with incomplete token usage are excluded from samples.
Estimates are not provider billing and exclude additional retries, credit-purchase
fees, taxes, hosting, storage and bandwidth.

### Model usage and cost

Processing commands print reported input/output tokens, calculated cost and
OpenRouter-reported cost when present. `sync` includes per-episode figures and
a run total even when some episodes fail. Publication retries and metadata-only
work add zero new model usage.

`episode list -channel SLUG` shows each episode's latest processing attempt and
cumulative recorded usage since tracking began. `status` shows the latest saved
sync totals. Failed processing attempts retain reported usage independently of
whether audio was rendered. Missing responses leave partial totals, not proven
zero cost. Unfinished attempts and unresolved requests remain visible.

Calculations keep their original rate and verification date. Legacy artifacts
retain their saved calculated amounts labeled `legacy/unverified`; absent
historical usage remains unknown. See [the accounting contract](docs/model-pricing.md)
for checkpoint behavior, history coverage and pricing sources.

### Episode state and retries

`render`, `publish` and `sync` share `state.json`. Scheduled and manual commands
share it automatically when both use the default path; otherwise pass the same
absolute `-state` path to each. An OS-level lock prevents overlapping
writers on Linux and macOS, and releases automatically if a process exits.

State records distinguish pending work, waiting captions, finished renders and
published episodes. Failures retain the last checkpoint and an error message.
Publication history is separate from replacement-render progress, so a failed
manual rerender cannot make an existing episode appear new to sync.

Missing English captions defer a new episode without blocking other episodes.
Sync retries while it remains in the selected upload window. Network errors,
rate limits and malformed caption files are reported as failures rather than
treated as clean episodes.

Once the rendered file and its checksummed record are durable, publication can
retry without YouTube, captions or a model key. A durable installation record
also recovers interruptions between installing audio and installing its sidecar.
Recovery completes local-only render checkpoints and source cleanup too, without
queuing publication. Attempt identifiers distinguish new output from an older
render left intact after a failed replacement.
Before that checkpoint, interrupted processing may need to run again. Sync
retries pending publication before listing uploads; a later listing failure is
still reported. `publish` can retry one episode directly.
If a date-only retry needs upload-list order to fit within retention, sync retries
its admission after fetching that listing, without repeating detection.

Metadata and captions live under `<cache>/<id>/`. Rendered audio and records
start under `<cache>/renders/<channel>/<id>/`. After filesystem publication,
managed audio lives at `<publishing.directory>/<channel>/<id>.m4a` and the
processing record stays in the private cache. The default cache root on macOS is
`~/Library/Caches/shearcast/`; override it with `-cache`. Source audio downloads
use `<cache>/sources/<channel>/<id>/audio.m4a`, keeping concurrent channels'
working files separate while metadata and captions stay shared. Older cached
downloads are reused when available. Source audio is temporary
working data and is removed after the render checkpoint is saved, before
publication. A later explicit rerender may download the source again. Finished
audio stays locally until retention or manual removal; small records survive
removal. Restart recovery reclaims abandoned downloads and render staging files.

A local-only `render` does not queue publication outside the selected upload
window. For an already published episode, replace it explicitly with `render`
followed by `publish`.

Existing `processed` history migrates when state is next saved. Existing feeds
keep their episode IDs. Older per-video renders remain on disk; import one
explicitly when needed:

```sh
shearcast publish <video-id> -channel hotu -audio /path/to/old/render.m4a
```

Mutating commands write state version 6, preserving prior checkpoints and
published history. It records usage and sync outcomes as well as the publishing destination
and separate paths for served audio and private processing records. Earlier
builds reject this version.
Version 6 saves published chapter timelines and pending artifact revisions.
Use the current build for every command sharing the state file. Changing the
backend, destination directory or base URL requires an explicit library
migration; changing config alone is rejected. To try local hosting alongside
an existing R2 library, use separate config, state and cache paths.

### Diagnostics

```sh
shearcast doctor                          # local checks for applicable operations
shearcast doctor -operation serve         # filesystem hosting, no processing dependencies
shearcast doctor -operation publish       # completed retries, no model/source requirement
shearcast doctor -operation sync -network # also check authentication, source and feed access
shearcast doctor -operation publish -network -write-probe
shearcast status
shearcast status -channel example
```

Doctor reports configuration, executables, credentials and publishing checks by
operation. Its default is local and read-only. Network checks require `-network`;
temporary publishing writes also require `-write-probe`. It never invokes the
model or publishes an episode. A local write probe needs an existing publication
directory. Missing feeds are normal before first publication. Warnings identify
what remains unverified; failed required checks return a nonzero exit code.

Status reads the last saved state offline, including while a writer holds the
lock. It reports each channel's latest attempt, last successful sync, waiting
episodes, errors, unfinished work and feed addresses. It creates no files and
runs no recovery. Status returns zero when it produces the report, even if that
report contains recorded failures; unreadable or invalid state/config returns
nonzero.

A sync can succeed with episodes waiting for captions. Operational errors make
the affected channel fail, while disabled channels are skipped without advancing
their successful-sync time. A scoped run updates only its selected channel.
Older state has no recorded sync times. An unfinished attempt means completion
was not recorded, not that a process is necessarily running. Use `sync -dry-run`
for fresh source selection and retry eligibility.

See [diagnostics](docs/diagnostics.md) for check scope, examples and limitations.

### Output bitrate

Every new render produces AAC in an M4A container, including episodes where
successful detection finds nothing to remove. Failed detection or an empty
caption scan does not produce a clean episode.

```toml
[audio]
bitrate_kbps = 128

[[channels]]
name = "Example channel"
slug = "example"
url = "https://www.youtube.com/@example"
bitrate_kbps = 96
```

The default is 128 kbps. Supported targets are integers from 32 through 320
kbps. A channel inherits `[audio]` when its bitrate is omitted or zero.
This is an encoder target; actual file size varies with the audio and container
overhead. The renderer keeps the source sample rate and channel layout.

Settings apply when you explicitly run `render` or when `sync` processes a new
episode. Changing settings does not invalidate published episode history.
To replace an existing episode, run `episode reprocess`, or `render` and then
`publish` for its channel. Replacements keep the GUID but use a new enclosure
URL when the audio changes, so a failed feed update leaves the old audio available.
Use `-out episode.m4a` and `publish -audio episode.m4a` for an explicit output
path. Default render paths are channel-specific, so channels can use different
settings for the same source video.

Each successful render writes `<audio-path>.json` beside the audio. This record
contains the resolved bitrate, cutting and snapping settings, model and detector
settings, source metadata, detection result and per-episode usage, final retained
ranges, probed output duration, file size and SHA-256 checksum. Sync verifies
this artifact before resuming publication. Changes to current rules or bitrate
do not invalidate an already finished render.

See [renderer measurements](docs/render-measurements.md) for local resource
results and commands to repeat them. Listening comparisons at 64, 96 and 128
kbps are still needed before recommending a lower default.

## Feeds

Each configured channel becomes its own podcast feed
(`<slug>/feed.xml` in the publishing directory or R2 bucket). Each channel has
a separate feed. Feed and audio URLs have no authentication. Anyone who can
reach the server and knows the URL can access them. Public deployments are
unlisted, publicly accessible feeds. LAN-only hosting requires a client that
fetches feeds on-device; Pocket Casts' server-side fetcher cannot reach your LAN.

## Layout

```
cmd/shearcast/        CLI: init, render, publish, sync, serve, doctor, status, channel, episode, transcript
cmd/renderbench/      local-only renderer measurement driver
cmd/redetect/         rerun detection without audio rendering
cmd/replay/           replay the production detector against recorded model evidence
cmd/featuredump/      export model feature answers for offline fitting
fit/                 fitting and scoring tools; see fit/README.md
scripts/             process-tree and working-disk measurement
internal/segment/     cut arithmetic                          (pure, heavily tested)
internal/transcript/  WebVTT parsing and windowing
internal/jev/         System One client, parallel batching
internal/detect/      scan, sentence localization and boundary refinement
internal/render/      silence-snapping and ffmpeg cutting
internal/chapters/    source chapter retiming and description rewriting
internal/youtube/     yt-dlp wrapper, on-disk cache
internal/config/      TOML config (rules, channels, Jev tuning)
internal/feed/        podcast RSS generation
internal/storage/     R2/filesystem publication and read-only HTTP serving
internal/state/       durable episode checkpoints and process locking
internal/usage/       reported usage and historical rate calculations
internal/pipeline/    lifecycle, library management, sync and planning
internal/fileutil/    durable record replacement and directory creation
```

## Status

Working end-to-end: caption fetch and parse, the scan/localize/bound detection
pipeline, silence-snapped ffmpeg cutting, R2 upload, per-channel RSS feed
generation, resumable publication, caption waiting, metadata refresh and
new-episode discovery via each channel's public uploads listing. Filesystem
publication and built-in HTTP serving pass automated lifecycle and download
checks. A real-device AntennaPod LAN smoke test also passed with generated speech.

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

- Detection batches transcript windows rather than sending a whole transcript
  in one request.
- `render`'s snap window, crossfade and min-keep defaults are reasonable
  starting points, not independently tuned per channel.
