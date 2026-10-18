# SRT → Twitch Relay with automatic fallback

Receives an SRT stream (e.g. from your phone), forwards it to Twitch **without
re-encoding**, and switches to a fallback video ("BRB" screen) when the phone's
connection drops. When the phone comes back, it switches back to live
automatically. A small HTTP API + web page lets you start/stop the stream and
force the fallback.

Because nothing is re-encoded, it needs almost no CPU (≈1–3% of one core) and
runs on the cheapest VPS you can find (1 vCPU / 512 MB RAM is plenty).

```
phone ──SRT──► relay ──┬─ live:     ffmpeg -c copy (SRT data)   ──RTMP──► Twitch
                       └─ fallback: ffmpeg -c copy (looped file) ──RTMP──► Twitch
                                   (only one of them runs at a time)
```

## How the switching works

Twitch keeps a broadcast alive when the RTMP connection is **lost**, and ends
it immediately when the encoder **unpublishes** (sends `FCUnpublish`/`deleteStream`,
which is what a normal ffmpeg/OBS shutdown does).

- **Switching** (live ↔ fallback) kills the current ffmpeg with `SIGKILL`, so the
  connection just drops without unpublishing, and immediately starts the other
  ffmpeg, which reconnects. Viewers see a short hiccup and Twitch keeps the same
  broadcast running.
- **Stopping** (API `stop`, `docker compose stop`, `FALLBACK_MAX_DURATION`)
  ends ffmpeg gracefully, so it unpublishes and Twitch ends the broadcast right away.
  For the live output this is done by closing ffmpeg's input, because ffmpeg
  ignores SIGINT while it reads from a pipe.

Measured locally: fallback → live reconnects in ≈0.6 s and live → fallback in
≈0.1 s (plus the time it takes to notice the drop, see `INPUT_TIMEOUT`). That's
well inside Twitch's ~5 s reconnect window.

To keep the live reconnect fast, the relay keeps the stream data since the
phone's last keyframe in memory and gives it to the new ffmpeg first, so it
doesn't have to wait for the next keyframe.

### What happens when the phone loses signal

1. No SRT data for `INPUT_TIMEOUT` (default 1.5 s), or the phone disconnects
   cleanly. The relay switches to the fallback video.
2. The phone reconnects and sends data. Once the data has flowed without a gap
   for `LIVE_STABLE_TIME` (default 3 s), the relay switches back to live. This
   delay prevents a flapping connection from causing a hiccup every few seconds.
3. If the phone stays away longer than `FALLBACK_MAX_DURATION` (off by default),
   the stream is ended.

If the phone reconnects with a *new* SRT connection while live is still running
(e.g. a network handover shorter than `INPUT_TIMEOUT`), the live output
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
cp .env.example .env
nano .env
```

Set at least:

| Variable | What to put |
|---|---|
| `TWITCH_STREAM_KEY` | Twitch dashboard → Settings → Stream → Primary Stream Key |
| `API_TOKEN` | a long random string: `openssl rand -hex 24` |
| `SRT_PASSPHRASE` | 10–79 characters; the phone app needs the same one |

Optionally set `TWITCH_RTMP_URL` to the Twitch ingest server closest to your VPS
(see https://help.twitch.tv/s/twitch-ingest-recommendation). The default
`rtmp://live.twitch.tv/app` picks one automatically.

### 4. Create the fallback clip

Put an image or video into `media/` and convert it. The relay streams the file
with `-c copy`, so it must already be H.264 + AAC with regular keyframes. The
helper script takes care of that:

```bash
mkdir -p media
cp ~/brb.png media/                       # your "be right back" image or video

docker compose build
docker compose run --rm --user "$(id -u):$(id -g)" --entrypoint make-fallback \
  relay /media/brb.png /media/fallback.mp4 1920x1080 30
```

- Arguments: input, output, resolution, fps. Use your phone's resolution and fps.
- Images become a 10 s clip with silent audio (`-e DURATION=20` changes the
  length). Videos keep their audio, or get silence if they have none.
- Without a `media/fallback.mp4`, the relay generates a plain dark screen at
  startup and logs a warning, so it works out of the box.
- Always convert the clip with `make-fallback`, even if it is already an `.mp4`.
  A raw video (e.g. 4K, high bitrate, no audio) is rejected by Twitch. The relay
  checks the file at startup and logs `fallback file is not prepared for Twitch`
  if something is off.
- If you upload the clip from another machine, upload it as a **file**:
  `rclone copyto brb.mp4 remote:path/media/brb.mp4` or `scp`. `rclone copy` with
  a file name as the destination creates a *directory* with that name.
- The fallback file is read at startup: run `docker compose restart` after
  replacing it (this ends a running broadcast).

