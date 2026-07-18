#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
state=${TERMADA_DEMO_STATE:-${TMPDIR:-/tmp}/termada-mission-control-$(id -u)}
binary=${TERMADA_BIN:-$state/termada}
test -x "$binary" || { echo "Run $root/demo/mission-control/run.sh first" >&2; exit 1; }
HOME="$state/home" exec "$binary" serve --stdio --agent build-week
