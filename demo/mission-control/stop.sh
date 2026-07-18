#!/bin/sh
set -eu
state=${TERMADA_DEMO_STATE:-${TMPDIR:-/tmp}/termada-mission-control-$(id -u)}
for file in "$state/daemon.pid" "$state/broken-checkout/service.pid"; do
  test -f "$file" || continue
  pid=$(cat "$file" 2>/dev/null || true)
  case "$pid" in *[!0-9]*|'') continue;; esac
  kill "$pid" 2>/dev/null || true
done
echo "Stopped the isolated Mission Control demo."
