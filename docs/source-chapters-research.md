# Source chapter formats and client support

Research date: 2026-09-23. This is primary-source research, not a device test or an implementation decision.

## Recommendation

Use **Podcasting 2.0 JSON chapters plus the retimed chapter list in the description** for public HTTPS feeds. Emit only titles and start times initially. Keep the source chapters and the published audio's edit timeline separately, then derive both public representations from the same retimed list.

JSON is the simplest single structured representation with current documented or source-confirmed support in Pocket Casts mobile, AntennaPod, and Apple Podcasts generally. It supports metadata-only changes without replacing the audio. Apple's exact behavior for an uncataloged public feed added by URL still needs a device check. The official documentation does not resolve that case explicitly. [1][2][4][6][9][10]

For the existing **HTTP LAN deployment**, inline Podlove Simple Chapters is a useful fallback generated from that same list. The current Podcasting 2.0 namespace specification requires HTTPS resource URLs; silently emitting HTTP JSON links would relax that specification. Inline Podlove avoids a chapter URL and an extra fetch. Its namespace URI starts with `http`, which identifies the XML vocabulary and does not require an HTTP request. [1][3]

Inline Podlove plus description timestamps is the smallest implementation overall. It adds no public object, route, or publication transaction. But it has two material limits: Apple does not document Podlove support, and AntennaPod's feed-update code preserves already-stored inline chapters. For a feature explicitly concerned with refreshing existing chapter metadata, those limits make it a weaker sole representation. [3][4][6][8]

Avoid embedded M4A chapters in the first slice. They couple a title or timestamp correction to replacing a media file, leave downloaded copies stale, and do not solve Pocket Casts Android support. JSON adds a small artifact lifecycle, but that is a better fit for metadata refresh than remuxing the audio. If only a feed-only first slice is approved, describe Podlove as that narrower choice rather than claiming native chapter compatibility across all target apps. [2][4][12][13]

## Scope and evidence

I read the source-chapter handoff and the original release specification's chapter section, lifecycle, publishing, app compatibility, and phase 5. The original specification requires source-provided titles, no additional model calls, final retained-range retiming including crossfades, omitted wholly removed topics, deliberate handling of partially removed topics, duplicate-start collapse, preserved original metadata, replacement of recognized description chapter lists, and structured metadata.

Those temporary planning documents are not distributed with the repository.
[Source chapters](source-chapters.md) records the implemented behavior and the
remaining client checks. The requirements above establish Shearcast's scope,
not third-party format support.

All external references below are official specifications, vendor documentation, or the owning project's source. Web pages were checked on the research date. A copyright year is not a publication or update date. Where a page exposes no revision date, this document gives the observation date only.

Pinned evidence:

- Podcast namespace source at `c0ff5caa3729610362ee93f8034454fa41f3c493`, commit dated 2026-05-13. JSON chapter syntax labels itself version 1.2, updated 2021-04-15. Its JSON examples use `"version": "1.2.0"`. [1][2]
- AntennaPod release `3.12.2`, published 2026-09-18. Client-specific claims below use that release's source, rather than assuming the development branch has reached users. [5]–[8]
- yt-dlp source at `c7fb478d21e9e59524befbe23f7801bb267fb880`, commit dated 2026-09-16. Its version file reports `2026.08.19`; this is a pinned source inspection, not a claim that the installed binary or every distributed build is identical. [15]–[17]
- FFmpeg source at release tag `n8.0` for the Nero chapter limits. The official live muxer documentation independently describes the two embedded chapter representations. [13][14]

No chapter format was tested on a real device in this research. The existing [AntennaPod LAN smoke test](antennapod-lan-test.md), dated 2026-09-23, verifies feed subscription, playback, seeking, downloads, and refresh of episode listings. It does not verify chapter navigation, chapter refresh, or chapter timing. The phone's app version was not recorded. No media downloads, paid calls, or remote publication were performed for this research.

## Format comparison

