#!/bin/sh
# Supervisor: stops the Kindle UI, keeps the device awake, runs frames.sh
# and touch.sh, restarts them if they die, recovers from lost polls step by
# step, and writes a heartbeat line every minute. Started by start.sh.
. "$(dirname "$0")/common.sh"

echo $$ > "$RUN/agent.pid"
RESTARTS=0
LAST_WIFI_CYCLE=0
LAST_RESTART=0

# Each child writes its own pid file (its pid is its process group).
spawn() {
  rm -f "$RUN/$1.pid"
  $SETSID sh "$BIN/$1.sh" </dev/null >>"$LOG" 2>&1 &
}

stop_children() {
  kill_pidfile "$RUN/frames.pid"
  kill_pidfile "$RUN/touch.pid"
}

cleanup() {
  log "stopping"
  stop_children
  rm -f "$RUN/agent.pid"
  exit 0
}
trap cleanup INT TERM

heartbeat() {
  now=$1
  last="$(cat "$RUN/lastok" 2>/dev/null)"
  age=$((now - ${last:-0}))
  line="$(date '+%Y-%m-%d %H:%M:%S') up=$(cut -d' ' -f1 /proc/uptime)s load=$(cut -d' ' -f1-3 /proc/loadavg | tr ' ' ,)"
  line="$line wifi=$(lipc-get-prop com.lab126.wifid cmState 2>/dev/null) poll_age=${age}s"
  line="$line memfree=$(awk '/^MemFree:/ { print $2 }' /proc/meminfo)kB batt=$(lipc-get-prop com.lab126.powerd battLevel 2>/dev/null)"
  line="$line restarts=$RESTARTS version=$(cat "$RUN/version" 2>/dev/null)"
  echo "$line" >> "$HEARTBEAT"
  size=$(wc -c < "$HEARTBEAT" 2>/dev/null)
  if [ "${size:-0}" -gt "$HEARTBEAT_MAX" ]; then
    mv -f "$HEARTBEAT" "$HEARTBEAT.1"
  fi
}

log "start, server $URL"
echo "$(date '+%Y-%m-%d %H:%M:%S') agent start, server $URL" >> "$HEARTBEAT"
lipc-set-prop com.lab126.powerd preventScreenSaver 1
stop lab126_gui 2>/dev/null   # also stops framework (Stage 0)
sleep 2
draw_text -c -f
date +%s > "$RUN/lastok"
spawn frames
spawn touch

TICK=0
while :; do
  sleep 10 &
  wait $!   # so TERM is handled at once
  TICK=$((TICK + 1))
  now=$(date +%s)

  # Stay awake (powerd may undo this, so keep saying it).
  [ $((TICK % 3)) -eq 0 ] && lipc-set-prop com.lab126.powerd preventScreenSaver 1

  for c in frames touch; do
    if ! alive "$RUN/$c.pid"; then
      log "$c died, restarting"
      kill_pidfile "$RUN/$c.pid"
      spawn "$c"
      RESTARTS=$((RESTARTS + 1))
    fi
  done

  # Escalating recovery when polls stop succeeding.
  last="$(cat "$RUN/lastok" 2>/dev/null)"
  age=$((now - ${last:-now}))
  if [ "$REBOOT_AFTER" -gt 0 ] && [ "$age" -ge "$REBOOT_AFTER" ]; then
    log "no good poll for ${age}s: rebooting"
    echo "$(date '+%Y-%m-%d %H:%M:%S') reboot, poll_age=${age}s" >> "$HEARTBEAT"
    sync
    reboot
  elif [ "$age" -ge "$RESTART_AFTER" ] && [ $((now - LAST_RESTART)) -ge "$RESTART_AFTER" ]; then
    log "no good poll for ${age}s: restarting frames and touch"
    echo "$(date '+%Y-%m-%d %H:%M:%S') restart, poll_age=${age}s" >> "$HEARTBEAT"
    stop_children
    spawn frames
    spawn touch
    RESTARTS=$((RESTARTS + 1))
    LAST_RESTART=$now
  elif [ "$age" -ge "$WIFI_CYCLE_AFTER" ] && [ $((now - LAST_WIFI_CYCLE)) -ge "$WIFI_CYCLE_AFTER" ]; then
    log "no good poll for ${age}s: cycling Wi-Fi"
    echo "$(date '+%Y-%m-%d %H:%M:%S') wifi cycle, poll_age=${age}s" >> "$HEARTBEAT"
    lipc-set-prop com.lab126.cmd wirelessEnable 0 2>/dev/null
    sleep 3
    lipc-set-prop com.lab126.cmd wirelessEnable 1 2>/dev/null
    LAST_WIFI_CYCLE=$now
  fi

  [ $((TICK % 6)) -eq 0 ] && heartbeat "$now"
done
