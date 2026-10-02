# Publishing and serving

Shearcast publishes RSS, finished AAC/M4A audio and chapter metadata to either a local
directory or Cloudflare R2. Processing still uses one-shot `sync` runs.
For local hosting, a web server stays running between those runs.

## Home LAN with the built-in server

Choose paths owned by the user running Shearcast. This example uses:

```text
/srv/shearcast/config.toml
/srv/shearcast/.env
/srv/shearcast/state.json
/srv/shearcast/cache/
/srv/shearcast/public/       # feeds, audio and public chapter documents
```

Keep the config, `.env`, state, cache and custom render outputs outside `public`.
Shearcast rejects overlapping cache/public directories and private paths under
the publication root, including paths reached through symlinked ancestors.
Use one state file for all writers to a publishing directory.

Add these top-level config tables, alongside your rules and channels:

```toml
[publishing]
backend = "filesystem"
directory = "/srv/shearcast/public"
base_url = "http://192.168.1.20:8080"

[serve]
listen = "0.0.0.0:8080"
```

`directory` is a disk path. Relative paths resolve beside the config file.
`base_url` is the address clients use, including any URL prefix. It must be an
HTTP or HTTPS URL with no credentials, query or fragment. `listen` is the
interface and port the built-in HTTP server binds. Its default is
`127.0.0.1:8080`; `0.0.0.0:8080` accepts connections on all IPv4 interfaces.
A `-listen` flag can override it without changing feed URLs.

Use a stable LAN IP or local hostname that your phone can resolve. `localhost`
on a phone refers to the phone. A DHCP reservation helps keep subscriptions
working across server restarts. Allow the chosen port through the host firewall
for your LAN and keep the machine awake while clients refresh or download.

Start the server in one terminal:

```sh
shearcast serve -config /srv/shearcast/config.toml \
  -state /srv/shearcast/state.json -cache /srv/shearcast/cache
```

Then plan and process in another:

```sh
shearcast sync -config /srv/shearcast/config.toml \
  -state /srv/shearcast/state.json -cache /srv/shearcast/cache -dry-run
shearcast sync -config /srv/shearcast/config.toml \
  -state /srv/shearcast/state.json -cache /srv/shearcast/cache
```

New processing needs `OPENROUTER_API_KEY`, yt-dlp and FFmpeg. Serving requires
neither model credentials nor those executables. A completed publication retry
needs the verified audio and FFprobe, but no source or model access.

`serve` prints configured feed URLs. A feed returns 404 until its first
publication. The server can start before the publication directory exists.
Its `-state` and `-cache` flags check path separation; it never opens or locks
state. Keep their values consistent with sync.

For channel `example`, subscribe to:

```text
http://192.168.1.20:8080/example/feed.xml
```

If `base_url` includes `/podcasts`, the built-in server serves under that
prefix too. It supports GET, HEAD, conditional requests and byte ranges for
downloads and seeking. Feeds and audio revalidate rather than staying stale
in HTTP caches. It serves only `<slug>/feed.xml`, `<slug>/<video-id>.m4a`,
`<slug>/<video-id>.<sha256>.m4a` replacements, and
`<slug>/<video-id>.<sha256>.chapters.json` chapter documents. It also serves
`subscribe/index.html` and `subscribe/feeds.opml`. There are no
directory listings, symlinks, staging files or private processing records.

## Artifact ownership and recovery

Rendering first writes verified audio and a processing record to the private
cache. Filesystem publication installs complete audio before updating RSS,
using atomic replacement so readers never receive a partly written file.
After publication, state points to the served audio. Only then does Shearcast
delete the managed private audio copy. Its small processing record stays in
the cache for provenance, retry verification and historical estimates.

A crash before the published checkpoint leaves the private render available
for retry. A crash after that checkpoint lets recovery finish removing the
duplicate. Reprocessing builds its replacement privately while the previous
audio remains available. Temporary downloads, replacement renders and the
publication copy require additional space beyond the retained library.

