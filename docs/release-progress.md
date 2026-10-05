# First-release progress

Updated 2026-09-24. The first four implementation phases are complete. Local
publishing and serving passed automated checks and a real-device AntennaPod
LAN smoke test using generated speech.
Source chapters also passed AntennaPod chapter-skip, metadata-update and offline
checks by user report; public HTTPS JSON chapter checks remain.
A [real-source episode](real-source-chapters-test.md) subsequently passed
server-side rendering/publication checks using cached original captions and an
HTTP/3 audio-download workaround. Phone chapter navigation passed by user report,
but detection removed genuine product-review content before the sponsor. The
saved sponsor end boundary also needs review; this is not a clean cut-accuracy pass.

## Implemented

- OpenRouter-only setup. Removed direct-provider endpoint, credential support
  and public provider/base-URL settings. Kept the injected HTTP endpoint for
  tests and `OPENROUTER_KEY` as a legacy fallback.
- Global `[audio].bitrate_kbps` and per-channel overrides, defaulting to 128.
  Supported AAC targets are 32 through 320 kbps. Encoding always uses AAC/M4A.
- Successful no-cut detection now renders a clean episode at the chosen
  bitrate. Empty caption scans and malformed scan answers stop rendering.
- Render records capture effective settings, source metadata, detector result,
  episode usage, final kept ranges, duration, file size and SHA-256 checksum.
  A durable installation journal recovers either side of the audio rename.
  Attempt identifiers prevent recovery from mistaking an old render for the
  output of a failed replacement. Local-only recovery also checkpoints output
  and cleans up source audio without queuing publication.
- A local-only renderer driver, process-tree measurement script, and documented
  synthetic results in [render-measurements.md](render-measurements.md).
- Fixed cut-heavy rendering by bounding seeked readers and balancing crossfade
  joins. The 90-minute heavy case fell from 585 seconds to 32 seconds; the
  three-hour heavy case now completes in 114 seconds. Decoded-audio regression
  tests cover the original mix, AAC seek priming and fractional sample boundaries.
- Versioned JSON episode state preserves legacy processed history and records
  pending, waiting-caption, rendered and published stages. Last errors retain
  a retryable checkpoint; publication history survives failed manual rerenders.
- Shared lifecycle behavior for render, publish and sync. Completed publication
  retries need no model key or source access. Manual publish records history.
- Process-level writer locks on Linux/macOS, channel-specific output paths,
  verified source cleanup and restart reclamation of abandoned staging files.
- Published metadata refresh preserves GUIDs, enclosures and edited durations.
  Missing captions wait within the selection window; other caption failures
  stay distinct. New episodes and pending publication remain sequential.
- Bounded `latest` selection and separate `keep` retention, defaulting to five
  and ten. Positive limits and `keep >= latest` are required. `watch_limit`
  remains a migration alias. Source timestamps and upload order distinguish
  same-day releases; retention never sorts episodes by processing time or ID.
- Durable exclusions and pruning records in state version 2. Removal updates
  the feed before deleting media; recovery completes interrupted deletions and
  channel purges. Processing history and small records survive pruning.
- `channel add/list/update/remove`, with `remove -purge` distinct from stopping
  sync. Config edits preserve rule, tuning and unknown values, including inline
  channel tables. They rewrite formatting and remove TOML comments.
- `episode list/remove/restore/reprocess`. Explicit restoration and reprocessing
  share the existing render/publication lifecycle and preserve GUIDs. Retention
  admission runs before paid work; removed episodes cannot reappear through
  metadata refresh, publication retry or ordinary render/publish commands.
- Read-only `sync -dry-run` lists selection and retries, source durations,
  current/upload/retained bytes and a qualified historical model-cost range.
  With no matching history it reports insufficient data. Temporary source and
  render workspace are additional disk needs. Pricing verification is recorded below.
- Filesystem publishing alongside R2, with explicit destination directory and
  client-facing base URL. Relative publishing directories resolve beside the
  config. All publishing, removal, purge and planning commands select the same
  adapter; local publication requires no R2 credentials.
- Atomic local audio/feed replacement, confined paths and idempotent deletion.
  Successful filesystem publication checkpoints the served audio before removing
  its managed private copy. Processing records stay in the cache. Recovery
  finishes interrupted cleanup; caller-owned imports remain untouched.
- Read-only `serve` runs alongside scheduled sync without holding state locks.
  It serves RSS and M4A with GET, HEAD, conditional requests and byte ranges,
  including a configured URL prefix. Directory listings, symlinks, staging
  files and processing records are not served. Shutdown handles SIGTERM.
