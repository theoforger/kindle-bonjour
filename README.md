# kindle-bonjour

An always-on Home Assistant and Environment Canada weather dashboard for a jailbroken Kindle Paperwhite 3, in English or French.

A Go server draws the dashboard as an e-ink-friendly PNG. A small KUAL extension on the Kindle shows it fullscreen with FBInk and sends taps back to the server.

**Status:** in development. Stages 0 (device checks) and 1 (Go server core) are done. Stage 2 (drawing, taps, Kindle agent) is written and tested in simulation, and still needs a run on the device. See [`PLAN.md`](PLAN.md) for the design and [`STAGES.md`](STAGES.md) for progress.

## Layout

| Path | What |
|---|---|
| `*.go` | Server: config store, HA client, ECCC weather, versioned long-poll, frame drawing and tap matching. Run with `go run . -data ./data -addr :8080`, then open `http://localhost:8080/` for the live frame (click to tap). Test with `go test ./...`; after an intended visual change, `go test -run Golden -update` |
| `kindle/ha-dash/` | KUAL extension. Copy it to `/mnt/us/extensions/` and copy `url.txt.example` to `url.txt` (the server's base URL). **Start** takes over the screen; to leave, hold any screen corner for 3 s and lift, or run `sh bin/stop.sh` over SSH, or reboot. Logs: `/tmp/ha-dash.log`, plus a heartbeat in `/mnt/us/ha-dash.log`. `bin/awaketest.sh` is the Stage 0 stay-awake test. |
| `testdata/eccc/` | Sample ECCC citypage XML for tests |
| `testdata/golden/` | Expected frames for the golden-image tests |
| `testdata/mock_ha.py` | Fake Home Assistant on `127.0.0.1:18123` (token `test-token`) |

Full setup docs come in Stage 7.
