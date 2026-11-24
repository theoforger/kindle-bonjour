#!/bin/sh
# post.sh URL: fire-and-forget POST, with curl or busybox wget.
if command -v curl >/dev/null 2>&1; then
  curl -s -m 5 -X POST -o /dev/null "$1"
else
  wget -q -T 5 -O /dev/null --post-data= "$1"
fi
