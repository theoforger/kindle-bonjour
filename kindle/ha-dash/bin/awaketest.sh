#!/bin/sh
# Stage 0: stay-awake test with the Kindle UI stopped.
# Usage: nohup sh awaketest.sh [minutes] >/dev/null 2>&1 &
# Logs to /mnt/us/ha-dash-awake.log; restores the UI when finished.
MIN=${1:-30}
DIR="$(cd "$(dirname "$0")/.." && pwd)"
URL="$(cat "$DIR/url.txt")"
F=/mnt/us/libkh/bin/fbink
L=/mnt/us/ha-dash-awake.log

restore() {
  start lab126_gui
  lipc-set-prop com.lab126.powerd preventScreenSaver 0
  echo "$(date +%T) restored" >> "$L"
}
trap 'restore; exit 0' INT TERM

echo "$(date +%T) start, $MIN min, url=$URL" > "$L"
lipc-set-prop com.lab126.powerd preventScreenSaver 1
stop lab126_gui
sleep 3
$F -q -c -f

i=1
while [ "$i" -le "$MIN" ]; do
  lipc-set-prop com.lab126.powerd preventScreenSaver 1
  WIFI=$(lipc-get-prop com.lab126.wifid cmState 2>/dev/null)
  PWR=$(lipc-get-prop com.lab126.powerd state 2>/dev/null)
  HTTP=$(curl -s -o /dev/null -w '%{http_code}' -m 5 "$URL")
  BAT=$(lipc-get-prop com.lab126.powerd battLevel 2>/dev/null)
  echo "$(date +%T) $i/$MIN wifi=$WIFI power=$PWR http=$HTTP batt=$BAT" >> "$L"
  $F -q -m -M -S 3 "awake $i/$MIN wifi=$WIFI http=$HTTP"
  [ "$HTTP" = "000" ] && lipc-set-prop com.lab126.cmd wirelessEnable 1
  sleep 60
  i=$((i + 1))
done

restore
