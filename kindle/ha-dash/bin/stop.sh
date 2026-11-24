#!/bin/sh
[ -f /tmp/ha-dash.pid ] && kill "$(cat /tmp/ha-dash.pid)" 2>/dev/null
rm -f /tmp/ha-dash.pid
lipc-set-prop com.lab126.powerd preventScreenSaver 0
