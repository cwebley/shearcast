# Source chapters

New publications include source-provided chapter titles and timestamps adjusted
to the edited audio. Chapters come from yt-dlp's full video metadata. Shearcast
does not generate titles or make extra model calls for this feature. When the
source has no chapters, it publishes the source description without adding any.
yt-dlp's `<Untitled Chapter N>` placeholders and empty titles are omitted.

This feature applies to new publications and explicit republishes. Metadata-only
sync does not backfill older published entries. Use explicit reprocessing or
publication to replace one when needed. No library reset is required.

Normal sync keeps published chapter metadata as it is, unless a changed source
title triggers an episode refresh. Run `shearcast sync -refresh-metadata`, with
`-channel SLUG` if desired, to fetch chapter corrections explicitly. Repeated
refreshes reuse chapter revisions already referenced by the live feed when
their content hash is unchanged.

## Timing and descriptions

Chapter starts use the final retained audio ranges after snapping, merging and
minimum-keep decisions. The renderer probes the source audio's actual duration
before choosing the final range end, rather than assuming the metadata duration
matches the file. Mapping subtracts preceding crossfade overlaps and uses the
same millisecond rounding as the renderer.

A source chapter at 15:00, after removing 10:00 through 12:00 with a 0.05-second
join, becomes 779.95 seconds. The readable timestamp is `00:13:00`. Structured
chapters keep millisecond precision. Distinct starts remain distinct even when
their readable timestamps round to the same second.

- A wholly removed topic is omitted.
- A topic whose beginning was cut starts at its first surviving audio.
- Duplicate mapped starts keep the first heading at that start.
- Missing chapter ends use the next later start or the saved source duration.
  Invalid intervals and empty or placeholder titles are omitted.

Shearcast recognizes description lines whose timestamp and complete title match
a source chapter. It accepts a timestamp before or after the title, including
common bullet and separator forms. It replaces those lines with the adjusted
list and removes recognized headings for topics that were cut. Other prose,
links and unrelated timestamps stay in place. If no source chapter lines match,
it appends a readable chapter list. It does not try to rewrite arbitrary HTML
or guess what an unrelated timestamp means.

Original descriptions and chapter intervals remain in private source metadata.
Every refresh starts from that source metadata, so timestamps are not repeatedly
shortened. Older metadata caches that discarded chapters receive one full
metadata refresh when next used. This does not fetch captions or audio.

## Formats and publication

HTTPS feeds link Podcasting 2.0 JSON through `<podcast:chapters>`. Each document
contains version `1.2.0`, chapter titles and numeric `startTime` values. The
managed path is `<slug>/<video-id>.<sha256>.chapters.json`, served as
`application/json+chapters`.

HTTP feeds, including the built-in LAN setup, contain inline Podlove Simple
Chapters with `HH:MM:SS.mmm` starts. The Podcasting 2.0 namespace requires HTTPS
resource URLs, so HTTP feeds do not emit an HTTP JSON link. Both formats use
the same retimed list as the readable description.

Shearcast does not embed chapters in the M4A file. Rendering also strips any
source container chapters, whose timestamps would refer to unedited audio.
Chapter corrections need only metadata publication.

State version 6 keeps the published timeline separate from a pending replacement
render. Updating bitrate, detector settings, or a local render cannot change
the chapter map of audio already referenced by RSS. A changed audio replacement
uses `<video-id>.<sha256>.m4a`; the episode GUID stays the same. RSS switches to
the new audio and chapter revision together. Upload failures leave retryable
work, without repeating detection. Removal, retention and purge remove chapter
revisions along with managed audio after removing their feed references.
An opaque `shearcast:publication` RSS marker distinguishes publication attempts
for recovery, including byte-identical audio imported with different provenance.
It contains no source timeline, processing settings or usage data.

Old RSS cached by a client can refer to an obsolete revision after cleanup.
Downloaded copies also keep their original contents. Server-side publication
cannot force a podcast app to replace its local episode data.

## Missing provenance

An explicit audio import needs a checksummed Shearcast render record to provide
a trustworthy edit timeline. Without one, Shearcast publishes no navigable
source chapters. If source chapters or timestamped description lines exist,
it labels those timestamps as referring to the original video. It never infers
cuts from the edited duration or calls the model to repair missing history.

A malformed or inconsistent saved timeline cannot produce adjusted chapters.
Publication or refresh with source chapters reports the error. Chapterless
publication can proceed without a usable chapter map. The original metadata
and completed audio remain available for explicit repair or reprocessing.

## Verification and app limits

Automated tests check mapping, actual AAC duration and audible marker placement
at 44.1 and 48 kHz, description preservation, XML/JSON round trips, repeated
refresh, pending replacements, imports, and artifact cleanup. Publishing tests
use temporary filesystem storage and the actual R2 adapter against a local
S3-protocol fixture. They make no live model calls or source downloads.

The [dated format research](source-chapters-research.md) records primary sources.
Pocket Casts documents JSON and Podlove support on Android and iOS. Apple now
documents JSON chapters, but its exact behavior for an uncataloged public feed
added by URL remains unverified here. AntennaPod's inspected source supports
both formats, truncates JSON fractions, and can retain an older chapter list
after feed refresh. Its chapter dialog's refresh action may help, but does not
guarantee every correction replaces stored chapters.

A [phone-side AntennaPod chapter test](antennapod-chapters-test.md) passed chapter
skips, a metadata update and offline chapter use on 2026-09-24, by user report. It used generated speech
and inline Podlove chapters over the LAN. The earlier
[AntennaPod LAN test](antennapod-lan-test.md) covered playback and downloads.
Remaining checks with generated audio are:

1. Change existing chapter start times and remove all chapters, then refresh.
2. Test public HTTPS JSON in Pocket Casts and Apple Podcasts via Add/Follow by
   URL. Pocket Casts' server-side fetcher cannot use a LAN-only feed.

Record observed app behavior separately from valid server output. iPhone
LAN-only testing remains deferred.
