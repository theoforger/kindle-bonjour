#!/bin/sh
# Long-polls the server for frames and draws them with FBInk.
# Run by agent.sh; see PLAN.md §4.
. "$(dirname "$0")/common.sh"
echo $$ > "$RUN/frames.pid"

HDR="$RUN/headers"
NEXT="$RUN/next.png"
CUR="$RUN/frame.png"
V=0          # version on screen; 0 makes the server answer at once
FAILS=0
OFFLINE=0    # the offline note is on screen

# hdr NAME: value of a response header (case-insensitive), last one wins.
hdr() {
  awk -v k="$1" 'BEGIN { k = tolower(k) }
    { sub(/\r$/, ""); sub(/^[ \t]+/, "") ; i = index($0, ":")
      if (i && tolower(substr($0, 1, i - 1)) == k) { v = substr($0, i + 1); sub(/^[ \t]+/, "", v) } }
    END { print v }' "$HDR"
}

fetch() {
  rm -f "$HDR" "$NEXT"
  if command -v curl >/dev/null 2>&1; then
    curl -s -m 40 -D "$HDR" -o "$NEXT" -w '%{http_code}' "$URL/frame.png?v=$V"
  else
    # busybox wget -S prints the headers to stderr, indented.
    wget -q -S -T 40 -O "$NEXT" "$URL/frame.png?v=$V" 2>"$HDR"
    sed -n 's/^[[:space:]]*HTTP\/[0-9.]*[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$HDR" | tail -n 1
  fi
}

good() {
  date +%s > "$RUN/lastok"
  FAILS=0
}

failed() {
  FAILS=$((FAILS + 1))
  [ "$FAILS" -le 3 ] || [ $((FAILS % 20)) -eq 0 ] && log "poll failed ($1), $FAILS in a row"
  if [ "$FAILS" -ge 3 ] && [ "$OFFLINE" = 0 ]; then
    # Bottom row, inverted, so it stands out on whatever frame is there.
    draw_text -h -m -y -1 -S 2 " $(msg offline) "
    OFFLINE=1
  fi
  if [ $((FAILS % 10)) -eq 3 ]; then
    lipc-set-prop com.lab126.cmd wirelessEnable 1 2>/dev/null
  fi
  n=$((FAILS * 2)); [ "$n" -gt 30 ] && n=30
  sleep "$n"
}

log "start, server $URL, fbink ${FBINK:-missing}"
while :; do
  CODE="$(fetch)"
  case "$CODE" in
    200)
      NV="$(hdr x-version)"
      if [ -z "$NV" ] || [ ! -s "$NEXT" ]; then
        failed "200 without a frame"
        continue
      fi
      FULL=
      [ "$(hdr x-refresh)" = full ] && FULL=-f
      [ "$OFFLINE" = 1 ] && FULL=-f   # clear the offline note
      L="$(hdr x-language)"
      [ -n "$L" ] && echo "$L" > "$RUN/lang"
      mv -f "$NEXT" "$CUR"
      if "$FBINK" -q $FULL -g file="$CUR"; then
        V="$NV"
        echo "$V" > "$RUN/version"
        OFFLINE=0
      else
        log "fbink failed on version $NV"
      fi
      good
      ;;
    204)
      good
      if [ "$OFFLINE" = 1 ]; then
        V=0   # back online but nothing changed: ask for the frame again to clear the note
      fi
      ;;
    *)
      failed "http ${CODE:-none}"
      ;;
  esac
done
