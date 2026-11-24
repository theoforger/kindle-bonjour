#!/bin/sh
# KUAL "Start": takes over the screen. Leave with the exit gesture (hold a
# screen corner for 3 s, then lift), bin/stop.sh over SSH, or a reboot.
# Nothing here runs at boot.
. "$(dirname "$0")/common.sh"

if [ -z "$URL" ]; then
  draw_text -m -M -S 3 "$(msg nourl)"
  exit 1
fi
if [ -z "$FBINK" ]; then
  log "no fbink found"
  exit 1
fi
if alive "$RUN/agent.pid"; then
  exit 0
fi
# Own session, so it survives KUAL and the UI being stopped.
$SETSID sh "$BIN/agent.sh" </dev/null >>"$LOG" 2>&1 &