Changed audio uses a new checksum-named enclosure. Chapter JSON uses a
content-named revision too. Shearcast saves their keys before upload, publishes
both before RSS references them, then deletes obsolete revisions after the feed
switch. A failed feed write leaves the previous audio and chapters available.
Recovery checks the actual feed before adopting a pending timeline or deleting
unreferenced files, including when a feed write succeeded but its response was
lost. HTTP LAN feeds carry chapters inline instead of linking external JSON.
See [source chapters](source-chapters.md) for the format choices and app limits.

An explicit `publish -audio` import or custom `render -out` remains
caller-owned. Publication does not remove that file. Retention and manual
removal update RSS before deleting published audio and chapter documents, and keep small records and
exclusions. `channel remove` leaves hosting intact; `channel remove -purge`
removes the channel's feed and managed audio and resumes after interruption.

## Existing web server or public VPS

Use the same filesystem configuration with a public `base_url`, such as
`https://podcasts.example.com`. Either point your existing web server at
`publishing.directory`, or reverse-proxy the built-in server. An existing web
server must support byte ranges and HEAD requests, disable directory listings,
and deny dotfiles, including `.publish-*` temporary files. Serve M4A as
`audio/mp4`, RSS as `application/rss+xml`, and chapter documents as
`application/json+chapters`. Configure cache revalidation so
feed refreshes and explicit episode replacements become visible.

For HTTPS, terminate TLS at your existing web server or reverse proxy. The
built-in server speaks HTTP. A VPS can process and serve from its own disk;
R2 is optional. Both feed and enclosure URLs must be reachable by the client.

## R2

Omit `[publishing]`, or set:

```toml
[publishing]
backend = "r2"
```

Set the R2 variables listed in `.env.example`, including `R2_PUBLIC_BASE_URL`.
Run the same scheduled `sync` command. No `serve` process is needed. The local
managed render currently remains until retention or manual removal, in addition
to the R2 copy. Cloudflare documents `r2.dev` as development-only and
rate-limited; use a connected custom domain for longer-term hosting.

R2 is the supported cloud adapter. S3 protocol compatibility alone does not
establish support for other providers.

## Changing an existing destination

State versions 3 through 6 bind a library to its publishing backend, destination
and base URL. Version 4 added sync outcomes; version 5 adds usage accounting.
Version 6 adds published timeline snapshots and revision cleanup journals.
Publishing commands reject a changed binding before touching the
library. Dry runs check it without saving a binding. Legacy R2 history adopts
the current R2 destination after checking any recorded feed URLs.

Changing config is not a migration. Existing subscriptions, stored feed URLs,
audio URLs and retry paths must move together. There is no automated migration
command yet. Restore the previous settings to continue using an existing
library. To try LAN publishing alongside R2, use a separate config, state and
cache, and subscribe to the new feed. Do not share one publication directory
between independently locked state files.

## Scheduling

Use `shearcast doctor` with the scheduler's user, environment and paths to check
local setup. `shearcast status` reports saved outcomes while sync is running.
See [diagnostics](diagnostics.md) for optional network and publishing probes.

Use absolute config, state and cache paths for every invocation. Relative
`[jev].weights` and `[jev].end_weights` paths resolve beside the config file,
independently of the scheduler's working directory.
The examples below use hourly sync. A sleeping/offline host, delayed captions
or a failed run can delay a new episode. Overlapping writers report a state
lock error rather than running together.

### Linux systemd

Install the following units under `/etc/systemd/system/`. Adjust the user,
executable and working directory for your installation. The `shearcast` user
must already exist and own the data directories. Dependencies must be on the
service's PATH. Keep credentials in `/srv/shearcast/.env` beside the config.

`shearcast-serve.service`:

