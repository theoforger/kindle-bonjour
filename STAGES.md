# Build stages

This breaks the build order in `PLAN.md` §7 into stages. The design is **server-rendered PNG frames shown with FBInk** (PLAN revision 2). Stage 0 ruled out the stock browser.

---

## Stage 0: Device checks
*No server code.*

### Browser findings (done, 2026-11-23, PW3 5.16.2.1.1)
- **Launch works:** `lipc-set-prop com.lab126.appmgrd start "app://com.lab126.browser?view=$URL"`. `com.lab126.browser` has no `open` property.
- **The browser is a "mesquite" web app.** KPPMainApp draws its toolbars, and the browser sets them up itself on load (`pillowMessaging::configure pillow request`).
- **No fullscreen from outside:** writing to `com.lab126.chromebar` (`configureChrome`, `lastChromeConfig` + `replayChromeConfig`) returns rc=0, but nothing changes on screen.
- **Spinner on every request:** each XHR starts and stops the status-bar spinner (`activityIndicator`), so the browser redraws part of the screen on every poll.
- **Unexplained close:** the browser closed once on its own.
- **Decision:** switch to the FBInk/PNG design.

### Device facts
- **Kernel `3.0.35-lab126`:** too old for Go 1.24+ binaries, which need 3.2 or later. The agent stays in shell; any helper binary would need Go ≤1.23 or C.
- **Tools:** `/usr/bin/curl`, `/usr/bin/wget`, `/usr/bin/od` and `/usr/bin/awk` are present. FBInk is at **`/mnt/us/libkh/bin/fbink`** (another copy at `/mnt/us/koreader/fbink`), not on the PATH.
- **Interface services:** `framework` and `lab126_gui` are running; `lab126_gui_monitor` and the `*_setup` jobs are stopped or waiting.
- **Touchscreen:** `cyttsp4_mt` on **`/dev/input/event1`**. Multitouch protocol B:
  - `ABS_MT_SLOT` (47), `ABS_MT_POSITION_X` (53), `ABS_MT_POSITION_Y` (54), `ABS_MT_TRACKING_ID` (57);
  - `BTN_TOUCH` (330) and `BTN_TOOL_FINGER` (325);
  - no single-touch `ABS_X`/`ABS_Y`.
- **Power button:** `max77696-onkey` on `/dev/input/event0` (`KEY_POWER` 116). It could serve as a second exit.
- **Decoding:** `od -An -td4 -w16` gives `sec usec type+code*65536 value` (32-bit `input_event`).
- **Buffering caveat:** when `od` is piped into `awk`, it buffers its output, so taps arrive late. `touch.sh` needs a fix for this in Stage 2, for example reading in `dd bs=16` chunks or a small C or Go 1.23 helper.

### Remaining checks for the FBInk design
- [x] **Tools:** checked (see Device facts).
- [x] **Interface service name:** `stop lab126_gui` also stops `framework`. `start lab126_gui` restores the Kindle (loading screen, then home).
- [x] **Fullscreen drawing:** with the interface stopped, `fbink -c -f -m -M -S 4 "…"` drew on a clean white screen, and nothing drew over it for 20 s. FBInk sees the PW3 framebuffer (`mxc_epdc_fb`) as 1072×1448, 8 bpp `Y8` grayscale, hardware rotation 3, and corrects for that rotation, so text came out upright in portrait. The server should send 8-bit grayscale PNGs at 1072×1448.
- [ ] **Image and touch alignment** (fits better in Stage 2): draw a test PNG with tap targets using `fbink -g file=…` and confirm the touch coordinates match it.
- [x] **Touch calibration:** **touch X/Y = panel pixels** (1072×1448 portrait), with no swap, mirroring or scaling. Taps about 4 mm in from the corners gave TL (51,48), TR (1027,54), BR (1037,1406) and BL (47,1407).
  - In `od -td4` field 3: `21626881` = BTN_TOUCH, `21299201` = BTN_TOOL_FINGER, `3735555` = TRACKING_ID (-1 on lift), `3473411` = X, `3538947` = Y. Positions are only sent when they change.
  - A finger drifts about 20 px during a tap, and taps last 250–480 ms. Use the touch-down position, and drop the tap if it moves more than about 60 px.
  - A single finger never sends slot events.