| Representation | Placement and useful minimum | Metadata refresh | Main cost or limit |
| --- | --- | --- | --- |
| Podcasting 2.0 JSON | One item-level `<podcast:chapters>` pointing to JSON with `version`, `chapters`, and each chapter's `startTime` and `title` | Replace small metadata or publish a new metadata revision, without changing audio | Extra public object and fetch; HTTPS requirement; client caches and chapter merging can preserve old data |
| Podlove Simple Chapters | One item-level `<psc:chapters>` with `<psc:chapter start="..." title="..."/>` children | Update the RSS | Larger feed; no documented Apple parser; AntennaPod does not simply overwrite stored inline chapters on refresh |
| Embedded M4A | QuickTime chapter track and/or Nero `chpl` atom in the enclosure | Rewrite or remux the enclosure and republish its metadata | Downloaded copies remain old; container variants differ; Pocket Casts Android does not support Apple's M4A chapter format |
| Description timestamps | Readable source titles with adjusted `HH:MM:SS` timestamps | Update the RSS description | Useful fallback, but native navigation and parsing vary; Apple documents a zero start and at least three chapters for this route |

These are format and source-code findings, not demonstrated client behavior with Shearcast output. [1]–[14]

### Podcasting 2.0 JSON chapters

The canonical namespace is `https://podcastindex.org/namespace/1.0`. The namespace specification also requires clients to recognize the older GitHub specification URL. The item permits a single chapter element with required `url` and `type` attributes. The specified MIME type is `application/json+chapters`, including that exact order of suffixes. The JSON syntax also says to serve the document with that Content-Type. [1][2]

```xml
<rss version="2.0" xmlns:podcast="https://podcastindex.org/namespace/1.0">
  <channel>
    <!-- other channel fields -->
    <item>
      <!-- other episode fields -->
      <podcast:chapters
        url="https://example.com/show/episode.chapters.json"
        type="application/json+chapters" />
    </item>
  </channel>
</rss>
```

```json
{
  "version": "1.2.0",
  "chapters": [
    {"startTime": 0, "title": "Opening"},
    {"startTime": 779.95, "title": "Source topic heading"}
  ]
}
```

The example is illustrative, not a claim about a particular source episode. Times are numeric seconds with fractional precision, in ascending order. JSON requires only `startTime` per chapter; `title`, `endTime`, `img`, `url`, `toc`, and location data are optional. Apple separately requires a timestamp and title, so Shearcast should supply both. Omit optional fields that the feature does not need. In particular, `toc: false` means a hidden metadata marker in the specification, but AntennaPod 3.12.2's JSON parser does not inspect it. [2][6][10]

The specification explicitly motivates external chapters as editable after publication and accessible without modifying audio. It allows clients to request them at playback or cache them with a download. That is permission to cache, not a freshness guarantee. The reviewed specification states no universal maximum chapter count or mandatory two-minute chapter length. [2]

### Podlove Simple Chapters

Use the exact namespace `http://podlove.org/simple-chapters`. An RSS item contains one chapter-list element, with one or more chapter children. Each child requires a `start` and `title`; `href` and `image` are optional. Normal Play Time syntax supports both whole seconds and fractions. [3]

```xml
<psc:chapters xmlns:psc="http://podlove.org/simple-chapters" version="1.2">
  <psc:chapter start="00:00:00.000" title="Opening" />
  <psc:chapter start="00:12:59.950" title="Source topic heading" />
</psc:chapters>
```

Always use a colon-delimited time such as `HH:MM:SS.mmm` for AntennaPod compatibility. Although the Podlove specification allows bare seconds such as `37`, AntennaPod 3.12.2's `DateUtils.parseTimeString` accumulates time only when it sees at least two colon-separated components. Bare `37` therefore becomes zero in that source. [3][7]

The current Podlove page is internally inconsistent about versions: its prose calls 1.1 current, its embedding example uses 1.2, and an omitted version defaults to 1.0. The example above follows its explicit 1.2 example. AntennaPod's parser does not branch on the version attribute. The page also has stale introductory prose about images despite documenting an `image` attribute. Use the element definitions and verify actual client output rather than treating every sentence as a consistent revision history. [3][7]

The specification describes external Podlove chapter resources too. Inline RSS is the useful choice here; external Podlove would add object management without gaining the current Apple documentation that JSON has. [3][9][10]

### Embedded M4A chapters

M4A chapter support is not a single interchangeable encoding:

