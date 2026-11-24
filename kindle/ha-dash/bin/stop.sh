#!/bin/sh
# Stops the agent and brings the normal Kindle UI back.
# "stop.sh exit" is the exit gesture: it says so on screen first.
. "$(dirname "$0")/common.sh"

if [ "$1" = exit ]; then
  log "exit gesture"
  draw_text -c -m -M -S 3 "$(msg exit)"
fi
kill_pidfile "$RUN/agent.pid"   # first, so it doesn't restart the others
kill_pidfile "$RUN/frames.pid"
kill_pidfile "$RUN/touch.pid"
lipc-set-prop com.lab126.powerd preventScreenSaver 0
start lab126_gui 2>/dev/null
log "stopped, UI restarted"