```ini
[Unit]
Description=Shearcast podcast HTTP server
After=network.target

[Service]
User=shearcast
WorkingDirectory=/opt/shearcast
ExecStart=/usr/local/bin/shearcast serve -config /srv/shearcast/config.toml -state /srv/shearcast/state.json -cache /srv/shearcast/cache
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

`shearcast-sync.service`:

```ini
[Unit]
Description=Shearcast one-shot sync
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
User=shearcast
WorkingDirectory=/opt/shearcast
Environment=PATH=/usr/local/bin:/usr/bin:/bin
ExecStart=/usr/local/bin/shearcast sync -config /srv/shearcast/config.toml -state /srv/shearcast/state.json -cache /srv/shearcast/cache
TimeoutStartSec=infinity
```

`shearcast-sync.timer`:

```ini
[Unit]
Description=Hourly Shearcast sync

[Timer]
OnCalendar=hourly
Persistent=true

[Install]
WantedBy=timers.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now shearcast-serve.service shearcast-sync.timer
journalctl -u shearcast-serve -u shearcast-sync
```

For R2, enable only the sync timer.

### macOS launchd

Replace `YOU` and the executable path, then save this as
`~/Library/LaunchAgents/com.shearcast.sync.plist`. Create the private data
directory and put your config and `.env` there first.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.shearcast.sync</string>
  <key>ProgramArguments</key><array>
    <string>/Users/YOU/bin/shearcast</string><string>sync</string>
    <string>-config</string><string>/Users/YOU/Library/Application Support/shearcast/config.toml</string>
    <string>-state</string><string>/Users/YOU/Library/Application Support/shearcast/state.json</string>
    <string>-cache</string><string>/Users/YOU/Library/Application Support/shearcast/cache</string>
  </array>
  <key>WorkingDirectory</key><string>/Users/YOU/src/shearcast</string>
  <key>EnvironmentVariables</key><dict>
    <key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
  </dict>
  <key>StartInterval</key><integer>3600</integer>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>/Users/YOU/Library/Application Support/shearcast/sync.log</string>
  <key>StandardErrorPath</key><string>/Users/YOU/Library/Application Support/shearcast/sync.log</string>
</dict></plist>
```

For the server, copy this plist to `com.shearcast.serve.plist`, change the label
to `com.shearcast.serve`, the `sync` argument to `serve` and both log filenames
to `serve.log`. Replace `StartInterval` and its integer with
`<key>KeepAlive</key><true/>`. Keep `RunAtLoad`.

```sh
plutil -lint "$HOME/Library/LaunchAgents/com.shearcast.sync.plist"
plutil -lint "$HOME/Library/LaunchAgents/com.shearcast.serve.plist"
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.shearcast.serve.plist"
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.shearcast.sync.plist"
```

For R2, load only the sync plist. These user agents run while that user is
logged in; they do not make a sleeping Mac available to a phone.

## AntennaPod LAN smoke test

Automated tests exercise feeds, full downloads, HEAD, byte ranges and recovery.
A real-device LAN smoke test passed on 2026-09-23 with generated speech served
from macOS. See [the test record](antennapod-lan-test.md) for results and scope.
To repeat the check:

1. Put the Android phone and server on the same LAN. Open the printed feed URL
   in the phone browser and confirm it returns RSS.
2. In AntennaPod, add the podcast using that RSS URL. Confirm the title,
   episode list, artwork and edited duration appear.
3. Stream an episode and seek forward and backward.
4. Publish another episode or update its title, then refresh in AntennaPod.
   Confirm the update appears without duplicate episodes.
5. Download an episode, disconnect the phone from the network, and play and
   seek through the downloaded copy.
6. Reconnect and remove an episode in Shearcast, then refresh. Confirm the served
   RSS no longer contains it and its audio URL returns 404. AntennaPod may retain
   the episode in its history, including when it is no longer in the feed.
   A copy already downloaded to the phone may still play.

Record the server OS, results and any known app/Android versions. The completed
test did not record app or Android versions; the user declined version collection.
Pocket Casts works with the existing public R2
setup but its server-side feed fetcher cannot reach a home-LAN-only URL.
Public-feed Apple Podcasts compatibility is a target; iPhone LAN-only testing
remains deferred. Spotify's arbitrary private-RSS workflow is not supported.

Feeds are unlisted and unauthenticated. A LAN address limits reachability to
networks that can route to it; publicly hosted URLs are accessible to anyone
who has them.