- Apple's QuickTime documentation defines the `chap` track reference as a chapter or scene list, usually referencing a text track. [12]
- FFmpeg documents that it normally writes both a QuickTime chapter track and Nero chapter markers. `-movflags +disable_chpl` suppresses the Nero `chpl` atom while leaving the QuickTime chapter track. These flags matter to compatibility. [13]
- AntennaPod 3.12.2's M4A reader specifically finds `moov.udta.chpl`, then reads titles and starts. This establishes Nero support in that code, not general support for every QuickTime chapter track, embedded chapter artwork, or chapter links. [8]
- FFmpeg `n8.0` limits its Nero output to 255 chapters and 255 title bytes per chapter. Those are limits of that writer's Nero representation, not of JSON or all MP4 chapter schemes. AntennaPod's reader uses a signed byte for the title length; a length of 128–255 bytes can therefore fail in the inspected implementation. This is a source-level edge case, not a reproduced device failure. [8][14]

A metadata-only remux can avoid audio re-encoding, but it still creates a different media file. For Shearcast that would mean updating checksum, size, durable installation state, and publication together. It cannot update an already-downloaded enclosure in place on a phone. Embedding during initial rendering and later refreshing only feed-side chapters would also leave two potentially conflicting chapter lists. AntennaPod explicitly merges chapter sources rather than always preferring the newest external list. [6][8]

## Target app support

| Target | JSON chapters | Inline Podlove | Embedded chapters | Evidence and limits |
| --- | --- | --- | --- | --- |
| Pocket Casts Android | Documented as Podcast Index chapters | Documented | MP3 documented; Apple's M4A chapter format explicitly unsupported | Official current support page, no minimum app version stated [4] |
| Pocket Casts iOS | Documented as Podcast Index chapters | Documented | AAC and MP3 documented | Official current support page; it does not specify each MP4 atom variant [4] |
| Pocket Casts web/desktop | Some chapter sources supported | Some chapter sources supported | No complete format guarantee | Vendor explicitly warns that mobile chapter availability does not imply web/desktop availability [4] |
| AntennaPod 3.12.2 | Source-confirmed URL parser, JSON parser, fetching, and chapter loading | Source-confirmed RSS parser | ID3, Vorbis, and Nero M4A loading in source | Precision, stored-data, and refresh caveats below [5]–[8] |
| Apple Podcasts generally | Explicitly documented through `<podcast:chapters>` | No support claim found in reviewed official material | MP4 header and MP3/AAC ID3 documented | Description timestamps also documented [9][10] |
| Apple Podcasts, uncataloged public RSS added by URL | Exact workflow not explicitly confirmed | Not documented | General embedded support documented; exact workflow untested | Following by URL is official, but chapter-specific behavior for this case needs verification [9]–[11] |

### Pocket Casts

The current chapter help page is explicit about mobile platform differences. Do not translate its Android support for MP3 chapters into support for Shearcast's M4A enclosure. JSON and Podlove avoid that container-specific limitation. Chapters may not appear until playback starts. [4]

The same page currently lists chapters for CarPlay and Apple Watch when the play source is Phone, but not Wear OS, Android Auto, or Android Automotive. Web and desktop have browser and source-access limitations. These are useful boundaries if reporting compatibility beyond the phone app. [4]

Pocket Casts documents server-side feed parsing. It says feed changes can take about 20 minutes on average and sometimes longer, depending on followers, upload frequency, and feed errors. This is not a chapter-refresh SLA. A LAN-only feed remains unreachable to its crawlers even when the phone can reach it. [18]

### AntennaPod precision and refresh

The released JSON parser reads `startTime` with `optInt`, then multiplies by 1000. Fractional seconds are lost in that path. It reads `title`, `url`, and `img`, but does not use `endTime`, `toc`, or `version`. A JSON start of `779.95` therefore does not retain its fractional precision in this implementation. That is within the specification's whole-second tolerance for Shearcast, but it can differ from a description rounded to `00:13:00`. Keep accurate canonical times and document client precision rather than remapping the whole timeline to integers. [6]

Refresh has several distinct layers:

1. `FeedItem.updateFromOther` imports inline chapters only when the existing item does not already have chapters. A normal feed refresh is therefore not reliable evidence that an existing Podlove list was replaced. It does update a non-null Podcast Index chapter URL. [8]
2. `ChapterUtils.loadChapters` returns early when chapters are already loaded unless forced. Its external JSON loader first tries the HTTP cache, uses the network if it has no usable multi-chapter cache, and uses `FORCE_NETWORK` for a forced refresh. The chapter dialog has a refresh handler calling that forced path. [6]
3. `ChapterMerger` favors a longer list when sources have different counts. For equal counts it compares starts with a one-second tolerance and merges metadata, or chooses by a completeness score when starts differ too much. Thus even freshly fetched JSON can lose to an older stored or embedded list, especially when a correction removes chapters. Changing a URL alone does not prove the displayed list updates. [8]

