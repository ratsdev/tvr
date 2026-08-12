# tvr

Minimal self-hosted IPTV relay built around three concepts:

1. **Channels** — reusable upstream library (`name`, `logo`, `upstream URL`, headers, optional transcoding)
2. **EPGs** — global XMLTV sources
3. **Relays** — named lineups with ordered groups of channels, each generating its own M3U/EPG/stream URLs

## Features

- Global channel library (HTTP/HTTPS MPEG-TS and HLS `.m3u8`)
- Optional per-channel ffmpeg transcoding to H.264/AAC MPEG-TS
- Global transcoding profile under **Settings**
- Relays with drag/drop ordered groups and channel memberships
- Per-membership channel number, EPG source, and `tvg-id`
- Shared upstream session per global channel across relays
- Import M3U as a new relay (reuse channels by upstream URL)
- Per-relay XMLTV cache filtered by membership mappings
- Single Go binary, SQLite, Docker-friendly

## Quick start

```bash
go run ./cmd/tvr
```

Open `http://localhost:8080`.

Binary installs need a system `ffmpeg` on `PATH` (or set `TVR_FFMPEG_PATH`) if you enable channel transcoding. Docker/GHCR images include ffmpeg.

### Docker

```bash
docker compose up -d
```

Optionally set `TVR_BASE_URL` to a fixed public origin. If unset, playlist and UI links are derived from the request Host (and TLS state). Set `TVR_TRUST_PROXY=true` only behind a trusted reverse proxy that strips client-supplied `X-Forwarded-*` headers.

## Client setup

1. Add channels under **Channels**, or **Import M3U** under **Relays**.
2. Optionally enable **Transcode with global profile** on channels that need re-encoding; tune the profile under **Settings**.
3. Create/edit a relay: select EPG sources, organize groups, set each channel’s `tvg-id`.
4. Point players at:
   - Playlist: `http://<host>:8080/r/<slug>/playlist.m3u`
   - EPG: `http://<host>:8080/r/<slug>/epg.xml`
   - Stream: `http://<host>:8080/stream/<channel-uuid>`

## Configuration

| Variable | Default | Description |
|---|---|---|
| `TVR_LISTEN` | `:8080` | HTTP listen address |
| `TVR_BASE_URL` | _(empty)_ | Fixed absolute HTTP(S) origin for playlist links (optional path prefix; no query/fragment); if unset, derived from each request |
| `TVR_TRUST_PROXY` | `false` | When true, request-derived URLs may use `X-Forwarded-Proto` / `X-Forwarded-Host` |
| `TVR_DATA_DIR` | `./data` | Data directory for DB + EPG caches |
| `TVR_DATABASE` | `$TVR_DATA_DIR/tvr.db` | SQLite path |
| `TVR_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `TVR_FFMPEG_PATH` | `ffmpeg` | ffmpeg executable used for transcoded channels |
| `TVR_RELAY_BUFFER_SIZE` | `1024` | Per-viewer queue depth (≥ 8) |
| `TVR_RELAY_IDLE_TIMEOUT` | `30s` | Stop upstream after last viewer leaves |
| `TVR_RELAY_CONN_TIMEOUT` | `10s` | Upstream connect/readiness timeout for pass-through channels |
| `TVR_EPG_MAX_BYTES` | `67108864` | Max XMLTV download size |
| `TVR_EPG_DEFAULT_INTERVAL` | `1h` | Default EPG refresh interval (≥ 1m) |

Editable transcoder profile fields (CRF, preset, audio bitrate, max height, startup timeout) live in SQLite and are managed from the **Settings** page.

## Supported stream formats

- HTTP(S) MPEG-TS pass-through
- HLS (`.m3u8`) with MPEG-TS segments, concatenated to `video/mp2t`
- Optional per-channel ffmpeg re-encode to H.264/AAC MPEG-TS (including ordinary AES-128 HLS)

Not supported: HLS-fMP4/CMAF, DASH, UDP/multicast, SAMPLE-AES variants, hardware encoders, per-channel encode profiles.

Transcoding uses one ffmpeg process per active shared channel session. Enabling/changing transcoding disconnects current viewers for that channel so the next tune-in starts a fresh producer.

## Security note

There is **no authentication**. Designed for a trusted LAN. The Settings API is therefore also unauthenticated; keep tvr off the public internet.

## Development

```bash
go test ./...
go test -race ./internal/relay ./internal/store ./internal/epg ./internal/httpapi
go build -o bin/tvr ./cmd/tvr
```

## License

MIT
