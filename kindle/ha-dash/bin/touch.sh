#!/bin/sh
# Reads the touchscreen and sends taps to the server; catches the exit
# gesture (hold a corner for EXIT_HOLD seconds, then lift).
# Run by agent.sh; see PLAN.md §4 and STAGES.md Stage 0 for the numbers.
. "$(dirname "$0")/common.sh"
echo $$ > "$RUN/touch.pid"

log "start, $TOUCH_DEV"

# od buffers its output when writing to a pipe, so a long-running od
# delivers taps late. Instead, each dd does a single read(2), which returns
# every event queued so far (16 bytes each on this 32-bit kernel); od then
# formats that batch and exits, flushing it.
#
# od -td4 -w16 gives one event per line: sec usec type+code*65536 value.
# -v keeps repeated lines (od would print "*" for them).
while :; do
  if [ ! -e "$TOUCH_DEV" ]; then
    log "no $TOUCH_DEV"
    sleep 10
    continue
  fi
  dd if="$TOUCH_DEV" bs=1536 count=1 2>/dev/null | od -An -v -td4 -w16
done | awk -v url="$URL" -v run="$RUN" -v bin="$BIN" -v setsid="$SETSID" \
  -v tapmax="$TAP_MAX" -v move="$TAP_MOVE" -v hold="$EXIT_HOLD" -v corner="$EXIT_CORNER" '
BEGIN {
  # type + code * 65536
  SYN = 0; SLOT = 3080195; X = 3473411; Y = 3538947; TRACK = 3735555
  W = 1072; H = 1448      # panel; touch X/Y are panel pixels (Stage 0)
  move2 = move * move
  slot = 0; down = 0
}
function corner_hit(x, y) {
  return (x < corner || x >= W - corner) && (y < corner || y >= H - corner)
}
function finish(   dur, ver, f) {
  dur = t - t0
  if (!started || moved || multi) return
  if (dur >= hold && corner_hit(sx, sy)) {
    system(setsid " sh " bin "/stop.sh exit </dev/null >/dev/null 2>&1 &")
    return
  }
  if (dur > tapmax) return
  ver = ""; f = run "/version"
  if ((getline ver < f) <= 0) ver = ""
  close(f)
  if (ver == "") return
  system("sh " bin "/post.sh \"" url "/api/tap?x=" sx "&y=" sy "&v=" ver "\" </dev/null >/dev/null 2>&1 &")
}
NF == 4 {
  t = $1 + $2 / 1000000; tc = $3; v = $4
  if (tc == SYN) {
    if (down && !started) { sx = cx; sy = cy; started = 1 }
    if (down && started && (cx - sx) * (cx - sx) + (cy - sy) * (cy - sy) > move2) moved = 1
    if (lifted) { finish(); lifted = 0; down = 0 }
    next
  }
  if (tc == SLOT) { slot = v; next }
  if (tc == TRACK) {
    if (slot != 0) { if (v != -1) multi = 1; next }   # a second finger cancels the tap
    if (v != -1) { down = 1; started = 0; moved = 0; multi = 0; lifted = 0; t0 = t }
    else if (down) lifted = 1
    next
  }
  # Positions are only sent when they change, so keep the last ones.
  if (slot == 0 && tc == X) cx = v
  if (slot == 0 && tc == Y) cy = v
}'
