# tvr

Minimal self-hosted IPTV relay built around three concepts:

1. **Channels** — reusable upstream library (`name`, `logo`, `upstream URL`, headers)
2. **EPGs** — global XMLTV sources
3. **Relays** — named lineups with ordered groups of channels, each generating its own M3U/EPG/stream URLs

## Features

- Global channel library (HTTP/HTTPS MPEG-TS and HLS `.m3u8`)
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

### Docker

```bash
docker compose up -d
```

Optionally set `TVR_BASE_URL` to a fixed public origin. If unset, playlist and UI links are derived from the request Host (and TLS state). Set `TVR_TRUST_PROXY=true` only behind a trusted reverse proxy that strips client-supplied `X-Forwarded-*` headers.

## Client setup

1. Add channels under **Channels**, or **Import M3U** under **Relays**.
2. Create/edit a relay: select EPG sources, organize groups, set each channel’s `tvg-id`.
3. Point players at:
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
| `TVR_RELAY_BUFFER_SIZE` | `1024` | Per-viewer queue depth (≥ 8) |
| `TVR_RELAY_IDLE_TIMEOUT` | `30s` | Stop upstream after last viewer leaves |
| `TVR_RELAY_CONN_TIMEOUT` | `10s` | Upstream connect/readiness timeout |
| `TVR_EPG_MAX_BYTES` | `67108864` | Max XMLTV download size |
| `TVR_EPG_DEFAULT_INTERVAL` | `1h` | Default EPG refresh interval (≥ 1m) |

## Supported stream formats

- HTTP(S) MPEG-TS pass-through
- HLS (`.m3u8`) with MPEG-TS segments, concatenated to `video/mp2t`

Not supported: HLS-fMP4/CMAF, DASH, UDP/multicast, transcoding.

## Security note

There is **no authentication**. Designed for a trusted LAN.

## Development

```bash
go test ./...
go test -race ./internal/relay ./internal/store ./internal/epg ./internal/httpapi
go build -o bin/tvr ./cmd/tvr
```

## License

MIT
