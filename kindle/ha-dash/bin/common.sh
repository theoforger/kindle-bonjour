# Shared settings for the ha-dash agent. Sourced, not run.
# Put overrides (TOUCH_DEV, EXIT_HOLD, REBOOT_AFTER, ...) in ../config.sh.

BIN="$(cd "$(dirname "$0")" && pwd)"
DIR="$(dirname "$BIN")"

# Server base URL: first line of url.txt, without trailing slashes.
URL="$(sed -n '1{s/[[:space:]]*$//;s:/*$::;p;}' "$DIR/url.txt" 2>/dev/null)"

RUN=/tmp/ha-dash                 # runtime state: pids, current frame, version
LOG=/tmp/ha-dash.log             # agent log (lost on reboot)
HEARTBEAT=/mnt/us/ha-dash.log    # one line a minute, survives reboots
HEARTBEAT_MAX=1048576            # rotate at ~1 MB

TOUCH_DEV=/dev/input/event1      # cyttsp4_mt on the PW3 (Stage 0)
TAP_MAX=1.5                      # seconds; longer presses are not taps
TAP_MOVE=60                      # px; a finger that moves further is not a tap
EXIT_HOLD=3                      # seconds of holding a corner to exit
EXIT_CORNER=150                  # px from a panel corner

WIFI_CYCLE_AFTER=120             # s without a good poll: cycle Wi-Fi
RESTART_AFTER=600                # s without a good poll: restart frames + touch
REBOOT_AFTER=0                   # s without a good poll: reboot (0 = never)

[ -f "$DIR/config.sh" ] && . "$DIR/config.sh"

FBINK=
for f in "$BIN/fbink" /mnt/us/libkh/bin/fbink /mnt/us/koreader/fbink "$(command -v fbink 2>/dev/null)"; do
  if [ -n "$f" ] && [ -x "$f" ]; then FBINK="$f"; break; fi
done

# Run a command in its own process group, so it can be killed with all
# its children (pipelines included).
SETSID=
command -v setsid >/dev/null 2>&1 && SETSID=setsid

mkdir -p "$RUN"

log() { echo "$(date '+%Y-%m-%d %H:%M:%S') [$(basename "$0" .sh)] $*" >> "$LOG"; }

# Kill a process group (or just the process if it has none) from a pid file.
kill_pidfile() {
  [ -f "$1" ] || return 0
  p="$(cat "$1" 2>/dev/null)"
  if [ -n "$p" ]; then
    kill -TERM -"$p" 2>/dev/null || kill -TERM "$p" 2>/dev/null
  fi
  rm -f "$1"
}

alive() { [ -f "$1" ] && kill -0 "$(cat "$1")" 2>/dev/null; }

# Messages the agent draws itself (the server isn't there to draw them).
# The language is whatever the server last sent in X-Language. FBInk's
# built-in font is ASCII only, so no accents.
msg() {
  lang="$(cat "$RUN/lang" 2>/dev/null)"
  case "$1:$lang" in
    offline:fr) echo "Serveur injoignable" ;;
    offline:*)  echo "Server unreachable" ;;
    exit:fr)    echo "Retour au Kindle..." ;;
    exit:*)     echo "Returning to the Kindle..." ;;
    nourl:fr)   echo "url.txt manquant" ;;
    nourl:*)    echo "url.txt is missing" ;;
  esac
}

draw_text() { [ -n "$FBINK" ] && "$FBINK" -q "$@"; }
