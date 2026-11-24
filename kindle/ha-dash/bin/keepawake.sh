#!/bin/sh
# Re-assert "no screensaver / no sleep" and keep Wi-Fi up, every 30 s.
while true; do
  lipc-set-prop com.lab126.powerd preventScreenSaver 1
  lipc-set-prop com.lab126.cmd wirelessEnable 1 2>/dev/null
  sleep 30
done
