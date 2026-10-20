# SRT → Twitch Relay with automatic fallback

Receives an SRT stream (e.g. from your phone), forwards it to Twitch **without
re-encoding**, and switches to a fallback video ("BRB" screen) when the phone's
connection drops. When the phone comes back, it switches back to live
automatically. You can define several of these videos as **scenes** (e.g.
"starting soon", "be right back", "ending") and switch between them by hand. A
web dashboard (and HTTP API) lets you start/stop the stream, pick a scene,
watch a live preview of the phone's stream and change most settings without
touching the config file.

Because nothing is re-encoded, it needs almost no CPU (≈1–3% of one core) and
runs on the cheapest VPS you can find (1 vCPU / 512 MB RAM is plenty).

```
phone ──SRT──► relay ──┬─ live:     ffmpeg -c copy (SRT data)   ──RTMP──► Twitch
                       └─ scene:    ffmpeg -c copy (looped file) ──RTMP──► Twitch
                                   (only one of them runs at a time)
```

## How the switching works

Twitch keeps a broadcast alive when the RTMP connection is **lost**, and ends
it immediately when the encoder **unpublishes** (sends `FCUnpublish`/`deleteStream`,
which is what a normal ffmpeg/OBS shutdown does).

- **Switching** (live ↔ scene, or between scenes) kills the current ffmpeg with `SIGKILL`, so the
  connection just drops without unpublishing, and immediately starts the other
  ffmpeg, which reconnects. Viewers see a short hiccup and Twitch keeps the same
  broadcast running.
- **Stopping** (API `stop`, `docker compose stop`, `switching.fallback_max_duration`)
  ends ffmpeg gracefully, so it unpublishes and Twitch ends the broadcast right away.
  For the live output this is done by closing ffmpeg's input, because ffmpeg
  ignores SIGINT while it reads from a pipe.

Measured locally: fallback → live reconnects in ≈0.6 s and live → fallback in
≈0.1 s (plus the time it takes to notice the drop, see `switching.input_timeout`). That's
well inside Twitch's ~5 s reconnect window.

To keep the live reconnect fast, the relay keeps the stream data since the
phone's last keyframe in memory and gives it to the new ffmpeg first, so it
doesn't have to wait for the next keyframe.

### What happens when the phone loses signal

1. No SRT data for `switching.input_timeout` (default 1.5 s), or the phone
   disconnects cleanly. The relay switches to the fallback scene
   (`switching.fallback_scene`).
2. The phone reconnects and sends data. Once the data has flowed without a gap
   for `switching.live_stable_time` (default 3 s), the relay switches back to
   live. This delay prevents a flapping connection from causing a hiccup every
   few seconds.
3. If the phone stays away longer than `switching.fallback_max_duration` (off by
   default), the stream is ended.

If the phone reconnects with a *new* SRT connection while live is still running
(e.g. a network handover shorter than `switching.input_timeout`), the live output
reconnects right away with the new connection.

---

## Setup

### 1. VPS requirements

- Any Linux VPS with Docker and the Docker Compose plugin
  (`curl -fsSL https://get.docker.com | sh`).
- Open these ports in the provider's firewall and in `ufw`/`firewalld`, if you use one:
  - **UDP 9000**: SRT from the phone
  - **TCP 8080**: control API / web page (or only 443 if you use the reverse proxy below)
- Traffic: your bitrate in plus your bitrate out. At 6 Mbps that's about 2.7 GB in
  plus 2.7 GB out per streamed hour. Check your plan's traffic allowance.

### 2. Get the code onto the VPS

```bash
git clone <your repo> srt-twitch-relay   # or scp/rsync this folder
cd srt-twitch-relay
```

### 3. Configure

```bash
cp config.example.toml config.toml
chmod 600 config.toml    # it holds your stream key
nano config.toml
```

Set at least:

| Setting | What to put |
|---|---|
| `[twitch]` `stream_key` | Twitch dashboard → Settings → Stream → Primary Stream Key |
| `[web]` `api_token` | a long random string: `openssl rand -hex 24` |
| `[srt]` `passphrase` | 10–79 characters; the phone app needs the same one |

