#!/usr/bin/env sh
# Build and install the termada binary.
#
# Usage:
#   ./install.sh                 # install to ~/.local/bin (or $TERMADA_BIN_DIR)
#   TERMADA_BIN_DIR=~/bin ./install.sh
#
# Requires Go 1.26+. If `go` is not on PATH it falls back to ~/.local/go/bin/go.
set -eu

GO="${GO:-$(command -v go 2>/dev/null || echo "$HOME/.local/go/bin/go")}"
if [ ! -x "$GO" ] && ! command -v "$GO" >/dev/null 2>&1; then
  echo "error: Go toolchain not found. Install Go 1.26+ or set \$GO." >&2
  exit 1
fi

BIN_DIR="${TERMADA_BIN_DIR:-$HOME/.local/bin}"
mkdir -p "$BIN_DIR"

ROOT="$(cd "$(dirname "$0")" && pwd)"
echo "Building termada with $GO ..."
"$GO" build -o "$BIN_DIR/termada" "$ROOT/cmd/termada"

echo "Installed: $BIN_DIR/termada"
"$BIN_DIR/termada" version

case ":$PATH:" in
  *":$BIN_DIR:"*) : ;;
  *) echo "note: $BIN_DIR is not on your PATH. Use the absolute path in your MCP config, or add it to PATH." >&2 ;;
esac

cat <<EOF

Next: register it with your MCP client. For Claude Code:
  claude mcp add termada -- "$BIN_DIR/termada" serve

Or add to your client's MCP config:
  { "mcpServers": { "termada": { "command": "$BIN_DIR/termada", "args": ["serve"] } } }
EOF
