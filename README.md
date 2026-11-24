# kindle-bonjour

An always-on Home Assistant and Environment Canada weather dashboard for a jailbroken Kindle Paperwhite 3, in English or French.

A Go server draws the dashboard as an e-ink-friendly PNG. A small KUAL extension on the Kindle shows it fullscreen with FBInk and sends taps back to the server.

**Status:** in development. Stages 0 (device checks) and 1 (Go server core) are done. Stage 2 (drawing, taps, Kindle agent) is written and tested in simulation, and still needs a run on the device. Stage 3 (settings page and admin API) is written and tested on the server side. Stage 4 (Docker image and compose) is written and tested with podman. See [`PLAN.md`](PLAN.md) for the design and [`STAGES.md`](STAGES.md) for progress.

## Layout

| Path | What |
|---|---|
| `*.go` | Server: config store, HA client, ECCC weather, versioned long-poll, frame drawing and tap matching. Run with `go run . -data ./data -addr :8080`, then open `http://localhost:8080/settings` to configure it, or `http://localhost:8080/` for the live frame (click to tap). Optional: `HA_URL`/`HA_TOKEN` seed the first config, and `ADMIN_PASSWORD_FILE` or `ADMIN_PASSWORD` protects the settings page. Test with `go test ./...`; after an intended visual change, `go test -run Golden -update` |
| `Dockerfile`, `compose.yaml` | Container image (distroless, non-root, multi-arch) and compose file. `mkdir -p data`, copy `.env.example` to `.env` and set `BIND_IP` to this host's LAN IP, then `docker compose up -d`. `compose.test.yaml` adds a fake HA: `docker compose -f compose.yaml -f compose.test.yaml up -d --build` |
| `kindle/ha-dash/` | KUAL extension. Copy it to `/mnt/us/extensions/` and copy `url.txt.example` to `url.txt` (the server's base URL). **Start** takes over the screen; to leave, hold any screen corner for 3 s and lift, or run `sh bin/stop.sh` over SSH, or reboot. Logs: `/tmp/ha-dash.log`, plus a heartbeat in `/mnt/us/ha-dash.log`. `bin/awaketest.sh` is the Stage 0 stay-awake test. |
| `testdata/eccc/` | Sample ECCC citypage XML and site lists for tests |
| `testdata/golden/` | Expected frames for the golden-image tests |
| `testdata/mock_ha.py` | Fake Home Assistant on `127.0.0.1:18123` (token `test-token`) |
| `web/` | The settings page and the debug page (embedded in the binary) |

Full setup docs come in Stage 7.