Optionally set `rtmp_url` in `[twitch]` to the Twitch ingest server closest to
your VPS (see https://help.twitch.tv/s/twitch-ingest-recommendation). The
default `rtmp://live.twitch.tv/app` picks one automatically.

`docker-compose.yml` mounts `config.toml` into the container as
`/config/config.toml`. Outside Docker, pass the path with
`srt-twitch-relay -config path/to/config.toml` (default: `config.toml` in the
current directory). Without a config file the relay starts with the defaults
and logs a warning.

### 4. Create the scenes

A scene is a video that is streamed instead of the phone. One of them is the
**fallback scene**, shown automatically when the phone drops. The others you
pick by hand in the dashboard, e.g. a "starting soon" screen before you go live
with the phone and a "thanks for watching" screen before you end the stream.
Define them in `config.toml`:

```toml
[switching]
fallback_scene = "brb"   # default: the first scene

[[scenes]]
id = "brb"               # lowercase letters, digits, - and _
name = "Be right back"   # shown in the dashboard
file = "/media/brb.mp4"  # path inside the container

[[scenes]]
id = "starting"
name = "Starting soon"
file = "/media/starting.mp4"
```

Without any `[[scenes]]`, a single scene `fallback` with `/media/fallback.mp4`
is used.

Put an image or video for each scene into `media/` and convert it. The relay
streams the file with `-c copy`, so it must already be H.264 + AAC with regular
keyframes. The helper script takes care of that:

```bash
mkdir -p media
cp ~/brb.png media/                       # your "be right back" image or video

docker compose build
docker compose run --rm --user "$(id -u):$(id -g)" --entrypoint make-fallback \
  relay /media/brb.png /media/brb.mp4 1920x1080 30
```

- Arguments: input, output, resolution, fps. Use your phone's resolution and fps,
  and the same for every scene.
- Images become a 10 s clip with silent audio (`-e DURATION=20` changes the
  length). Videos keep their audio, or get silence if they have none. Every
  scene loops until you switch away from it.
- If a scene's file is missing, the relay streams a plain dark screen instead
  and logs a warning, so it works out of the box. The dashboard marks such
  scenes with "file missing".
- Always convert the clip with `make-fallback`, even if it is already an `.mp4`.
  A raw video (e.g. 4K, high bitrate, no audio) is rejected by Twitch. The relay
  checks the files at startup and logs `scene file is not prepared for Twitch`
  if something is off.
- If you upload a clip from another machine, upload it as a **file**:
  `rclone copyto brb.mp4 remote:path/media/brb.mp4` or `scp`. `rclone copy` with
  a file name as the destination creates a *directory* with that name.
- Scenes are read at startup: run `docker compose restart` after adding or
  replacing one (this ends a running broadcast).

### 5. Start

```bash
docker compose up -d --build
docker compose logs -f
```

You should see `listening for SRT` and `API listening`. The relay is now
**ready but not streaming to Twitch**. Start the stream from the web page or the
API (or set `autostart = true` in `[twitch]`).

### 6. Configure the phone

Any app that can send SRT works (e.g. Larix Broadcaster, Moblin, IRL Pro).
Field names differ between apps, but you need:

| Setting | Value |
|---|---|
| URL | `srt://<VPS-IP>:9000` |
| Mode | Caller (the default in most apps) |
| Passphrase | same as `passphrase` in `[srt]` |
| Stream ID | empty, or the same as `stream_id` in `[srt]` if you set one |
| Latency | 2000 ms is a good start on mobile networks (see tuning) |
| Video codec | **H.264**, not H.265/HEVC. Some apps (e.g. Moblin) default to H.265. Twitch rejects it, so the relay stays on the fallback and shows an error |
| Keyframe interval | **1 s** (2 s max). This affects how fast live comes back |
| Bitrate | constant, ≤ 6000 kbps for Twitch (4000–6000 is typical) |
| Audio | AAC, 48 kHz |

Some apps put the passphrase and stream id into the URL instead:
`srt://<VPS-IP>:9000?passphrase=...&streamid=...`.

---

## Controlling the stream

### Web dashboard

Open `http://<VPS-IP>:8080` on your phone or computer and enter the
`api_token` once (it is stored in that browser). Tip: bookmark
`http://<VPS-IP>:8080/#token=<api_token>` to log in directly; the token is
read from the URL fragment, which is never sent to the server.

- **Broadcast state**: live / fallback / scene / off air, time on air,
  switches, and the last error.
- **Input preview**: a snapshot of the phone's stream about once per keyframe
  (every second with the recommended 1 s keyframe interval), with resolution,
  fps, codec and an audio level meter. The relay only decodes keyframes, and only
  while a browser shows the preview, so it costs almost no CPU. Turn it off with
  the toggle on the card (per browser, saves mobile data) or for everyone with
  `preview = false` in `[web]`.
- **Controls**: Go live, Reconnect and the scene picker. Ending the stream
  needs a press-and-hold, because it ends the Twitch broadcast. On phones the
  controls sit in a bar at the bottom of the screen (the scene button opens a
  list).
- **Scenes**: **Auto** shows the phone and the fallback scene when it drops.
  Picking any other scene puts it on air even if the phone is fine, until you
  pick Auto again. A dot marks what is on air right now. Pick a scene before
  "Go live" to start the stream with it (e.g. "starting soon"). Ending the
  stream resets the picker to Auto.
- **Metrics** with 60 s sparklines: bitrate, round-trip time, packet loss, time
  since the last packet.
- **Phone**: connection details and the `srt://` URL to put into the app.
- **Activity**: the relay's recent log (connections, switches, errors).
- **Settings** (slider icon, top right): see below.

### Changing settings from the web UI

Most settings can be changed in the dashboard, including the stream key, the
Twitch ingest server, the SRT passphrase / stream id and all switching timings.
Each one shows when the change takes effect (immediately, on the next phone
connection, or on the next Twitch connection; after changing the stream key you
can reconnect with one tap). Listen addresses, the SRT latency, the scenes and
the API token can only be changed in the config file.

Changes are saved to `settings_file` in `[web]` (`/data/settings.json`, a
Docker volume) and **override the config file**, also after a restart. Every
overridden setting shows a "Reset to config file" link. Secrets (stream key,
passphrase) are never sent back to the browser, only whether they are set.
Without an `api_token` the stream key, ingest server and passphrase can't be
changed from the web UI.

Because the stream key can be set in the dashboard, `stream_key` is optional in
the config file: without it the relay starts, and "Go live" asks you to set the
key first.

### HTTP API

Authenticate with `Authorization: Bearer <api_token>` (or `?token=<api_token>`).
Every action returns the new status.

| Method & path | Effect |
|---|---|
| `GET /api/status` | current state (see below). `?preview=1` keeps the preview snapshots coming |
| `GET /api/preview.jpg` | latest preview snapshot (JPEG, `204` if there is none). Supports `ETag` |
| `GET /api/logs?after=<seq>` | recent log entries after a sequence number |
| `GET /api/settings` | all settings with their current/config file values (secrets masked) |
| `PATCH /api/settings` | change settings: `{"switching.input_timeout": "2s"}`; `null` resets a setting to its config file value |
| `POST /api/start` | go live on Twitch (fallback scene if the phone isn't sending yet) |
| `POST /api/stop` | end the broadcast (graceful unpublish) and reset the scene to automatic |
| `POST /api/scene` | pick a scene: `{"scene": "brb"}` shows it even if the phone is fine; `{"scene": ""}` goes back to automatic |
| `POST /api/fallback/force` | shortcut: pick the fallback scene (manual BRB) |
| `POST /api/fallback/release` | shortcut: back to automatic |
| `POST /api/reconnect` | drop and re-open the Twitch connection without ending the broadcast. Only needed if Twitch looks stuck while the relay shows `live` |
| `GET /healthz` | liveness check, no auth |

```bash
TOKEN=...; HOST=http://<VPS-IP>:8080
curl -X POST -H "Authorization: Bearer $TOKEN" -d '{"scene": "starting"}' $HOST/api/scene
curl -X POST -H "Authorization: Bearer $TOKEN" $HOST/api/start
curl -X POST -H "Authorization: Bearer $TOKEN" -d '{"scene": ""}' $HOST/api/scene
curl -H "Authorization: Bearer $TOKEN" $HOST/api/status
curl -X POST -H "Authorization: Bearer $TOKEN" $HOST/api/stop
```

Example status:

```json
{
  "output": {
    "running": true,
    "mode": "live",
    "mode_since": "2026-10-13T06:12:51.72Z",
    "ffmpeg_running": true,
    "ffmpeg_pid": 128,
    "switches": 3,
    "restarts": 0
  },
  "input": {
    "connected": true,
    "active": true,
    "remote_addr": "203.0.113.7:50591",
    "connected_at": "2026-10-13T06:12:48.12Z",
    "last_data_ms_ago": 12,
    "video_codec": "h264",
    "bitrate_kbps": 5870,
    "rtt_ms": 48,
    "packets_lost": 31,
    "packets_dropped": 0,
    "keyframe_cache_bytes": 412580
  }
}
```

`mode` is `stopped`, `live` or `fallback`. In `fallback`, `scene` is the id of
the scene on air. `picked_scene` is set while a scene is picked by hand (absent
= automatic). `relay.scenes` lists the configured scenes and
`relay.fallback_scene` the automatic one. `running: true` with
`ffmpeg_running: false` means the relay is waiting before it retries a
connection that failed (see `last_error`).

### Securing the API (recommended)

The API token travels in plain text over plain HTTP. If you control the stream
from mobile networks or public Wi-Fi, put HTTPS in front of it, for example
with [Caddy](https://caddyserver.com). Point a (sub)domain at the VPS, then in
`docker-compose.yml` change the API port mapping to `"127.0.0.1:8080:8080"` and run Caddy
with this Caddyfile:

```
relay.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

---

## Configuration reference

All settings live in a TOML file (`config.toml`, see `config.example.toml`),
grouped into sections. Durations are strings in Go syntax: `"500ms"`, `"3s"`,
`"10m"`. Unknown keys are an error, so typos don't go unnoticed. Settings marked
UI can also be changed in the dashboard.

| Setting | Default | Description |
|---|---|---|
| **`[twitch]`** | | |
| `stream_key` (UI) | *(empty)* | Your stream key, required to go live. Append `?bandwidthtest=true` to test without going public |
| `rtmp_url` (UI) | `rtmp://live.twitch.tv/app` | Ingest server, without the key |
| `autostart` (UI) | `false` | Start streaming to Twitch as soon as the relay starts |
| **`[srt]`** | | |
| `passphrase` (UI) | *(empty)* | Require SRT encryption with this passphrase (10–79 chars) |
| `stream_id` (UI) | *(empty)* | If set, only accept SRT connections with exactly this stream id |
| `listen_addr` | `:9000` | SRT listen address (UDP) |
| `latency` | `2s` | SRT receive latency. The higher of this and the phone's setting is used |
| **`[switching]`** | | |
| `fallback_scene` (UI) | first scene | Scene shown when the phone drops |
| `input_timeout` (UI) | `1500ms` | No data for this long → switch to the fallback scene |
| `live_stable_time` (UI) | `3s` | Data must flow this long before switching back to live |
| `fallback_max_duration` (UI) | `0` (off) | End the stream after the phone has been gone this long, e.g. `"15m"`. Not while a scene is picked by hand |
| **`[web]`** | | |
| `api_token` | *(empty = no auth!)* | Token for the API / web page |
| `listen_addr` | `:8080` | API / web page address (the Docker healthcheck expects port 8080) |
| `preview` (UI) | `true` | Allow the input preview in the web UI |
| `settings_file` | `/data/settings.json` | Where changes made in the web UI are saved. They override the config file |
| **`[ffmpeg]`** | | |
| `path` | `ffmpeg` | ffmpeg binary |
| `analyze_duration` (UI) | `1s` | How much input ffmpeg analyses before connecting to Twitch |
| `log_level` (UI) | `warning` | ffmpeg log level (`error`, `warning`, `info`, `debug`) |
| **`[log]`** | | |
| `level` (UI) | `info` | Relay log level |
| **`[[scenes]]`** | `fallback` → `/media/fallback.mp4` | One block per scene, see [Create the scenes](#4-create-the-scenes) |
| `id` | | Unique, lowercase letters, digits, `-` and `_` |
| `name` | the id | Shown in the dashboard |
| `file` | | Clip to stream (H.264 + AAC, made with `make-fallback`). Relative paths are relative to the config file |

### Upgrading from `.env`

Earlier versions were configured with environment variables. They are no
longer read; the relay logs a warning for each one it still finds. Move the
values into `config.toml` (e.g. `TWITCH_STREAM_KEY` → `stream_key` in
`[twitch]`, `FALLBACK_FILE` → a `[[scenes]]` block), replace `env_file: .env`
in your `docker-compose.yml` with the `config.toml` mount, and delete `.env`.
Changes made in the web UI (`/data/settings.json`) are migrated automatically.

## Tuning

- **Stream delay.** The relay itself adds nothing on top of the SRT latency
  (measured: data reaches Twitch within ~50 ms of leaving the SRT buffer). The
  delay a viewer sees is roughly phone encoder (~0.3 s) + SRT latency + Twitch
  (2–4 s in low latency mode, much more in normal mode). For ~4–5 s in total:
  - Twitch dashboard → Settings → Stream → **Latency mode: Low latency**.
  - Lower the SRT latency on **both** sides: the higher of `srt.latency` and the
    phone's setting is used, so lowering only the phone changes nothing. About
    4× the RTT shown on the web page is the minimum. 1000 ms works on decent
    LTE/5G; go back up if you see dropped packets.
  - Avoid interruptions: every reconnect or switch makes the Twitch player
    stall, and viewers who were watching stay behind by the stall time. Refresh
    the player to get back to the live edge. Measure the delay with a clock
    in front of the camera and a freshly loaded player.
- **How fast does the fallback appear?** About `SRT latency` + `input_timeout`
  after the phone loses signal. Lowering `input_timeout` reacts faster but
  switches on shorter dropouts. Every switch costs a short hiccup, so don't go
  far below 1 s.
- **SRT latency** is a buffer that rides out packet loss. Mobile: 1500–3000 ms.
  More latency means fewer glitches but more delay.
- **Flapping connection** (e.g. on a train): raise `live_stable_time` to 5–10 s
  so the relay stays on the fallback until the connection has recovered.
- **Slow switch back to live:** set the phone's keyframe interval to 1 s.
  `keyframe_cache_bytes` in the status should be non-zero while the phone is
  sending. If it stays at 0, keyframe detection doesn't work for your stream and
  each switch to live waits for the next keyframe. Try raising
  `analyze_duration` in `[ffmpeg]` to `"2s"` if live then fails to start.

## Testing without going public

- Append `?bandwidthtest=true` to your stream key
  (`stream_key = "live_xxx?bandwidthtest=true"`). Twitch receives the stream
  but doesn't publish it; check it in [Twitch Inspector](https://inspector.twitch.tv).
- Simulate a phone from any machine with ffmpeg:

  ```bash
  ffmpeg -re -f lavfi -i testsrc2=s=1280x720:r=30 -f lavfi -i sine=r=48000 \
    -c:v libx264 -preset veryfast -tune zerolatency -g 30 -b:v 3M -c:a aac -ar 48000 \
    -f mpegts "srt://<VPS-IP>:9000?pkt_size=1316&passphrase=<srt.passphrase>"
  ```

  Ctrl+C simulates a dropout; starting it again brings back live.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Phone can't connect at all | UDP 9000 blocked (provider firewall!), wrong IP/port |
| Log: `rejected SRT connection: wrong passphrase` / `no passphrase set on the sender` | Passphrase mismatch between phone and the config file |
| Log: `rejected SRT connection: wrong stream id` | `srt.stream_id` doesn't match what the app sends. Unset it to see what the app sends (it's logged) |
| `last_error` mentions ffmpeg exited, repeating | Wrong stream key, Twitch unreachable, or a scene file isn't H.264/AAC. Run `docker compose logs` and look at the ffmpeg lines |
| Live works, but switching back from fallback takes long | Keyframe interval too long. Set 1 s in the app |
| Live never shows up, Twitch shows the broken NotLikeThis screen, or the live ffmpeg exits with `Broken pipe` / `Connection reset by peer` about 1 s after starting | The phone sends H.265/HEVC. Set the video codec in the app to H.264. The relay now detects this (`video_codec: "hevc"` in the status, error in the log) and stays on the fallback |
| Log: `scene file is a directory, not a file` | e.g. `media/brb.mp4` is a folder, usually from `rclone copy`. Delete it and upload with `rclone copyto` or `scp` |
| Log: `scene file is not prepared for Twitch` | Convert the clip with `make-fallback` (step 4) |
| Relay exits with `invalid configuration` | A typo or wrong value in `config.toml`; the log names the key |
| Stream ends during `docker compose down`/restart | Expected: shutting down the relay ends the broadcast gracefully |

## Development

```bash
go test ./...       # needs ffmpeg in PATH for the MPEG-TS test
go build .
```

CI (`.github/workflows/ci.yml`) runs gofmt, `go vet`, `go test -race`, golangci-lint
and a multi-arch Docker build on every push and pull request. Pushes to `main` and
`v*` tags are published to `ghcr.io/l3-n0x/cheaprelay` (`edge`, `<version>`, `sha-…`)
by `.github/workflows/publish.yml`.

Code overview:

| File | Purpose |
|---|---|
| `main.go` | wiring, signal handling |
| `config.go` | config file (TOML) and scenes |
| `ingest.go` | SRT listener (datarhei/gosrt), one active publisher, feeds the live ffmpeg |
| `ts.go` | minimal MPEG-TS parsing for the keyframe cache |
| `ffmpeg.go` | ffmpeg processes: `Kill` (drop connection) vs `Stop` (unpublish) |
| `supervisor.go` | state machine deciding live / scene / stopped |
| `settings.go` | settings changed from the web UI, saved to `web.settings_file` |
| `preview.go` | keyframe snapshots + audio level for the web UI |
| `logs.go` | in-memory log buffer for the activity feed |
| `api.go`, `web/` | HTTP API and the dashboard (plain HTML/CSS/JS, embedded into the binary; Geist font under the OFL) |
| `scripts/make-fallback.sh` | converts an image/video into a scene clip |