JSON makes refresh possible without audio replacement, but no representation here guarantees that an existing AntennaPod subscription immediately shows all corrections. Test title changes, moved starts, fewer chapters, and removal of all chapters separately. Avoid emitting conflicting embedded and feed-side lists. If using both Podlove and JSON, derive them from one list and include their interaction in the device checks.

AntennaPod documents direct on-device feed fetching, which is why the HTTP LAN workflow is plausible. Its source has no HTTPS-only validation in the inspected chapter tag handler. An HTTP JSON URL may work in the app, but that is not proof of Podcasting 2.0 specification compliance or a completed LAN chapter test. [1][6][19]

### Apple Podcasts and public custom RSS

Apple's current chapter article documents three publisher-supplied routes: description timestamps, `<podcast:chapters>`, and file metadata. Its RSS guide explicitly lists `<podcast:chapters>` as an item-level tag. Older advice that Apple supports only embedded chapters is now incomplete. [9][10]

Separate stated requirements from editorial guidance:

- For description-derived chapters, Apple says to start at `00:00:00` and provide at least three chapters. Do not invent headings or preserve removed topics just to reach three. [9]
- The RSS guide requires a timestamp, optionally with millisecond resolution, and a title. It recommends titles no longer than 45 characters. JSON's optional title is therefore not a sufficient Apple-oriented minimum. [10]
- Six chapters per hour, chapters at least two minutes long, fewer than five title words, and the other title-style rules appear under best practices. They are not stated as universal parser rejection rules. Preserving source titles takes priority over silently rewriting them for editorial style. [9]
- Automatic chapter generation starts with iOS 26.2, applies to English full and bonus episodes, and excludes trailers and episodes under ten minutes. Apple also says it does not create chapters for episodes outside its catalog. Those statements concern generated chapters and should not be turned into a blanket prohibition on publisher-supplied chapters. Nor does the iOS 26.2 statement establish the minimum version for every supplied format. [9]

Apple officially supports adding a feed by URL without publishing it to its directory. Such listeners follow the hosting provider directly. A publicly reachable R2 RSS URL that has not been submitted to the catalog is still an uncataloged feed for this distinction. [11]

The documentation does not explicitly establish that description parsing and external JSON ingestion behave identically for this direct-follow path and the catalog path. Its chapter article discusses RSS crawling and tells publishers to contact Apple if updates take more than 24 hours. That is not a documented refresh contract for custom subscriptions. Record native chapter behavior on the actual iOS/macOS version before claiming this target passes. Readable description timestamps remain useful even when a client does not expose chapter controls. [9][11]

## yt-dlp chapter metadata

The extractor contract defines the optional `chapters` field as a list of dictionaries. The keys use snake_case, unlike Podcasting 2.0 JSON. [15]

```json
{
  "chapters": [
    {"start_time": 0, "end_time": 900, "title": "Opening"},
    {"start_time": 900, "end_time": 1800.5, "title": "Source topic heading"}
  ]
}
```

`start_time` is in seconds. `end_time` is optional at the extractor boundary; yt-dlp's core can infer it from the next chapter's start or the video's duration. `title` is optional. The contract treats `None` as absence of information. Handle omitted, null, and empty chapter collections, and accept numeric seconds without assuming integers. Do not substitute the separate singular `chapter`, `chapter_number`, or `chapter_id` fields, which describe a video's membership in another logical chapter. [15]

The inspected YouTube extractor tries player JSON chapters, then engagement-panel chapters, then description-derived chapters. The helper validates starts against the duration, accepts equal consecutive starts, and sorts description-derived chapters. The extraction result can be `None`. There is no per-chapter provenance flag in the documented shape distinguishing creator-written headings from every other heading supplied by YouTube. Call these source-provided chapters, not verified human-authored chapters. [16]

yt-dlp core may insert a zero-start chapter when the first chapter starts later, fill missing end times, and replace empty titles with `<Untitled Chapter N>`. Thus even `--dump-single-json` can contain a title synthesized by yt-dlp rather than a source heading. Preserve the raw metadata and choose an explicit policy for these placeholders. Do not add further headings merely to meet Apple's description parser requirements. A source chapter's last end can remain unknown when the video duration is unavailable. [17]

