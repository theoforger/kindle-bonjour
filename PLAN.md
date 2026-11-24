# Final plan: Kindle PW3 Home Assistant + ECCC weather dashboard

> **Revision 2 (2026-11-23):** Switched from the Kindle browser to **server-rendered PNG frames shown with FBInk**. Stage 0 found that on firmware 5.16.2.1.1 the browser can't hide its toolbars, and it redraws the status-bar spinner on every request. See `STAGES.md` → Stage 0 findings.

## 1. Goal
A fullscreen, always-on **landscape** dashboard on a jailbroken Kindle Paperwhite 3 (firmware 5.16.2.1.1). Home Assistant switches are on the left and Environment Canada weather is on the right. Switches toggle by touch. A settings page, opened from a phone or laptop, configures everything, and changes reach the Kindle within seconds. The Kindle is LAN-only, so all internet access happens on the server. It ships as a Docker image with a compose file.

## 2. Architecture
**The server does all the work, and the Kindle only displays images and reports taps.**

- **Server:** one Go binary in a container. It draws the dashboard as a grayscale PNG at the panel's native resolution and orientation (1072×1448), with rotation already applied. It also works out which tile a tap hit and talks to HA and ECCC.
- **Kindle agent:** a shell script. It stops the Kindle's normal interface and waits for new frames from the server (long-poll). It draws each frame with FBInk, reads raw taps from the touchscreen and sends their coordinates to the server.

| Route | Purpose |
|---|---|
| `GET /frame.png?v=N` | Long-poll (up to ~25 s). Returns `200` + PNG + `X-Version`, plus `X-Refresh: full` when a ghosting flash is due, or `204` when nothing changed |
| `POST /api/tap?x=&y=&v=` | Tap at panel coordinates on the frame of version `v`. The server maps the coordinates through the rotation, finds the tile, toggles it and draws a new frame |
| `POST /api/toggle?id=` | Toggle a configured entity directly (for the settings page and debugging; unlisted entities are rejected) |
| `GET /` | Debug view: the current frame in an `<img>` |
| `GET /settings` | Settings page (modern JS, used from a phone or laptop) |
| `GET/PUT /api/admin/config` | Settings (the token is write-only and never returned) |
| `GET /api/admin/entities` | HA entity list for the picker |
| `POST /api/admin/test-ha` | Test the HA URL and token |
| `GET /api/admin/eccc-sites` | ECCC site search |
| `GET /healthz` | Health check |

**Source layout:** `main.go`, `config.go` (atomic JSON store plus version counter), `ha.go`, `eccc.go` (`encoding/xml` structs), `render.go` (layout, drawing, rotation), `layout.go` (tile rectangles shared by drawing and tap matching), `handlers.go`, `web/settings.html`, `*_test.go`, `kindle/` (KUAL extension).

**Dependencies:** standard library plus `golang.org/x/image` only. That package supplies `font/opentype`, the built-in Go fonts (`font/gofont`, BSD-licensed, so no font file to ship) and `draw` for scaling the ECCC icons. `CGO_ENABLED=0` still works.

**Weather:** The server finds the newest file under `https://dd.weather.gc.ca/today/citypage_weather/{PROV}/{HH}/` (trying the current and previous 3 UTC hours). It falls back to `/citypage_weather/xml/{PROV}/{site}_e.xml`, caches for 10 minutes, and draws the last good data marked "(old)" if a refresh fails. Parsing ignores unknown elements, because ECCC has been changing this feed through 2026. ECCC icons (GIF) are fetched once, cached in `/data`, then decoded and scaled into the frame.

## 3. The frame (server-side drawing)
- **Canvas:** a landscape layout at 1448×1072, rotated by 0/90/180/270 into the 1072×1448 panel frame. Rotation comes from settings. Landscape is the default.
- **Left pane:** switch tiles in a grid, with a configurable number of columns. Tiles show ON (filled black, white text), OFF (outlined), N/A (gray) or RUN (scenes, scripts and buttons). A dotted border marks a pending tap.
- **Right pane:** current conditions with an icon, a warning banner and 6 forecast periods.
- **Header:** clock (server time, minute resolution) and a small "last update" or "(old)" marker.
- **E-ink rules:** pure black and white plus a few grays, no thin gray text, and large hit targets (at least about 9 mm, or roughly 110 px at 300 ppi).
- **Versioning:** the frame is drawn again and the version increases only when something visible changes (state, weather, config or the clock minute). The server keeps the tile rectangles for recent versions so a tap is matched against the frame the user actually saw.
- **Settings preview:** the settings page shows `/frame.png` directly, so the preview is exactly what the Kindle shows.

## 4. Kindle agent (KUAL extension)
`kindle/ha-dash/`:
- `url.txt`: the server address.
- `bin/start.sh`:
  - stops the Kindle interface (`stop lab126_gui`) and turns on `preventScreenSaver`;
  - starts `frames.sh` and `touch.sh`, logging to `/tmp/ha-dash.log`.
- `bin/frames.sh`: loops on `GET /frame.png?v=N` (with curl, or busybox `wget` if curl is missing). It draws each frame with `fbink -g file=…`, using a flashing full refresh (`-f`) when the server sends `X-Refresh: full`. If the request fails it backs off and draws an "offline" note in a corner.
- `bin/touch.sh`: reads the touchscreen's `/dev/input/eventN` through `od | awk`. It decodes multitouch slot and position events, turns them into panel coordinates using the calibration from Stage 0, and sends `POST /api/tap` when a finger lifts. It also catches the **exit gesture**, a long press (3 s or more) in the top-left corner, and runs `stop.sh`. While the Kindle interface is stopped, KUAL can't be reached, so this gesture is the only way out without SSH.
- `bin/stop.sh`: kills the agent, turns `preventScreenSaver` off and restarts the interface (`start lab126_gui`).
- `bin/fbink`: a static FBInk build, bundled if the device doesn't already have one.
- **Wi-Fi:** `frames.sh` re-enables Wi-Fi (`lipc-set-prop com.lab126.cmd wirelessEnable 1`) after repeated failures.
- **Nothing starts at boot** until the soak test passes, so a reboot always brings back the normal Kindle.

