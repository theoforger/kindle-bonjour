# kindle-bonjour

An always-on Home Assistant and Environment Canada weather dashboard for a jailbroken Kindle Paperwhite 3, in English or French.

A Go server draws the dashboard as an e-ink-friendly PNG. A small KUAL extension on the Kindle shows it fullscreen with FBInk and sends taps back to the server.

**Status:** in development. Stages 0 (device checks) and 1 (Go server core) are done. See [`PLAN.md`](PLAN.md) for the design and [`STAGES.md`](STAGES.md) for progress.

## Layout

| Path | What |
|---|---|
| `*.go` | Server: config store, HA client, ECCC weather, versioned long-poll. Run with `go run . -data ./data -addr :8080`, and test with `go test ./...` |
| `kindle/ha-dash/` | KUAL extension. Copy it to `/mnt/us/extensions/` and copy `url.txt.example` to `url.txt`. The launch scripts are still the browser-based prototype until Stage 2. `bin/awaketest.sh` is the stay-awake test. |
| `testdata/eccc/` | Sample ECCC citypage XML for tests |
| `testdata/mock_ha.py` | Fake Home Assistant on `127.0.0.1:18123` (token `test-token`) |

Full setup docs come in Stage 7.
