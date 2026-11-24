#!/bin/sh
DIR="$(cd "$(dirname "$0")/.." && pwd)"
URL="$(cat "$DIR/url.txt")"
LOG=/tmp/ha-dash.log
PIDF=/tmp/ha-dash.pid

# 1. stay awake
[ -f "$PIDF" ] && kill "$(cat "$PIDF")" 2>/dev/null
"$DIR/bin/keepawake.sh" >/dev/null 2>&1 &
echo $! > "$PIDF"

# 2. open the page in the built-in browser.
#    NOTE: how to pass a URL to the browser differs between firmware builds;
#    every attempt is logged to $LOG so you can see what your 5.16.2.1.1 accepts.
{
  echo "--- $(date) url=$URL"
  # Verified on 5.16.2.1.1: com.lab126.browser has no "open" property.
  # appmgrd with ?view= opens the URL (with chrome).
  lipc-set-prop com.lab126.appmgrd start "app://com.lab126.browser?view=$URL" 2>&1
  echo "rc=$?"
} >> "$LOG" 2>&1