Shearcast's current `internal/youtube/youtube.go` uses `--skip-download --dump-single-json` for full episode info and `--flat-playlist --dump-json` for listing. Full info is the appropriate input for chapters. A flat playlist record or old chapterless cache record does not establish that the source has no chapters.

## Implications for the implementation decision

These are recommendations derived from the original specification and the findings above, not newly discovered format requirements.

- Store original description and original source chapter intervals. Keep retimed output separate. Preserve enough field presence to distinguish an unknown final end from a real end at zero.
- Map each chapter's surviving content through the exact retained ranges and crossfade overlaps of the audio currently in the feed. A pending rerender's timeline is not the published timeline. Repeated refresh starts from original metadata each time.
- Omit wholly removed topics; for a partially removed topic use its first surviving audio. Collapse resulting duplicate starts deterministically. Keep fractional starts for structured output and round only the readable timestamps. The original worked example maps source `15:00` to `779.95` seconds after a two-minute cut and one 0.05-second overlap.
- If a historical edit timeline is absent or invalid, do not infer it from the edited duration or current detector settings. Report that retiming is unavailable. Caller-owned audio imports likewise need an explicit timeline or an explicit unedited-source contract before source timestamps can be trusted.
- Rewrite only a confidently recognized source chapter list in the original description. Other timestamps, links, and prose must survive. yt-dlp's permissive description extraction is useful evidence for input variants, not a sufficient rule for deleting every timestamped line.
- For JSON, publish the chapter object before the feed reference. A content-revision URL can prevent an HTTP cache from serving an old object under a new reference, but it does not override app-level stored chapters. Track old and new objects through failed publication, replacement, retention, and removal on both adapters. A stable URL is simpler but needs suitable cache validation and accepts weaker cache invalidation.
- Give public chapter JSON an explicit managed path and Content-Type. Add only that artifact to the server allowlist. Private render records are also JSON and must remain outside public serving.
- Define metadata refresh success at the publisher separately from client convergence. The inspected AntennaPod code makes that distinction necessary even with valid XML and JSON.

The remaining device evidence should cover a fresh subscription, native chapter listing and seeking, title/time corrections on the same GUID and enclosure, a reduced chapter list, offline chapters after download, and a refresh with both inline and JSON data if both are emitted. Use generated audio and record app/OS versions where available. Apple must be tested through Add/Follow by URL without catalog submission. This research does not authorize remote publication to perform that test.

## Primary references

All checked 2026-09-23 unless an explicit source date is given above.