- State version 3 records separate audio/record paths and a structured publishing
  destination. Changed destinations and filesystem adoption of legacy published
  R2 history are rejected, including when the public domain stays the same.
- [Publishing documentation](publishing.md) covers the built-in LAN server,
  existing web servers, VPS/R2 arrangements, systemd and launchd scheduling, and
  the AntennaPod phone checklist. The [LAN test record](antennapod-lan-test.md)
  records the completed phone checks and observed episode-history behavior.
- `doctor` checks setup by operation, locally by default. Explicit network checks
  authenticate the OpenRouter key without inference, read bounded source listings
  and check RSS plus sample audio HEAD/range access. A separate opt-in publishing
  probe writes, reads and deletes unique diagnostic data.
- `status` reads an offline state snapshot without a writer lock or filesystem
  mutations. It reports per-channel sync outcomes, waiting/failures/pending work
  and recorded/configured feed addresses. State version 4 saves invocation scope
  and per-channel attempts separately from last-success times. Waiting captions
  permit success; disabled channels are skipped. Older sync times remain unknown.
  See [diagnostics](diagnostics.md) for exact check and exit-code semantics.
- Verified Jev's current published OpenRouter rate, $0.042 per million input
  tokens with free output, using public metadata and endpoint-specific docs.
  [Pricing research and accounting](model-pricing.md) records the sources and
  billing uncertainties. No inference call was needed.
- Request-level usage checkpoints retain reported input/output tokens and
  optional OpenRouter cost independently of downstream processing success.
  Calculated cost saves its rate and provenance. Missing observations and
  unresolved requests remain visible, including after retries and restarts.
- State version 5 adds compact latest-attempt and cumulative episode accounting,
  plus latest channel/invocation sync totals. Render records use version 2;
  older records retain legacy/unverified labels. Processing commands report
  current usage, `episode list` shows saved episode figures, and `status` shows
  saved sync totals. Publication retries and metadata refresh add no model work.
- Source chapters now map through the published audio's final retained ranges
  and crossfades. Source metadata remains private and unadjusted. Recognized
  description chapter lines are replaced; removed topics and duplicate starts
  are omitted. HTTPS feeds link revisioned JSON chapters; HTTP LAN feeds carry
  inline Podlove chapters. [Source chapters](source-chapters.md) documents the
  new-publications-only scope, imports, cache refresh and client limits.
- State version 6 separates the published timeline from pending renders and
  journals chapter/audio revisions. Changed audio gets a new enclosure URL,
  preserving GUIDs and keeping old audio/chapters usable after a failed feed
  write. Recovery, removal, retention and purge clean up unreferenced revisions.
- New renders fetch usable captions before downloading source audio, then require
  a positive probed source duration before Jev detection. Source acquisition and
  probe failures record zero new model requests while preserving prior usage.

Existing feed artwork, HTML entity decoding, config-driven feed metadata
refresh and example title changes were preserved.

Existing configs must remove `[jev].provider` and `[jev].base_url`. Personal
credentials, configuration and state were not edited or migrated during this
implementation sessions. The application migrates legacy state on its next
successful state write. Sync refreshes published metadata; changing bitrate or
rules does not trigger automatic reprocessing. Explicit `render` followed by
`publish` replaces an episode. Older per-video outputs can be imported with
`publish -audio`; their files remain untouched during migration.

Set `keep` deliberately before the next live sync, which can prune an existing
library to the default ten. Legacy `watch_limit` above ten requires an explicit
`keep` at least that large. `latest` and `watch_limit` cannot both be set.
Caller-owned imported audio and custom output files are not deleted by retention.
State version 6 requires the current build for every command sharing the state.
Changing a library's backend, destination directory or base URL is not an
automatic migration. Use separate config, state and cache to try LAN hosting
alongside an existing R2 library. Personal files have not been migrated.

## Next implementation steps

1. Real-source and public-feed chapter checks, then release checks,
   licensing, packaging and installation documentation.
2. Address the real-source test's download transport failure, hidden retry
   progress and long retry delays before unattended cold-source validation.
3. Use the saved real-source case to correct sponsor-boundary content loss and
   investigate the retained sponsor tail before accepting detector accuracy.

The user's 2026-09-24 follow-up deferred downloader transport changes and detector
tuning. The subsequently approved ordering change is implemented: usable captions
first, downloaded and duration-probed audio second, paid detection third.
Direct control of caption/audio fetching remains an open design question; no
yt-dlp replacement or HTTP/3 dependency was selected. The [real-source case](real-source-chapters-test.md)
now records the effective detector settings, candidate boundaries, verification
score, investigation leads and a preserved local replay archive for later work.

