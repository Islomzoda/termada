#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
test -f "$root/.termada-demo"
pid=$(cat "$root/service.pid")
case "$pid" in *[!0-9]*|'') echo "invalid demo service pid" >&2; exit 1;; esac
kill -0 "$pid"
printf 'healthy\n' > "$root/service.mode"
kill -HUP "$pid"
sleep 0.2
printf 'reloaded demo service %s with mode=%s\n' "$pid" "$(cat "$root/service.mode")"