1. Podcast Index, [namespace rules](https://github.com/Podcastindex-org/podcast-namespace/blob/c0ff5caa3729610362ee93f8034454fa41f3c493/docs/1.0.md) and [`podcast:chapters` tag](https://github.com/Podcastindex-org/podcast-namespace/blob/c0ff5caa3729610362ee93f8034454fa41f3c493/docs/tags/chapters.md). Required attributes, namespace, count, and HTTPS rule.
2. Podcast Index, [JSON chapter syntax](https://github.com/Podcastindex-org/podcast-namespace/blob/c0ff5caa3729610362ee93f8034454fa41f3c493/docs/examples/chapters/jsonChapters.md), version 1.2, updated 2021-04-15. Structure, MIME type, precision, ordering, optional fields, and caching.
3. Podlove, [Simple Chapters specification](https://podlove.org/simple-chapters/). Live page; no unambiguous revision date shown.
4. Pocket Casts, [Chapters](https://support.pocketcasts.com/knowledge-base/chapters/). Live support matrix and platform limitations; no minimum mobile version stated.
5. AntennaPod, [release 3.12.2](https://github.com/AntennaPod/AntennaPod/releases/tag/3.12.2), published 2026-09-18.
6. AntennaPod 3.12.2, [Podcast Index namespace handler](https://github.com/AntennaPod/AntennaPod/blob/3.12.2/parser/feed/src/main/java/de/danoeh/antennapod/parser/feed/namespace/PodcastIndex.java), [JSON parser](https://github.com/AntennaPod/AntennaPod/blob/3.12.2/parser/feed/src/main/java/de/danoeh/antennapod/parser/feed/PodcastIndexChapterParser.java), [chapter loading and caching](https://github.com/AntennaPod/AntennaPod/blob/3.12.2/ui/chapters/src/main/java/de/danoeh/antennapod/ui/chapters/ChapterUtils.java), and [chapter dialog](https://github.com/AntennaPod/AntennaPod/blob/3.12.2/app/src/main/java/de/danoeh/antennapod/ui/screen/chapter/ChaptersFragment.java).
7. AntennaPod 3.12.2, [Simple Chapters parser](https://github.com/AntennaPod/AntennaPod/blob/3.12.2/parser/feed/src/main/java/de/danoeh/antennapod/parser/feed/namespace/SimpleChapters.java) and [time parsing](https://github.com/AntennaPod/AntennaPod/blob/3.12.2/parser/feed/src/main/java/de/danoeh/antennapod/parser/feed/util/DateUtils.java).
8. AntennaPod 3.12.2, [M4A reader](https://github.com/AntennaPod/AntennaPod/blob/3.12.2/parser/media/src/main/java/de/danoeh/antennapod/parser/media/m4a/M4AChapterReader.java), [FeedItem update behavior](https://github.com/AntennaPod/AntennaPod/blob/3.12.2/model/src/main/java/de/danoeh/antennapod/model/feed/FeedItem.java), and [chapter source merging](https://github.com/AntennaPod/AntennaPod/blob/3.12.2/ui/chapters/src/main/java/de/danoeh/antennapod/ui/chapters/ChapterMerger.java).
9. Apple, [Chapters on Apple Podcasts](https://podcasters.apple.com/support/5482-using-chapters-on-apple-podcasts). Live documentation, including the iOS 26.2 automatic-chapter boundary.
10. Apple, [A Podcaster's Guide to RSS](https://help.apple.com/itc/podcasts_connect/en.lproj/itcb54353390.html). Static official version of the JavaScript-linked guide; chapter tag and namespace requirements.
11. Apple, [Test your podcast RSS feed](https://podcasters.apple.com/support/828-test-your-podcast) and [How Apple Podcasts distributes your shows to listeners](https://podcasters.apple.com/support/5108-how-apple-podcasts-distributes-your-shows-to-listeners). Direct URL subscription and catalog distinction.
12. Apple Developer Documentation, [QuickTime track reference type atom](https://developer.apple.com/documentation/quicktime-file-format/track_reference_type_atom), including `chap`. [Official Markdown representation](https://developer.apple.com/documentation/quicktime-file-format/track_reference_type_atom.md).
13. FFmpeg, [MOV/MPEG-4 muxer options](https://ffmpeg.org/ffmpeg-formats.html#MOV_002fMPEG_002d4_002fISOMBFF-muxers), particularly `disable_chpl` and `faststart`.
14. FFmpeg `n8.0`, [`mov_write_chpl_tag`](https://github.com/FFmpeg/FFmpeg/blob/n8.0/libavformat/movenc.c#L4873-L4900). Nero count, title-byte, and timestamp encoding limits.
15. yt-dlp, [extractor metadata contract](https://github.com/yt-dlp/yt-dlp/blob/c7fb478d21e9e59524befbe23f7801bb267fb880/yt_dlp/extractor/common.py), `InfoExtractor` docstring's `chapters` field.
16. yt-dlp, [YouTube chapter extraction](https://github.com/yt-dlp/yt-dlp/blob/c7fb478d21e9e59524befbe23f7801bb267fb880/yt_dlp/extractor/youtube/_video.py#L2336-L2362), [selection order](https://github.com/yt-dlp/yt-dlp/blob/c7fb478d21e9e59524befbe23f7801bb267fb880/yt_dlp/extractor/youtube/_video.py#L4405-L4410), and [chapter helper and description extraction](https://github.com/yt-dlp/yt-dlp/blob/c7fb478d21e9e59524befbe23f7801bb267fb880/yt_dlp/extractor/common.py#L3988-L4025).
17. yt-dlp, [core chapter normalization](https://github.com/yt-dlp/yt-dlp/blob/c7fb478d21e9e59524befbe23f7801bb267fb880/yt_dlp/YoutubeDL.py#L2871-L2883) and [version file](https://github.com/yt-dlp/yt-dlp/blob/c7fb478d21e9e59524befbe23f7801bb267fb880/yt_dlp/version.py).
18. Pocket Casts, [Podcast Parsing](https://support.pocketcasts.com/knowledge-base/podcast-parsing/). Server fetching, variable parsing delays, and crawler reachability.
19. AntennaPod, [Central and distributed podcast apps](https://antennapod.org/documentation/general/central-distributed). Direct on-device fetching and refresh behavior.