Spending limits are outside the approved pricing and cost-reporting slice.

Resolve the remaining measurement follow-ups alongside these steps. The
cut-heavy slowdown is fixed; the current data does not support a RAM minimum.
The current rate is verified in the pricing research above. No live model calls, source downloads,
R2 publishing, commits or release operations were needed for these implementation
phases. Tests use local HTTP adapters, temporary state and generated audio.

## Verification

`go test -race ./...`, `go vet ./...`, `go build ./...` and `git diff --check`
passed. Real-FFmpeg tests cover AAC bitrates, clean episode encoding and
crossfade timing. Pipeline tests cover malformed scan responses, effective
settings in sidecars and preservation of an existing render after probe failure.
Lifecycle tests cover publication failure after audio upload, cancellation,
reopening state, recovery on either side of audio installation, source cleanup,
published-history migration, manual publishing, failed manual replacements,
metadata-only updates, corrupt artifacts, waiting captions, abandoned working
files, process exclusion and symlinked output aliases.
Library acceptance tests also cover five-upload selection with three existing
and two new episodes, repeated syncs, failure on either remote-deletion step,
legacy feed-only pruning, disabled-channel purge recovery, restore and caption
retry, explicit reprocessing, same-day source ordering, missing-date refresh,
config value preservation and non-mutating historical cost/storage planning.
The standards/spec review found config inline-table preservation, stale missing
dates, purge-intent ordering, same-day chronology and retry upload-estimation
issues; each was corrected and regression-tested.
Follow-up checks cover excluded restores with incomplete metadata, same-day
publication retries deferred until listing order is available, interrupted or
legacy overfull feeds, and timestamp-preserving retry estimates. Ambiguous
retention waits for source order rather than permanently pruning a guess.
Completed publication retries also finish retention without fetching source
metadata again; successful resolution clears the temporary retention error.
Final standards and phase-3 spec review follow-ups have no unresolved findings.

Phase 4: `go test -race ./...`, `go vet ./...`, `go build ./...` and
`git diff --check` passed. After the final URL-validation fix, storage race tests,
storage vet, the full build and whitespace checks passed again. Acceptance tests
exercise both filesystem publication and the actual R2 adapter against a local
S3-protocol HTTP fixture. Local lifecycle tests cover failed feed publication,
retry after restart without source/model access, one retained managed copy,
private records, replacement failure, explicit imports, cleanup recovery,
removal and purge. HTTP tests cover RSS, full audio, HEAD, byte ranges, invalid
ranges and rejection of non-public files. Filesystem tests cover incomplete
writes, repeat deletion, symlinks, staging reclamation and malformed base URLs.

A compiled CLI smoke test used generated audio, temporary config/state/storage,
and a loopback HTTP listener. It verified non-mutating dry runs, exact downloaded
bytes, RSS enclosures, HEAD/range/conditional requests, destination and path
separation checks, removal/purge alongside the server, and clean SIGTERM exit.
Its legacy version-2/same-base-URL case reproduced the review defect before the
fix and then verified rejection with byte-identical legacy state.

Parallel standards/spec reviews found a legacy R2 binding bypass and base-URL
query/encoding edge cases. All were corrected and rechecked. The standards
review's destination-representation concern was resolved with a structured
record. Final follow-ups have no unresolved code findings. The subsequent phone
smoke test passed subscription, playback, seeking, download and airplane-mode
playback, discovery of a second episode without duplicates, and continued use
after restarting the server. Server-side removal left the episode in AntennaPod's
history, as recorded in [the test results](antennapod-lan-test.md). App and Android
versions were not recorded; the user declined version collection. The generated
fixture did not test artwork. No live model call, source download, R2 publication,
commit or release operation was performed. The temporary LAN server was stopped
after testing.
The ignored repository-root `shearcast` executable was rebuilt and its top-level
and `serve` help checked. Personal config, credentials and state remain untouched.

Diagnostics: `go test -race ./...`, `go vet ./...`, `go build ./...` and
`git diff --check` passed after the review fixes. Tests cover snapshots while a
writer holds the lock, missing/legacy/corrupt state without writes, durable
waiting-success and listing-failure history, disabled channels, partially failed
and channel-scoped invocations, dry-run history preservation, operation-specific
dependencies, destination-change rejection, RSS/HEAD/range checks and probe
cleanup after cancellation. Network tests use local or injected HTTP fixtures.

