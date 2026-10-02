# Real-source chapter test

Run on 2026-09-24 after the user authorized a real video download and model calls.
Server-side checks passed. The user confirmed chapter navigation on the phone,
but reported that the cut removed some genuine Ubiquiti doorbell discussion.
This is a detector-accuracy failure, not a clean end-to-end acceptance pass.

This is a historical test record. Subsequent changes added source-download
progress, moved acquisition before detection, changed boundary estimation and
removed the unused verification question. Saved settings, failures and proposed
investigations below describe the tested version, not the current implementation.
See [the methodology plan](detection-methodology-plan.md) for current proposals.

## Source and output

- Linus Tech Tips, [I Tried All The Best (and Worst) Doorbells](https://www.youtube.com/watch?v=qgLaCZyKv_8), published 2026-09-17.
- Nine source chapters, source metadata duration 1050 seconds and 470 parsed
  original English caption cues. Probed source AAC duration was 1050.308 seconds.
- An isolated filesystem feed with source synchronization disabled. Personal
  configuration and state were unchanged. No remote publishing or commit.
- Production detection found one region. The final retained ranges were
  `[0, 913.0216216216216]` and `[978.12, 1050.308209]`, with a 0.05-second crossfade.
- Published AAC/M4A at 128 kbps: 985.160 seconds, displayed as **16:25**,
  16,027,477 bytes. Successful detection/rendering took 15.343 seconds with
  captions, metadata and source audio already cached. This is not a full cold
  processing-time or resource measurement.

All nine chapter headings survive. Starts before the removed interval are
unchanged. **Outro** moves from source 17:18 to 16:12.852, displayed as 16:13.
The description contains one adjusted chapter list. Other source description
content, including links, is preserved.

## Acquisition failures and workaround

The normal caption request returned HTTP 429 before model work. A later request
for only `en-orig`, with a ten-second subtitle delay, downloaded the genuine
English captions. This does not isolate whether the narrower selection or delay
resolved the error.

The first render completed detection, then failed downloading audio. The
selected Google video CDN timed out connecting over TCP. yt-dlp's default
20-second connection timeout and ten retries made this slow; Shearcast captured
the subprocess output, so the retry progress was hidden until failure.
Independent IPv4/IPv6 probes and a bounded AAC-format retry reproduced the
connection failure. A different player client selected a different media host
that also failed a TCP probe. No system or environment proxy was configured.

The user reported successful browser playback. A temporary HTTP/3 probe fetched
an audio range from the original CDN. A single long transfer was too slow for
its deadline, but four concurrent 512 KiB byte-range requests completed the
original English AAC file. The script checked every returned range and byte
count, then verified the duration and codec before installing the cached source.
The actual browser transport was not inspected.

This workaround lives only in the temporary test directory. It is not an
implemented automatic fallback in Shearcast. Source download progress, retry
bounds and reliable transport selection remain follow-up work before claiming
an unattended cold-source test passes here.

## Model usage

| Attempt | Requests | Reported input/output tokens | Calculated cost | OpenRouter-reported cost |
| --- | ---: | --- | ---: | ---: |
| Detection followed by failed audio download | 9 | 48,602 / 4,040 | $0.002041284 | $0.002041284 |
| Successful render from cached inputs | 9 | 48,602 / 4,040 | $0.002041284 | $0.002041284 |
| Total | 18 | 97,204 / 8,080 | $0.004082568 | $0.004082568 |

The failed attempt never reached the durable render checkpoint, so retrying
repeated detection. State retained both attempts' usage. Missing-usage,
missing-cost and unresolved-request counters were zero. Publication added no
model work. These are model charges, not an account invoice or hosting costs.

## Verification

HTTP checks through the LAN address passed:

- RSS with one stable episode GUID and all nine inline Podlove chapters.
- Every chapter matched the final retained-range/crossfade mapping.
- The readable description replaced the source list with adjusted timestamps.
- Downloaded enclosure bytes matched the render record's SHA-256 and size.
- FFprobe duration matched the render record and calculated timeline.
- HEAD and byte ranges worked; private state, config and record URLs returned 404.
- Source audio and duplicate managed private audio were removed. The private
  processing record remained, and state pointed to the served audio.

The user reported that chapters worked well and the boot.dev sponsor material
was cut, but some Ubiquiti doorbell discussion was also removed. Inspection of
the saved record confirms that detection itself chose a start at 913.022 seconds,
inside the product verdict. The chapter mapper and renderer used that boundary.
The transcript places the combined Ubiquiti conclusion/sponsor segue sentence
at 934.220 seconds and the explicit sponsor pitch at 943.320 seconds. The exact
desired edit point still needs listening, but the current start removes genuine
review content roughly 20–30 seconds before those later boundaries.

The saved end boundary also deserves review. The cut resumes at 978.120 seconds,
while the captions continue a sponsor skit and a boot.dev offer through about
1037.521 seconds. The user did not separately confirm complete removal of that
tail. Do not classify the whole sponsor block as successfully removed merely
because its main pitch was cut.

Preserve this example for detector regression work, including the beginning,
the comedic sponsor tail and the selected program-context text. Public HTTPS
JSON chapters remain separate from this successful LAN chapter-navigation test.

## Decisions after the test

On 2026-09-24 the user agreed that downloading and verifying source audio before
paid detection would avoid wasting detection work on an unavailable source.
The follow-up clarified that captions must come first, so captionless videos do
not trigger audio downloads. This ordering is now implemented: fetch and validate
captions, download audio and probe for a positive duration, then run Jev.
Source-stage failures record zero new model requests and preserve prior usage.
The attempt totals above remain the historical evidence from before this change.

The user raised the possibility of controlling caption and audio fetching more
directly, but explicitly deferred that work. No decision was made to replace
yt-dlp, choose a fetching library, or make the temporary HTTP/3 workaround a
production dependency. The next fetching discussion should use the transport
and caption evidence above, rather than assume yt-dlp alone caused both failures.

Detector iteration is also deferred. This video is the agreed case to return to.
The immediate work is preserving and documenting the baseline; no prompt,
threshold, fit-window, verification policy or cut boundary was changed after
the listening feedback.

## Detector regression baseline

The tested model was `typesafe/jev-1.13`. The test channel used `no_weights = true`,
so the opening boundary came from the monotone-predicate path, not fitted start
weights. Verification was enabled. Other effective settings include:

| Setting | Saved value |
| --- | ---: |
| Scan window / maximum | 30 / 45 seconds |
| Anchor threshold | 0.85 |
| Lead-in / tail search spans | 180 / 180 seconds |
| Maximum candidates | 60 |
| Repeated boundary questions | 3 |
| End fit window | 11 candidates |
| Context span | 120 seconds |
| Cluster bridge gap / segment merge gap | 90 / 2 seconds |
| Minimum region | 8 seconds |

The record preserves the complete effective options and rule prompts. The
following source-time points matter for the next investigation:

| Evidence | Time or saved value | Interpretation |
| --- | --- | --- |
| Sponsor scan anchor | 929.800–992.510 seconds; `AnchorProb = 1` | A detected sponsor anchor, not approved edit boundaries. |
| Selected opening candidate `L026` | 913.021622 seconds; score 0.53 | Starts during the flagship doorbell recommendation. User-confirmed content loss. |
| Next opening candidate `L027` | 926.478333 seconds; score 0.54 | Continues the editorial data-ownership discussion. |
| Opening candidate `L028` | 934.220 seconds; score 0.87 | One sentence mixes the Ubiquiti conclusion with the sponsor segue. |
| Opening candidate `L029` | 943.320 seconds; score about 0.78 | Explicit sponsor pitch. |
| Selected closing candidate `R003` | 978.120 seconds; score about 0.29 | Resumes during the sponsor skit according to the captions. |
| Closing candidate `R015` | 1030.240195–1037.521 seconds; score 0.15 | Still contains the boot.dev offer. |
| Closing candidate `R016` | 1037.531 seconds; score about 0.74 | Returns to video-related closing remarks. |
| Selected start/end step heights | about 0.602 / 0.163 | Saved changepoint diagnostics. |
| `ContentLoss` verification result | 0.6 | The completed cut was still published. This is a model score, not a calibrated probability. |

These are caption-derived sentence positions and model observations, not
hand-labeled ground truth. Confirm an exact desired cut against the original
audio before turning it into an acceptance assertion. In particular, preserving
the Ubiquiti verdict while removing the mixed segue may require a boundary
within a sentence rather than treating that entire sentence as advertising.

### Starting points for the later investigation

- `internal/detect/detect.go`: inspect `startState`, program-context selection,
  `startPredicate` and `segueBrief`. The saved `StartState` includes sponsor-tail
  material in the passage labeled as the program's own narration. Assess
  whether that context and the treatment of segues contributed to the early cut.
- `findEdge` and `EndFitWindow`: all end candidates were scored, but only the
  first eleven entered the fit. `R016`, the later return to the program, lies
  outside that fit window. Compare the saved full curve before changing this cap.
- `bound` and verification handling: inspect what should happen when the content
  loss score conflicts with the proposed cut. Preserve the existing result as
  evidence rather than silently treating verification as an acceptance gate.
- Keep opening-boundary preservation and closing-boundary completeness as
  separate checks. Removing a sponsor's main pitch is insufficient if editorial
  content is lost or the sponsor skit and offer remain.

These are investigation leads from the saved trace, not proven root causes or
approved fixes. Use this case alongside earlier detector cases when evaluating
changes so a narrower fit or more aggressive segue rule does not merely trade
one failure for another.

## Saved replay evidence

The evidence was copied out of temporary storage into the repository's existing
ignored `work/` directory:

```text
work/detector-cases/qgLaCZyKv_8/baseline-20260924.tar.gz
SHA-256: f46eeb555127118b0876421b74272b4a6780184abbb0799a58372608be4aac55
```

The archive contains original English captions, source metadata, the full render
record, isolated test config/state, published RSS and edited audio, and the
detector/transcript source code from the tested worktree. The render record
includes scan windows, start/end prompt states, boundary curves, effective
settings and usage. The state retains usage from both processing attempts.

Archive-relative paths for the main inputs are:

```text
cache/qgLaCZyKv_8/qgLaCZyKv_8.en-orig.vtt
cache/qgLaCZyKv_8/info.json
cache/renders/real-chapters/qgLaCZyKv_8/render.m4a.json
public/real-chapters/qgLaCZyKv_8.m4a
```

The original unedited audio was removed by normal pipeline cleanup before the
feedback. It is not in the bundle. Saved captions and curves permit offline
inspection; listening to removed content needs the original source again.
Extract the archive into a new scratch directory for later analysis and keep
the baseline intact. Archived state contains the old absolute test paths, so
it is evidence rather than a ready-to-run replacement for active state.
The temporary LAN server was stopped after preserving the case; port 8765 was
checked to have no listener. The archived edited audio remains available for
later comparison.
