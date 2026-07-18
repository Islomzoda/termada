#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
state=${TERMADA_DEMO_STATE:-${TMPDIR:-/tmp}/termada-mission-control-verify-$(id -u)}
export TERMADA_DEMO_STATE=$state
trap '"$root/demo/mission-control/stop.sh" >/dev/null 2>&1 || true' EXIT INT TERM
"$root/demo/mission-control/run.sh"
"$root/demo/mission-control/verify_flow.py"