### 5. Start

```bash
docker compose up -d --build
docker compose logs -f
```

You should see `listening for SRT` and `API listening`. The relay is now
**ready but not streaming to Twitch**. Start the stream from the web page or the
API (or set `AUTOSTART=true`).

### 6. Configure the phone

Any app that can send SRT works (e.g. Larix Broadcaster, Moblin, IRL Pro).
Field names differ between apps, but you need:

| Setting | Value |
|---|---|
| URL | `srt://<VPS-IP>:9000` |
| Mode | Caller (the default in most apps) |
| Passphrase | same as `SRT_PASSPHRASE` |
| Stream ID | empty, or the same as `SRT_STREAM_ID` if you set one |
| Latency | 2000 ms is a good start on mobile networks (see tuning) |
| Video codec | **H.264**, not H.265/HEVC. Some apps (e.g. Moblin) default to H.265. Twitch rejects it, so the relay stays on the fallback and shows an error |
| Keyframe interval | **1 s** (2 s max). This affects how fast live comes back |
| Bitrate | constant, ≤ 6000 kbps for Twitch (4000–6000 is typical) |
| Audio | AAC, 48 kHz |

Some apps put the passphrase and stream id into the URL instead:
`srt://<VPS-IP>:9000?passphrase=...&streamid=...`.

---

## Controlling the stream

### Web page

Open `http://<VPS-IP>:8080` on your phone, paste the `API_TOKEN` once (it is
stored in that browser), and you get the current state plus the
Start / Stop / Force fallback / Reconnect buttons. Stop requires a second tap,
because it ends the Twitch broadcast.

### HTTP API

Authenticate with `Authorization: Bearer <API_TOKEN>` (or `?token=<API_TOKEN>`).
Every action returns the new status.

