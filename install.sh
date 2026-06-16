#!/usr/bin/env sh
# Install termada. By default this DOWNLOADS the prebuilt binary — no Go needed:
#
#   curl -fsSL https://raw.githubusercontent.com/Islomzoda/termada/main/install.sh | sh
#
# Options (environment variables):
#   TERMADA_BIN_DIR=~/bin       where to install        (default: ~/.local/bin)
#   TERMADA_VERSION=v0.7.0      pin a release           (default: latest)
#   TERMADA_FROM_SOURCE=1       build from source       (requires Go + a checkout)
set -eu

REPO="Islomzoda/termada"
BIN_DIR="${TERMADA_BIN_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

build_from_source() {
  GO="${GO:-$(command -v go 2>/dev/null || echo "$HOME/.local/go/bin/go")}"
  { [ -x "$GO" ] || command -v "$GO" >/dev/null 2>&1; } || die "Go not found — install Go 1.26+ or use the prebuilt path (don't set TERMADA_FROM_SOURCE)"
  ROOT="$(cd "$(dirname "$0")" 2>/dev/null && pwd || echo '')"
  { [ -n "$ROOT" ] && [ -f "$ROOT/go.mod" ]; } || die "source build needs the repo — clone it and run ./install.sh from inside"
  say "Building termada from source with $GO ..."
  mkdir -p "$BIN_DIR"
  CGO_ENABLED=0 "$GO" build -trimpath -ldflags '-s -w' -o "$BIN_DIR/termada" "$ROOT/cmd/termada"
}

download_prebuilt() {
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$os" in darwin|linux) ;; *) die "no prebuilt for OS '$os' — use Docker, or TERMADA_FROM_SOURCE=1";; esac
  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) die "no prebuilt for arch '$arch' — use Docker, or TERMADA_FROM_SOURCE=1" ;;
  esac

  if command -v curl >/dev/null 2>&1; then GET="curl -fsSL"; GETO="curl -fsSL -o";
  elif command -v wget >/dev/null 2>&1; then GET="wget -qO-"; GETO="wget -qO";
  else die "need curl or wget"; fi

  ver="${TERMADA_VERSION:-}"
  if [ -z "$ver" ]; then
    ver=$($GET "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name" *: *"\([^"]*\)".*/\1/p' | head -1) || true
    [ -n "$ver" ] || die "couldn't find the latest version — set TERMADA_VERSION (e.g. v0.7.0)"
  fi

  asset="termada_${os}_${arch}.tar.gz"
  base="https://github.com/$REPO/releases/download/$ver"
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

  say "Downloading termada $ver ($os/$arch) ..."
  $GETO "$tmp/$asset" "$base/$asset" || die "download failed: $base/$asset — no prebuilt for $os/$arch? try Docker or TERMADA_FROM_SOURCE=1"

  # Verify the SHA-256 against the published checksums (best-effort).
  if $GETO "$tmp/checksums.txt" "$base/checksums.txt" 2>/dev/null; then
    want=$(grep " $asset\$" "$tmp/checksums.txt" 2>/dev/null | awk '{print $1}' || true)
    if [ -n "$want" ]; then
      if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$tmp/$asset" | awk '{print $1}');
      else got=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}'); fi
      [ "$want" = "$got" ] || die "checksum mismatch for $asset (want $want, got $got)"
      say "checksum verified"
    fi
  fi

  tar -xzf "$tmp/$asset" -C "$tmp"
  [ -f "$tmp/termada" ] || die "archive didn't contain the termada binary"
  mkdir -p "$BIN_DIR"
  mv "$tmp/termada" "$BIN_DIR/termada"
  chmod +x "$BIN_DIR/termada"
}

if [ "${TERMADA_FROM_SOURCE:-}" = "1" ]; then
  build_from_source
else
  download_prebuilt
fi

say "Installed: $BIN_DIR/termada"
"$BIN_DIR/termada" version

case ":$PATH:" in
  *":$BIN_DIR:"*) : ;;
  *) say ""; say "note: $BIN_DIR is not on your PATH yet — add this to your shell profile:"; say "  export PATH=\"$BIN_DIR:\$PATH\"" ;;
esac

cat <<EOF

Next:
  termada serve                                    # start the daemon + dashboard
  termada dashboard                                # open the dashboard in a browser
  claude mcp add termada -- termada serve --stdio  # let Claude Code use it
EOF