Standards review identified duplicated destination construction, now shared by
doctor and publishing. Its help-exit concern was withdrawn after confirming
`flag.ExitOnError` exits zero for help. Spec review identified inherited yt-dlp
config/cache writes, acceptance of channel-less RSS and probes running after
failed publishing checks. Each was corrected and regression-tested. Both
follow-up reviews reported no remaining findings. OpenRouter's current-key
endpoint was checked against its official documentation; no live credential
check, model call, source download or R2 operation was performed.
The ignored repository-root `shearcast` executable was rebuilt with diagnostics;
its `doctor -h` and `status -h` commands both exited successfully.

Cost reporting: `go test -race ./... -timeout 180s`, `go vet ./...`,
`go build ./...` and `git diff --check` passed. After review fixes, race tests
for usage, Jev, state, pipeline and CLI passed, followed by vet, build and
whitespace checks. Tests cover reported versus calculated cost, missing versus
zero values, unknown/free rates, rate changes, missing token fields, malformed
answers, HTTP errors, lost responses, concurrent batch failure, stale lifecycle
snapshots, idempotent checkpoints, interrupted attempts and state migration.

Pipeline tests verify usage after failed detection, audio cancellation,
publication failure and failed replacement, zero new work on publication retry,
mixed successful/failed channel totals, scoped metadata-only sync and exclusion
of incomplete estimation samples. CLI tests cover historical price preservation,
partial/zero/unknown labels and non-mutating status during a writer.

Standards review found prior pricing entries leaking into interval summaries.
Calculations now count observations, preserving genuine zero-token/free results
while removing entries with no observations in the interval. Spec review found
accounting-checkpoint failures did not stop later episodes/channels. A typed
checkpoint failure now ends that invocation's processing. Both reviewers also
identified a concurrent-error ordering case where errgroup's first API error
hid a later checkpoint failure. Batch completion now joins the sticky accounting
error after all dispatched requests settle. Each defect reproduced in a focused
test before its fix. Final follow-up reviews report no remaining findings.

Price verification used public metadata and documentation only. No live model
call, source download, publishing operation, personal state migration or commit
was performed for this slice. Spending limits remain out of scope.
The ignored repository-root executable was rebuilt; `status -h`, `sync -h`
and `episode list -h` exited successfully.

Source chapters: `go test -race ./... -timeout 180s` passed. After the final
publication-marker fix, affected race suites for chapters, feed, state, pipeline,
storage, YouTube and CLI passed, followed by `go vet ./...`, `go build ./...`
and `git diff --check`. Tests cover the 15:00-to-12:59.95 example, partially and
wholly removed topics, canonical duplicate starts, original description content,
source EOF differences, repeated refresh and preserved metadata ownership.
Generated AAC fixtures at 44.1 and 48 kHz check audible marker placement and
actual end duration.

Both publishing adapters pass chapter refresh/removal tests over HTTP and HTTPS
configurations. Lifecycle tests cover failed chapter/feed writes, byte-identical
imports with missing provenance, a lost response after a successful feed write,
published-versus-pending timelines, retention, interrupted purge and cleanup.
Public serving tests accept only managed chapter/audio revisions and reject
private JSON records. The local S3 fixture now decodes SDK chunked request bodies.

Standards review identified duplicated cleanup and snapshot adoption; both now
have shared implementations. Spec review identified duration mismatches blocking
chapterless publication, distinct starts lost through display rounding, missing
source-time labels on imports, and ambiguous same-byte publication recovery.
Each spec defect reproduced in a regression test before its fix. RSS now carries
an opaque publication marker so recovery can identify the completed attempt.
Final standards and spec follow-ups report no remaining findings.

The user chose JSON for HTTPS, inline Podlove for HTTP LAN, and new publications
only. Existing publications receive chapters through explicit republishing or
reprocessing. No library was wiped. Phone-side chapter skips, metadata update and offline use
subsequently passed in the [generated LAN demo](antennapod-chapters-test.md).
Public HTTPS chapter checks remain. The temporary demo server was stopped after
the offline test. No live model
call, source download, remote publication, personal state migration or commit
was performed for this slice.
The repository-root executable was rebuilt with chapters. `publish -h`, `sync -h`
and `serve -h` exited successfully.

Source acquisition ordering: `go test -race ./... -timeout 180s`, `go vet ./...`,
`go build ./...` and `git diff --check` passed. Focused tests cover missing and
unusable captions, successful download before detection, download failure,
corrupt audio and nonpositive duration. Source failures preserve published
objects and prior usage across restart, record known zero model requests, and
permit a successful retry. Existing-render probe failure preserves both audio
and its record without calling the model. Detection-failure tests now provide
source audio and verify that they actually reach the injected model endpoint.
Source cleanup still follows a durable render. Scoped standards and spec reviews
reported no findings. No paid model calls were made for this change.
