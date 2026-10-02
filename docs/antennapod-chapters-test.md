# AntennaPod chapter smoke test

Tested on 2026-09-24. Phone-side results are the user's reports. Server-side
results were checked separately through HTTP requests and the published feed.

## Setup

- Built-in `shearcast serve` on the Mac, reachable over the home LAN.
- AntennaPod on the user's Android phone. App and Android versions were not recorded.
- A generated spoken episode, **Chapter demo: lighthouse and moon garden**, in
  the **Shearcast chapter test** feed.
- A 105-second source with a fake sponsor section from 30 through 45 seconds.
  Production `render.Cut` removed that section with a 0.05-second crossfade.
- Published AAC/M4A duration of 89.950 seconds, 1,043,625 bytes. The initial
  inline Podlove chapters started at 0, 29.950 and 59.950 seconds.
- Separate temporary config, state, cache and publication directory. Source
  synchronization was disabled. Production publishing and metadata refresh used
  fixture metadata, without a YouTube or model client.

## Results

| Check | Result |
| --- | --- |
| Demo playback and chapter skips | Passed by user report. |
| Chapter metadata update | Passed by user report. |
| Downloaded playback and chapter skips offline | Passed by user report after the airplane-mode checklist. |
| Server-side correction | Verified two chapter markers, with the second renamed to **Lighthouse updated** and the moon-garden marker removed. |
| Identity and audio during metadata updates | Verified unchanged GUID, enclosure URL and downloaded audio bytes. |
| HTTP feed, audio, HEAD and byte ranges | Passed through the Mac's LAN address. |
| Private files excluded from HTTP serving | Passed. Config, state and private record requests returned 404. |

The user reported that the demo, chapter skips, update and offline test worked
well. The exact app refresh action was not recorded. The fixture retains the
updated two-chapter list. The temporary server was stopped after the offline
test, and port 8765 was checked to have no listener.

## Scope and remaining checks

This verifies chapter navigation, a metadata correction and offline use in the
tested AntennaPod setup using inline Podlove chapters. It does not establish behavior
across all app versions or every chapter correction.

Changing an existing chapter's start time and removing all chapters have not yet
been confirmed on the phone. The server-side
clear/reset operations passed automated HTTP checks. Public HTTPS JSON chapters
in Pocket Casts and Apple Podcasts remain separate device checks.

No live model call, YouTube download, remote publication, personal configuration
or state change, library reset, or commit was needed for this demo.
