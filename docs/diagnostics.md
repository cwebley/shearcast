# Doctor and status

`doctor` checks whether the current setup meets an operation's requirements.
`status` reports saved work and sync outcomes. Both default to offline reads and
can run while sync holds the state lock.

## Check a setup

Use the same user, environment and absolute paths as the scheduled command:

```sh
shearcast doctor -config /srv/shearcast/config.toml \
  -state /srv/shearcast/state.json -cache /srv/shearcast/cache
```

The default `-operation all` reports new-processing and publication checks,
plus serving checks for filesystem publishing. Focus the report with:

| Operation | Checks |
| --- | --- |
| `sync` | Active channel URLs, yt-dlp, FFmpeg, FFprobe, model key, configured weights, state/cache paths and publishing |
| `publish` | FFprobe, recorded completed retry files, state/cache paths and publishing |
| `serve` | Filesystem publishing, readable published feeds, path separation and listen-address syntax |

`-channel SLUG` limits channel checks. Disabled channels do not require source or
model access for new processing. Publication/recovery can still need storage.
Serving needs no R2 credentials, model key, processing executables or readable
state file. R2 setups need no built-in server.

Local checks do not authenticate credentials, resolve source URLs, bind the
server port, invoke the model, run recovery or test publishing writes. They do
not create missing directories or a state lock. Filesystem checks read stored
RSS where present. Missing feeds are normal for new or purged libraries; a
missing feed with active published history fails the check.

Doctor checks the publishing destination against saved history before any
publishing probe. Changing a backend, directory or base URL still requires an
explicit migration, as described in [publishing](publishing.md).

Executable checks confirm that programs can be found on this invocation's PATH.
They do not certify versions or every codec. A readable retry file is not a full
checksum verification; publication performs that verification before using it.
Missing optional weights use the same predicate fallback as processing.

### Network checks

Add `-network` to request live checks:

```sh
shearcast doctor -operation sync -channel example -network
shearcast doctor -operation serve -network
```

For active-channel processing, doctor reads at most one upload from each source
listing and authenticates the model key using OpenRouter's documented
[`GET /api/v1/key`](https://openrouter.ai/docs/api/api-reference/api-keys/get-current-key)
endpoint. It does not fetch captions or source audio, or invoke the decisions
model. Authentication does not prove that the configured model is available or
that an inference request will succeed. Key values and account-response bodies
are not printed.

The diagnostic source listing disables yt-dlp's configuration loading and disk
cache to prevent cookie/page/cache writes. It therefore checks public source
access without user-configured cookies or proxies that ordinary sync may use.

For R2, doctor reads the selected channels' feeds through authenticated storage.
For both backends, it fetches the client-facing RSS URL. If a feed has episodes,
it checks HEAD and a one-byte range request for one sample enclosure. Checks
apply to this machine's network access; they cannot establish phone-side or
podcast-app reachability. Redirects are reported rather than followed, so the
configured URL should be the final feed address.

`-timeout` defaults to `15s` for each network check. No successful read proves
write permissions. Requests may incur the hosting provider's ordinary request
charges, but doctor makes no paid model calls.

### Publishing write probes

Writes require an additional option:

```sh
# Filesystem: publication directory must already exist.
shearcast doctor -operation publish -write-probe

# R2: both options are required.
shearcast doctor -operation publish -network -write-probe
```

A probe writes, reads back and deletes a uniquely named diagnostic file or
object. It never replaces a feed or episode. Local probe files are hidden from
the built-in HTTP handler. R2 probes use `.shearcast-doctor/<random-id>`.
Cleanup is attempted even after cancellation; R2 cleanup has a separate
ten-second timeout. A cleanup failure reports the exact object or file to remove.
Failed prerequisite checks prevent the write probe for that operation.

The probe tests the publication root, not every existing channel directory,
object policy or future disk allocation. State/cache write access and temporary
workspace remain unverified by this probe. The measurements do not establish a
universal RAM minimum or a safe free-disk threshold for a complete sync.

### Results and exit codes

- `PASS` states exactly what was checked.
- `WARN` identifies incomplete evidence or an expected empty-library condition.
- `SKIP` identifies a check that does not apply or has no recorded work to inspect.
- `FAIL` identifies a failed required check for the requested operation.

Doctor exits zero when no required check fails. Warnings do not become a claim
that the entire workflow has been tested. Invalid arguments or failed required
checks return nonzero.

## Inspect saved work

```sh
shearcast status -config /srv/shearcast/config.toml \
  -state /srv/shearcast/state.json
shearcast status -channel example
```

Status takes no writer lock, creates no files and performs no network requests,
artifact repair or state migration. It reads one complete state document while
a writer may continue replacing that document atomically. Config and state are
separate files, so a concurrent channel edit may appear before or after its
associated state change.

The report includes:

- The latest sync invocation's scope, start, finish and result.
- Each channel's latest attempt and last successful sync, including channel-level
  failures such as upload-listing or retention errors.
- Recorded model usage and cost subtotals for the latest invocation and channel
  attempts, with missing observations distinguished from zero model work.
- Recorded publication counts, waiting captions, completed publication retries,
  local-only renders, unfinished processing, errors, deletions and purges.
- Recorded and configured feed addresses, labeled as availability unchecked.
  A known address does not prove a feed exists or is currently reachable.

Published counts exclude removed/pruned episodes but reflect saved history,
not a fresh storage inventory. A failed replacement can still have a previous
publication. Local-only renders are not automatically queued for publication.
Old waiting episodes may have left the latest-N window. Status cannot discover
unseen uploads or promise which unfinished episodes will retry next; use
`sync -dry-run` for current selection.

Status exits zero when it produces the report, including reports containing
failures. Invalid configuration, corrupt/unsupported state and read errors
return nonzero. A missing state file is an empty history, not corruption.

## What sync history means

A channel succeeds when its sync completes without an operational error. This
includes no new uploads and episodes deferred for missing captions. A listing,
processing, publication, retention or recovery error makes that channel fail.
Completed work remains recorded even when later work fails.

Disabled channels are skipped without advancing their last-success time. An
interrupted purge can still fail during disabled-channel recovery. Successful
purge recovery does not claim that the channel's source was synced.

`sync -channel example` updates only that channel's history and records the
invocation's scope. `sync -dry-run` records no attempt. Other commands, such as
manual publication, do not advance sync timestamps.

An invocation starts being recorded after configuration/channel selection and
state opening succeed, before publishing setup. Errors before that point, such
as invalid config or failure to acquire the writer lock, remain in command and
scheduler logs. A publishing-setup error is recorded for the invocation; channel
attempts start when that channel is reached. Later channels that were not reached
retain their previous history. An all-disabled invocation can complete without
any channel gaining a new successful-sync time.

The latest attempt replaces the previous attempt; the last successful completion
is retained separately. This is a compact operational summary, not an audit log.
A missing finish is labeled `completion not recorded`, which does not prove
whether the process is still running. If state cannot be written, the command
fails rather than claiming that its result was saved.

State version 4 added sync history; version 5 adds compact usage accounting.
Older state reads without being rewritten; the next successful mutation
upgrades it. Historical episode timestamps never become invented sync timestamps,
and absent historical usage remains unknown. Use the current build for every
writer sharing the state, because older builds reject the current version 6.

Usage checkpoints retain observations from failed processing independently of
sync success. A failed run may have reported usage; a successful metadata-only
run has zero new model work. An unfinished run shows only its saved observations,
not a promise that all billable work has been accounted for. See
[model pricing and usage reporting](model-pricing.md) for rates, coverage and
the distinction between calculated cost and OpenRouter-reported cost.
