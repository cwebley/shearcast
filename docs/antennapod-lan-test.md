# AntennaPod LAN smoke test

Tested on 2026-09-23. Phone-side results are the user's reports; server-side
results were checked through the CLI and HTTP requests.

## Setup

- Built-in `shearcast serve` on macOS 15.7.7, reachable over the home LAN.
- A fresh AntennaPod installation on the user's Android phone. App and Android
  versions were not recorded; the user declined version collection.
- A 76.4-second spoken recording generated locally and encoded as AAC/M4A,
  955,395 bytes. The second episode reused the same recording with a separate
  episode ID, title and enclosure URL.
- Separate temporary configuration, state, cache and publication directory.
  Channel synchronization was disabled. Episodes were manually published from
  the generated audio, with fixture metadata already in the temporary state.
- No model calls, YouTube downloads or R2 publication. Personal configuration,
  credentials, state and the existing R2 library were unchanged.

## Results

| Check | Result |
| --- | --- |
| Subscribe by LAN RSS URL | Passed. The show and first episode appeared. |
| Audio playback | Passed. The episode played in AntennaPod. |
| Seeking | Passed using both skip buttons and dragging the progress bar. |
| Download and offline playback | Passed by user report after downloading and enabling airplane mode. |
| Feed refresh | Passed. Episode 2 appeared alongside Episode 1 without duplicates. |
| Server restart | Passed. The existing subscription refreshed and Episode 2 played after stopping and restarting `serve`. |
| Server-side episode removal | Passed. RSS contained only Episode 1 and Episode 2's audio URL returned HTTP 404. |
| AntennaPod after removal | Episode 2 remained in the app's history after refresh. |
| Pocket Casts with the same LAN URL | User reported it did not work, matching the expected server-side feed-fetching limitation. |

Mac-side HTTP checks also confirmed audio HEAD responses and successful byte
range requests through the LAN address.

## Scope

This verifies the local feed and listening workflow on the tested phone with a
short generated sample. Artwork was not included. The initial playback path
was not distinguished from an automatic download, and Wi-Fi state during the
airplane-mode check was not independently observed. This test does not establish
compatibility across all AntennaPod/Android versions or verify source processing,
long-episode listening or scheduled unattended operation.

Removing an episode from Shearcast does not remove AntennaPod's own historical
record or recall downloaded audio. The server's feed and media deletion are the
checks for successful removal.

The temporary test server was stopped after the test. Downloaded copies and the
test subscription may remain on the phone.