| Method & path | Effect |
|---|---|
| `GET /api/status` | current state (see below) |
| `POST /api/start` | go live on Twitch (fallback if the phone isn't sending yet) |
| `POST /api/stop` | end the broadcast (graceful unpublish) |
| `POST /api/fallback/force` | show the fallback even if the phone is fine (manual BRB) |
| `POST /api/fallback/release` | back to automatic mode |
| `POST /api/reconnect` | drop and re-open the Twitch connection without ending the broadcast. Only needed if Twitch looks stuck while the relay shows `live` |
| `GET /healthz` | liveness check, no auth |

```bash
TOKEN=...; HOST=http://<VPS-IP>:8080
curl -X POST -H "Authorization: Bearer $TOKEN" $HOST/api/start
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
    "forced_fallback": false,
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

`mode` is `stopped`, `live` or `fallback`. `running: true` with
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

All settings are environment variables (in `.env`). Durations use Go syntax:
`500ms`, `3s`, `10m`.

| Variable | Default | Description |
|---|---|---|
| `TWITCH_STREAM_KEY` | *(required)* | Your stream key. Append `?bandwidthtest=true` to test without going public |
| `TWITCH_RTMP_URL` | `rtmp://live.twitch.tv/app` | Ingest server, without the key |
| `API_TOKEN` | *(empty = no auth!)* | Token for the API / web page |
| `SRT_PASSPHRASE` | *(empty)* | Require SRT encryption with this passphrase (10–79 chars) |
| `SRT_STREAM_ID` | *(empty)* | If set, only accept SRT connections with exactly this stream id |
| `SRT_LISTEN_ADDR` | `:9000` | SRT listen address (UDP) |
| `SRT_LATENCY` | `2s` | SRT receive latency. The higher of this and the phone's setting is used |
| `INPUT_TIMEOUT` | `1500ms` | No data for this long → switch to fallback |
| `LIVE_STABLE_TIME` | `3s` | Data must flow this long before switching back from fallback to live |
| `FALLBACK_MAX_DURATION` | `0` (off) | End the stream after the phone has been gone this long, e.g. `15m` |
| `FALLBACK_FILE` | `/media/fallback.mp4` | Fallback clip inside the container (H.264 + AAC) |
| `AUTOSTART` | `false` | Start streaming to Twitch as soon as the container starts |
| `API_LISTEN_ADDR` | `:8080` | API / web page address (the Docker healthcheck expects port 8080) |
| `FFMPEG_ANALYZE_DURATION` | `1s` | How much input ffmpeg analyses before connecting to Twitch |
| `FFMPEG_LOGLEVEL` | `warning` | ffmpeg log level (`error`, `warning`, `info`, `debug`) |
| `LOG_LEVEL` | `info` | Relay log level |

## Tuning

- **Stream delay.** The relay itself adds nothing on top of the SRT latency
  (measured: data reaches Twitch within ~50 ms of leaving the SRT buffer). The
  delay a viewer sees is roughly phone encoder (~0.3 s) + SRT latency + Twitch
  (2–4 s in low latency mode, much more in normal mode). For ~4–5 s in total:
  - Twitch dashboard → Settings → Stream → **Latency mode: Low latency**.
  - Lower the SRT latency on **both** sides: the higher of `SRT_LATENCY` and the
    phone's setting is used, so lowering only the phone changes nothing. About
    4× the RTT shown on the web page is the minimum. 1000 ms works on decent
    LTE/5G; go back up if you see dropped packets.
  - Avoid interruptions: every reconnect or switch makes the Twitch player
    stall, and viewers who were watching stay behind by the stall time. Refresh
    the player to get back to the live edge. Measure the delay with a clock
    in front of the camera and a freshly loaded player.
- **How fast does the fallback appear?** About `SRT latency` + `INPUT_TIMEOUT`
  after the phone loses signal. Lowering `INPUT_TIMEOUT` reacts faster but
  switches on shorter dropouts. Every switch costs a short hiccup, so don't go
  far below 1 s.
- **SRT latency** is a buffer that rides out packet loss. Mobile: 1500–3000 ms.
  More latency means fewer glitches but more delay.
- **Flapping connection** (e.g. on a train): raise `LIVE_STABLE_TIME` to 5–10 s
  so the relay stays on the fallback until the connection has recovered.
- **Slow switch back to live:** set the phone's keyframe interval to 1 s.
  `keyframe_cache_bytes` in the status should be non-zero while the phone is
  sending. If it stays at 0, keyframe detection doesn't work for your stream and
  each switch to live waits for the next keyframe. Try raising
  `FFMPEG_ANALYZE_DURATION` to `2s` if live then fails to start.

## Testing without going public

- Append `?bandwidthtest=true` to your stream key in `.env`
  (`TWITCH_STREAM_KEY=live_xxx?bandwidthtest=true`). Twitch receives the stream
  but doesn't publish it; check it in [Twitch Inspector](https://inspector.twitch.tv).
- Simulate a phone from any machine with ffmpeg:

  ```bash
  ffmpeg -re -f lavfi -i testsrc2=s=1280x720:r=30 -f lavfi -i sine=r=48000 \
    -c:v libx264 -preset veryfast -tune zerolatency -g 30 -b:v 3M -c:a aac -ar 48000 \
    -f mpegts "srt://<VPS-IP>:9000?pkt_size=1316&passphrase=<SRT_PASSPHRASE>"
  ```

  Ctrl+C simulates a dropout; starting it again brings back live.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Phone can't connect at all | UDP 9000 blocked (provider firewall!), wrong IP/port |
| Log: `rejected SRT connection: wrong passphrase` / `no passphrase set on the sender` | Passphrase mismatch between phone and `.env` |
| Log: `rejected SRT connection: wrong stream id` | `SRT_STREAM_ID` doesn't match what the app sends. Unset it to see what the app sends (it's logged) |
| `last_error` mentions ffmpeg exited, repeating | Wrong stream key, Twitch unreachable, or the fallback file isn't H.264/AAC. Run `docker compose logs` and look at the ffmpeg lines |
| Live works, but switching back from fallback takes long | Keyframe interval too long. Set 1 s in the app |
| Live never shows up, Twitch shows the broken NotLikeThis screen, or the live ffmpeg exits with `Broken pipe` / `Connection reset by peer` about 1 s after starting | The phone sends H.265/HEVC. Set the video codec in the app to H.264. The relay now detects this (`video_codec: "hevc"` in the status, error in the log) and stays on the fallback |
| Log: `fallback path is a directory, not a file` | `media/fallback.mp4` is a folder, usually from `rclone copy`. Delete it and upload with `rclone copyto` or `scp` |
| Log: `fallback file is not prepared for Twitch` | Convert the clip with `make-fallback` (step 4) |
| Stream ends during `docker compose down`/restart | Expected: shutting down the relay ends the broadcast gracefully |

## Development

```bash
go test ./...       # needs ffmpeg in PATH for the MPEG-TS test
go build .
```

Code overview:

| File | Purpose |
|---|---|
| `main.go` | wiring, signal handling |
| `config.go` | environment variables |
| `ingest.go` | SRT listener (datarhei/gosrt), one active publisher, feeds the live ffmpeg |
| `ts.go` | minimal MPEG-TS parsing for the keyframe cache |
| `ffmpeg.go` | ffmpeg processes: `Kill` (drop connection) vs `Stop` (unpublish) |
| `supervisor.go` | state machine deciding live / fallback / stopped |
| `api.go`, `ui.html` | HTTP API and the control page |
| `scripts/make-fallback.sh` | converts an image/video into a fallback clip |