## 5. Settings page
- **HA connection:** URL, masked token, "Test connection" button.
- **Switches:** Search and filter HA entities, add them, rename, drag to reorder, remove.
- **Weather:** Province and city search, language, refresh interval.
- **Display:** Rotation, left/right split, tile columns, flash interval, clock on/off.
- **Live preview:** `<img src="/frame.png">`, reloaded whenever the version changes.
- **Access:** Optional admin password.

## 6. Docker
- **Image:** Multi-stage build, with `golang:alpine` as the build stage (`CGO_ENABLED=0`). The runtime is `scratch` or distroless, with CA certificates (needed for HTTPS to ECCC). Import `time/tzdata` so `TZ` works. Run as non-root. Add a `-healthcheck` flag, since there's no `curl` in the image.
- **Multi-arch:** Build amd64, arm64 and armv7 with `buildx`. Optionally publish to GHCR via GitHub Actions.
- **Compose:**
  - Bind the port to your LAN IP only.
  - Mount `./data:/data` for `config.json` (written 0600) and the icon cache.
  - Set `TZ`, optional `ADMIN_PASSWORD_FILE`, and a healthcheck.
  - Use `restart: unless-stopped`.
- **First run:** With no config, the server starts in setup mode, showing only `/settings`, and `/frame.png` shows a "configure me at http://…/settings" frame. `HA_URL`, `HA_TOKEN` and `PORT` environment variables can seed a config.
- **Networking:** Use HA's LAN IP, not `homeassistant.local` (mDNS usually fails in containers). The container needs outbound access to `dd.weather.gc.ca` and `weather.gc.ca`. The Kindle only needs the published port.

## 7. Build order
See `STAGES.md` for the detailed checklists.
0. **Device checks:** fullscreen FBInk drawing with the interface stopped, touch calibration, tools available, and staying awake and on Wi-Fi.
1. **Go server core with tests:** config, HA, ECCC and the long-poll versioning.
2. **Drawing and the tap path:** `render.go` and `layout.go` with golden-image tests, then the Kindle agent scripts, tested end to end on the device.
3. **Settings page, config API and live updates.**
4. **Dockerfile, compose, `compose.test.yaml`** (adds a mock HA service) and the healthcheck.
5. **Real-world integration.**
6. **Always-on soak test of several hours,** tuning the flash interval.
7. **README:** compose quickstart, `data/` volume, upgrades, Kindle setup, the exit gesture.

## 8. Acceptance checks
- Tapping a tile toggles the real device, and an external HA change appears on the Kindle within one poll cycle.
- Real ECCC data appears, or the last good data is marked "(old)" when the server loses internet.
- Changing rotation, switches or HA credentials in settings applies on the Kindle with no restart.
- Taps land on the right tile at every rotation.
- The exit gesture and `stop.sh` bring back the normal Kindle interface, and so does a reboot.
- The Kindle contacts only your server.
- The container is healthy, runs non-root, and settings survive `docker compose down && up`.
- The screen and Wi-Fi stay on for hours with no redraw when nothing changes.

## 9. Known risks
| Risk | Mitigation |
|---|---|
| **Locked out:** the interface is stopped and KUAL is gone | Exit gesture, SSH `stop.sh`, nothing starts at boot, so a reboot restores the Kindle |
| **Touch device or coordinates** differ (axes swapped or mirrored, a different max range) | Calibrate in Stage 0 by tapping the corners. Keep the mapping in `touch.sh` config |
| **powerd suspends** with the interface stopped | `preventScreenSaver` plus a keep-alive loop. A 30-min test passed in Stage 0 |
| **Freezes after hours or days** (seen in the user's earlier attempts) | Supervisor and escalating recovery (cycle Wi-Fi, then restart the agent, then optionally reboot), a persistent heartbeat log, "Kindle last seen" on the server, a multi-day soak |
| **The interface's service name** isn't `lab126_gui` on 5.16.2.1.1 | Check with `initctl list` in Stage 0 |
| **curl is missing** on the device | busybox `wget -S` fallback |
| **Ghosting** from partial refreshes | Server-scheduled full flashes, tuned in the soak test |
| **Text rendering** looks bad on e-ink | Bold Go fonts at large sizes, no anti-aliased thin strokes, test on the device in Stage 2 |
| **ECCC feed** layout changes again | Support both URL schemes, log parse errors |
| **Settings page** exposes the HA token | Write-only token, admin password, LAN-only port binding |
| **mDNS names** fail in the container | Use IPs or a shared compose network |
| **Always-on** wears the battery | Keep it on a charger with a timer |
| **Lost `data/` volume** loses the token and layout | Keep it in a bind mount you back up |

## 10. Starting material
The original prototype (Python server, ES5 browser page, KUAL extension) is kept locally in `prototype/` and is not in git. What carries over into the repo:
- the mock HA → `testdata/mock_ha.py`;
- the ECCC fixture → `testdata/eccc/citypage_en.xml`;
- the KUAL extension and keep-awake approach → `kindle/ha-dash/`.

`prototype/kindle-ha/server.py` is the reference for porting the HA and ECCC logic in Stage 1.
