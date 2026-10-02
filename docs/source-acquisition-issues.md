# Source acquisition issues

Observed caption and audio failures from 2026-09-24 and 2026-09-25. The caption
selection fix and download-progress reporting are implemented. The HTTP/3
workaround remains an experiment, not an automatic production fallback. These
observations do not establish current YouTube availability.

## Caption downloads return HTTP 429

**2026-09-24 sync.** The first caption request of the run (hotu
`nPLKfvr_y4E`, auto captions only) succeeded at 16:38. From 16:40, every
auto-caption request failed with `Unable to download video subtitles for 'en':
HTTP Error 429: Too Many Requests`: primetime `iuccfEQgIeY`, gabfest
`NQzDpjDDWQw` and coolworlds `HBpFIYRjuuM`. A retry 20 minutes later and a
manual yt-dlp probe at about 17:05 also failed. Space Time `568-39aOZkw`, which
has creator-uploaded English subtitles, succeeded at 16:41 between the
failures. Audio downloads and metadata requests kept working.

The same session had made heavy YouTube requests beforehand: about a dozen
isolated renders, single-video metadata lookups and full listings of two
channels and a 225-entry playlist.

yt-dlp was 2026.08.19, the latest stable release, so this is not an outdated
version. Upstream tracks it as a YouTube-side restriction (yt-dlp issue
#13831; #17536 was closed as a duplicate of it). Reports in #17412 describe
limits after 20–30 auto-caption sets and blocks lasting hours to days.
Upstream suggests browser cookies or `--sleep-subtitles` of 70 seconds or more.

**Earlier case.** The LTT doorbell test (`qgLaCZyKv_8`, see
[real-source-chapters-test.md](real-source-chapters-test.md)) also hit caption
429. Requesting only `en-orig` with a ten-second subtitle delay then worked. It
was not isolated which change mattered.

**Cause found, 2026-09-25.** On an auto-captioned video, the auto `en` track
is a translated rendition of `en-orig`, and YouTube was refusing only that one.
Primetime `iuccfEQgIeY` has just those two tracks. `--sub-langs en,en-orig`
still failed on `en`, while a request for `en-orig` alone succeeded right after.
yt-dlp cannot ask for a manual `en` without falling back to the auto `en`, so
`Captions` now makes up to two single-track requests: `--write-subs en`, then
`--write-auto-subs en-orig` only when no creator track exists. With that
change, primetime and coolworlds (both failing the night before) rendered and
published. Creator tracks named `en-US` or `en-GB` are no longer matched; every
cached track so far was plain `en`.

## Audio download cannot connect

The doorbell test's audio download timed out connecting over TCP to the
selected Google video CDN host. A different player client chose another host
that also failed. The browser played the video. A temporary script fetched the
file over HTTP/3 in four concurrent byte-range requests. That workaround was
never built into Shearcast, and the downloader transport decision was deferred.
All audio downloads on 2026-09-24 succeeded quickly (153 MB in 13 seconds).

## Remaining investigation

If failures recur, distinguish caption rate limits from audio transport failures
before changing the downloader. The narrow, separate caption requests are already
implemented; retry pacing still needs measurement. Transcription fallback would
need separate evaluation of timestamp accuracy, cost and resource use. No provider
or automatic fallback has been selected.
