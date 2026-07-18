#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
trap '"$root/demo/mission-control/stop.sh" >/dev/null 2>&1 || true; exit 0' INT TERM EXIT
"$root/demo/mission-control/run.sh"
echo "Demo preview is running; press Ctrl-C to stop it."
while :; do sleep 3600; done