- [x] **Stay awake (short):** `bin/awaketest.sh` ran for 30 min with the interface stopped. Wi-Fi stayed `CONNECTED`, powerd stayed `active`, HTTP to the server returned 200 every minute, the battery charged from 97 to 100%, and the interface was restored at the end.
- [x] **Way back:** `start lab126_gui` works. A reboot restores the Kindle too, since nothing starts at boot.
- **Caveat from the user:** in earlier attempts, the Kindle **froze after hours or sometimes days**. A 30-minute pass doesn't rule that out. See the self-healing work in Stage 2 and the multi-day soak in Stage 6.

**Stage 0 is complete.** Image and touch alignment moved to Stage 2.

---

## Stage 1: Go server core with tests

**Goal:** Rewrite the prototype's server logic in Go.

- [x] `config.go`: atomic JSON store, version counter, file written with 0600 permissions, token never returned.
- [x] `ha.go`: read states, toggle (rejects entities that aren't configured), entity list.
- [x] `eccc.go`: find the newest file in the hourly directory, fall back to the old URL, 10-minute cache, mark stale data "(old)", ignore unknown XML elements, icon cache.
- [x] Version and long-poll logic: the version increases only when visible state changes. It wakes waiting requests and times out at about 25 s with `204`.
- [x] `/healthz`, `/api/toggle`.
- [x] Tests: `httptest` fake for HA, plus the ECCC fixture.

**Notes (done, 2026-11-23):**
- Code: `config.go`, `ha.go`, `eccc.go`, `state.go` (the `Hub` versioning plus the `App` refresh loop), `handlers.go`, `main.go`. Standard library only for now.
- The version counter starts from the server's start time in milliseconds, so a Kindle still holding a version from before a restart never matches by accident.
- "Visible state" is a JSON `Snapshot` (switches, HA error, weather, stale flag, clock, display settings). The version goes up only when it changes. HA is polled every 3 s, the minute boundary and config saves trigger a refresh, and toggles trigger an extra read 0.7 s later.
- A temporary `GET /api/state?v=N` long-poll returns the snapshot as JSON. `/frame.png` will use the same wait in Stage 2.
- Weather is marked stale after a failed refresh or when the data is over an hour old. It retries 60 s after a failure.
- Smoke test against `testdata/mock_ha.py` plus the live ECCC datamart worked: real Toronto data, toggle, and the long-poll woke at once.

**Exit (met):** `go test -race ./...` passes, covering the long-poll, config persistence, write-only token, unlisted-entity rejection and stale weather.

---

## Stage 2: Drawing and the tap path

**Goal:** Full loop: frame on the panel → tap → toggle → new frame.

Server:
- [ ] `layout.go`: computes the landscape canvas (1448×1072), the tile grid, the weather pane and the header. It returns tile rectangles.
- [ ] `render.go`: draws with `x/image/font/opentype` and the Go fonts, scales ECCC icons, rotates into the 1072×1448 panel frame and encodes a grayscale PNG.
- [ ] `/frame.png?v=N` (sets `X-Version`, and `X-Refresh: full` when the flash interval is due) and `POST /api/tap?x=&y=&v=` (inverse rotation, hit test against version `v`, pending dotted border, toggle).
- [ ] `GET /` debug page.
- [ ] Tests: golden-image tests for each rotation, and tap matching at all 4 rotations.

Kindle (`kindle/ha-dash/`):
- [ ] `frames.sh`: long-poll loop, `fbink` drawing, full flash on request, offline back-off and marker, Wi-Fi re-enable.
- [ ] `touch.sh`: `od | awk` event decoder, calibration mapping, `POST /api/tap` when a finger lifts, exit gesture (long press of 3 s or more, top-left).
- [ ] `start.sh` and `stop.sh`: stop and start the interface, `preventScreenSaver`, keep-alive, PID handling. Nothing starts at boot.
- [ ] **Self-healing** (for the freezes the user has seen after hours or days):
  - a supervisor restarts `frames.sh` or `touch.sh` if either dies;
  - escalating recovery when there's no successful poll: after about 2 min, cycle Wi-Fi (`wirelessEnable 0` then `1`); after about 10 min, restart the agent; reboot after about 30 min is optional and depends on the boot decision in Stage 6.
- [ ] **Heartbeat logging** that survives a reboot: one line a minute to `/mnt/us/ha-dash.log` (uptime, Wi-Fi, poll age, free memory, load), rotated at about 1 MB, so the moment a freeze happens is captured.
- [ ] **The server tracks "Kindle last seen"** from polls and shows it in `/healthz` and the settings page.

**Exit:** On the real Kindle, a tap on each tile at every rotation toggles the right entity in the mock HA. The pending border appears in under 1 s. The exit gesture restores the Kindle interface. The text is clear and readable on the panel.

---

## Stage 3: Settings page and admin API

**Goal:** Configure everything without a restart.

- [ ] `/api/admin/config`, `entities`, `test-ha` and `eccc-sites`, plus an optional admin password.
- [ ] `web/settings.html` sections: HA connection, entity picker (search, rename, drag to reorder, remove), weather location, display options (rotation, split, columns, flash interval, clock), live preview using `/frame.png`.
- [ ] Setup mode when there's no config, with a "configure me" frame, and seeding from `HA_URL`, `HA_TOKEN` and `PORT`.

**Exit:** Changing rotation, switches or credentials on a phone shows up on the Kindle within one poll cycle.

---

## Stage 4: Docker

**Goal:** An image you can ship.

- [ ] Multi-stage Dockerfile: `CGO_ENABLED=0`, distroless or scratch runtime with CA certificates, `time/tzdata`, non-root user, `-healthcheck` flag.
- [ ] `compose.yaml`: port bound to the LAN IP, `./data:/data`, `TZ`, `ADMIN_PASSWORD_FILE`, `restart: unless-stopped`.
- [ ] `compose.test.yaml` with a mock HA service.
- [ ] `buildx` builds for amd64, arm64 and armv7. GitHub Actions publishing to GHCR is optional.

**Exit:** The container is healthy, runs as non-root, settings survive `docker compose down && up`, and it fetches real ECCC data from inside the container.

---

## Stage 5: Real-world integration

**Goal:** Connect everything to the real devices.

- [ ] Point the server at the real HA by IP and at the real ECCC site.
- [ ] Tap tiles and confirm the devices switch.
- [ ] Change a device in HA and confirm the Kindle updates.
- [ ] Cut the server's internet and confirm the weather shows "(old)".
- [ ] Stop the server and confirm the Kindle shows the offline marker and recovers when the server comes back.
- [ ] Confirm the Kindle talks only to your server.

**Exit:** All the checks in `PLAN.md` §8 pass except the soak test.

---

## Stage 6: Soak test
*Several days. Freezes have previously shown up after hours or days.*

- [ ] Run it always-on while charging, for **at least 3–7 days**.
- [ ] Tune the flash interval.
- [ ] Review the heartbeat log and "Kindle last seen" daily: Wi-Fi drops, agent restarts, recovery steps taken, memory trend, ghosting.
- [ ] For any freeze, find out which layer failed: the agent (SSH works, screen stale), the network (no SSH, screen frozen) or the whole device (no response to anything).
- [ ] Decide whether to start the agent at boot, which also enables reboot as a recovery step. If so, keep the exit gesture.

**Exit:** The dashboard runs for 3 days or more without anyone touching it, and any freeze it hits recovers on its own.

---

## Stage 7: Documentation

- [ ] README covering the compose quickstart, the `data/` volume and backups, upgrades, Kindle and KUAL setup, FBInk, touch calibration, the exit gesture, and troubleshooting (mDNS, the log at `/tmp/ha-dash.log`).

---

## Order and parallel work

- Finish Stage 0 first. Stage 1 can start alongside it.
- Stage 2's server side (layout, drawing, golden tests) can start alongside Stage 0. Only the Kindle scripts need the calibration numbers.
- Stage 4 can start any time after Stage 1.
