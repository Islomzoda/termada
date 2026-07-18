#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
state=${TERMADA_DEMO_STATE:-${TMPDIR:-/tmp}/termada-mission-control-$(id -u)}
home="$state/home"
workspace="$state/broken-checkout"
binary=${TERMADA_BIN:-$state/termada}

command -v python3 >/dev/null 2>&1 || { echo "python3 is required" >&2; exit 1; }
mkdir -p "$state" "$home" "$workspace"

stop_pid() {
  file=$1
  if test -f "$file"; then
    pid=$(cat "$file" 2>/dev/null || true)
    case "$pid" in *[!0-9]*|'') return;; esac
    if kill -0 "$pid" 2>/dev/null; then
      kill "$pid"
      i=0
      while kill -0 "$pid" 2>/dev/null && test "$i" -lt 20; do sleep 0.1; i=$((i+1)); done
    fi
  fi
}

stop_pid "$state/daemon.pid"
stop_pid "$workspace/service.pid"

if test -z "${TERMADA_BIN:-}"; then
  echo "Building the local Termada binary..."
  (cd "$root" && go build -o "$binary" ./cmd/termada)
fi

cp "$root/demo/mission-control/service.py" "$workspace/service.py"
cp "$root/demo/mission-control/probe.py" "$workspace/probe.py"
cp "$root/demo/mission-control/apply-fix.sh" "$workspace/apply-fix.sh"
cp "$root/demo/mission-control/config.yaml" "$state/config.yaml"
chmod 700 "$workspace/service.py" "$workspace/probe.py" "$workspace/apply-fix.sh"
: > "$workspace/.termada-demo"
printf 'broken\n' > "$workspace/service.mode"
: > "$workspace/service.port"

(
  cd "$workspace"
  nohup python3 ./service.py >service.log 2>&1 </dev/null &
  echo $! >service.pid
)
i=0
while ! test -s "$workspace/service.port"; do
  i=$((i+1)); test "$i" -lt 50 || { echo "demo service did not start" >&2; exit 1; }
  sleep 0.1
done

HOME="$home" nohup "$binary" serve --config "$state/config.yaml" --bind 127.0.0.1:0 >"$state/daemon.log" 2>&1 </dev/null &
echo $! > "$state/daemon.pid"
i=0
while ! grep -q 'dashboard:  http' "$state/daemon.log" 2>/dev/null; do
  i=$((i+1)); test "$i" -lt 100 || { echo "Termada daemon did not start; see $state/daemon.log" >&2; exit 1; }
  sleep 0.1
done

dashboard=$(sed -n 's/.*dashboard:  \(http.*\)$/\1/p' "$state/daemon.log" | tail -1)
cat <<EOF

Termada Mission Control demo is ready.

Dashboard: $dashboard
Workspace: $workspace
MCP command: $root/demo/mission-control/mcp.sh

Use the prompt in:
  $root/demo/mission-control/PROMPT.md

Optional automated MCP verification (uses the real approval API):
  TERMADA_DEMO_STATE=$state $root/demo/mission-control/verify_flow.py

Stop only this isolated demo with:
  $root/demo/mission-control/stop.sh
EOF
