#!/usr/bin/env sh
# Install termada. By default this DOWNLOADS the prebuilt binary — no Go needed:
#
#   curl -fsSL https://raw.githubusercontent.com/Islomzoda/termada/main/install.sh | sh
#
# Options (environment variables):
#   TERMADA_BIN_DIR=~/bin       where to install        (default: ~/.local/bin)
#   TERMADA_VERSION=vX.Y.Z      pin a release           (default: latest)
#   TERMADA_FROM_SOURCE=1       build from source       (requires Go + a checkout)
#   TERMADA_RELEASE_PUBKEY=...  require checksums.txt.sig (base64 Ed25519 key)
set -efu
umask 077

REPO="Islomzoda/termada"
BIN_DIR="${TERMADA_BIN_DIR:-$HOME/.local/bin}"
MAX_METADATA_BYTES=2097152
MAX_CHECKSUM_BYTES=2097152
MAX_SIGNATURE_BYTES=4096
MAX_ARCHIVE_BYTES=134217728
MAX_BINARY_BYTES=134217728

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

fetch_file() {
  url=$1 out=$2 limit=$3
  file_blocks=$((limit / 512 + 1))
  if command -v curl >/dev/null 2>&1; then
    (ulimit -f "$file_blocks"; curl -fsSL --max-filesize "$limit" -o "$out" "$url") \
      || { rm -f "$out"; return 1; }
  elif command -v wget >/dev/null 2>&1; then
    (ulimit -f "$file_blocks"; wget -qO "$out" "$url") \
      || { rm -f "$out"; return 1; }
  else
    die "need curl or wget"
  fi
  size=$(wc -c < "$out" | tr -d ' ')
  [ "$size" -le "$limit" ] || { rm -f "$out"; return 1; }
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$1" | awk '{print $NF}'
  else
    die "need sha256sum, shasum, or openssl to verify the release"
  fi
}

verify_signature() {
  checksums=$1 signature=$2 public_key=$3 work=$4
  command -v openssl >/dev/null 2>&1 || die "TERMADA_RELEASE_PUBKEY requires openssl"
  # SubjectPublicKeyInfo DER prefix for a raw 32-byte Ed25519 public key.
  printf '\060\052\060\005\006\003\053\145\160\003\041\000' > "$work/pub.der"
  printf '%s' "$public_key" | openssl base64 -d -A >> "$work/pub.der" 2>/dev/null || die "invalid TERMADA_RELEASE_PUBKEY"
  openssl base64 -d -A -in "$signature" -out "$work/signature.raw" 2>/dev/null || die "invalid checksums signature encoding"
  openssl pkeyutl -verify -pubin -keyform DER -inkey "$work/pub.der" -rawin \
    -in "$checksums" -sigfile "$work/signature.raw" >/dev/null 2>&1 || die "checksums signature verification failed"
}

build_from_source() {
  GO="${GO:-$(command -v go 2>/dev/null || echo "$HOME/.local/go/bin/go")}"
  { [ -x "$GO" ] || command -v "$GO" >/dev/null 2>&1; } || die "Go not found — install Go 1.26.5+ or use the prebuilt path (don't set TERMADA_FROM_SOURCE)"
  ROOT="$(cd "$(dirname "$0")" 2>/dev/null && pwd || echo '')"
  { [ -n "$ROOT" ] && [ -f "$ROOT/go.mod" ]; } || die "source build needs the repo — clone it and run ./install.sh from inside"
  say "Building termada from source with $GO ..."
  mkdir -p "$BIN_DIR"
  build_tmp=$(mktemp "$BIN_DIR/.termada-build.XXXXXX") || die "couldn't create install temp file in $BIN_DIR"
  if ! CGO_ENABLED=0 "$GO" build -trimpath -ldflags '-s -w' -o "$build_tmp" "$ROOT/cmd/termada"; then
    rm -f "$build_tmp"
    die "source build failed"
  fi
  chmod 755 "$build_tmp"
  mv "$build_tmp" "$BIN_DIR/termada"
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

  tmp=$(mktemp -d)
  install_tmp=""
  cleanup() {
    rm -rf "$tmp"
    [ -z "$install_tmp" ] || rm -f "$install_tmp"
  }
  trap cleanup EXIT HUP INT TERM

  ver="${TERMADA_VERSION:-}"
  if [ -z "$ver" ]; then
    fetch_file "https://api.github.com/repos/$REPO/releases/latest" "$tmp/latest.json" "$MAX_METADATA_BYTES" \
      || die "couldn't download latest release metadata"
    ver=$(sed -n 's/.*"tag_name" *: *"\([^"]*\)".*/\1/p' "$tmp/latest.json" | head -1) || true
    [ -n "$ver" ] || die "couldn't find the latest version — set TERMADA_VERSION (e.g. vX.Y.Z)"
  fi

  asset="termada_${os}_${arch}.tar.gz"
  base="https://github.com/$REPO/releases/download/$ver"
  # Fetch and authenticate release metadata before processing the archive.
  fetch_file "$base/checksums.txt" "$tmp/checksums.txt" "$MAX_CHECKSUM_BYTES" \
    || die "release has no downloadable checksums.txt; refusing an unverified install"
  matches=$(awk -v name="$asset" '$2 == name || $2 == "*" name { if (NF == 2) print $1; else print "invalid" }' "$tmp/checksums.txt")
  set -- $matches
  [ "$#" -eq 1 ] || die "checksums.txt must contain exactly one entry for $asset"
  want=$1
  [ "${#want}" -eq 64 ] || die "invalid SHA-256 entry for $asset"
  case "$want" in *[!0-9a-fA-F]*) die "invalid SHA-256 entry for $asset" ;; esac
  want=$(printf '%s' "$want" | tr 'A-F' 'a-f')

  if [ -n "${TERMADA_RELEASE_PUBKEY:-}" ]; then
    fetch_file "$base/checksums.txt.sig" "$tmp/checksums.txt.sig" "$MAX_SIGNATURE_BYTES" \
      || die "release has no checksums.txt.sig but TERMADA_RELEASE_PUBKEY is set"
    verify_signature "$tmp/checksums.txt" "$tmp/checksums.txt.sig" "$TERMADA_RELEASE_PUBKEY" "$tmp"
    say "checksums signature verified"
  fi

  say "Downloading termada $ver ($os/$arch) ..."
  fetch_file "$base/$asset" "$tmp/$asset" "$MAX_ARCHIVE_BYTES" \
    || die "download failed or exceeded size limit: $base/$asset"
  got=$(sha256_file "$tmp/$asset")
  [ "$want" = "$got" ] || die "checksum mismatch for $asset (want $want, got $got)"
  say "checksum verified"

  # Stream only the expected member after verification; never unpack arbitrary
  # archive paths into the filesystem.
  file_blocks=$((MAX_BINARY_BYTES / 512 + 1))
  (ulimit -f "$file_blocks"; tar -xOzf "$tmp/$asset" termada > "$tmp/termada") \
    || die "couldn't extract bounded termada binary from archive"
  binary_size=$(wc -c < "$tmp/termada" | tr -d ' ')
  [ "$binary_size" -gt 0 ] && [ "$binary_size" -le "$MAX_BINARY_BYTES" ] \
    || die "extracted binary is empty or exceeds size limit"
  mkdir -p "$BIN_DIR"
  install_tmp=$(mktemp "$BIN_DIR/.termada-install.XXXXXX") \
    || die "couldn't create install temp file in $BIN_DIR"
  cp "$tmp/termada" "$install_tmp"
  chmod 755 "$install_tmp"
  mv "$install_tmp" "$BIN_DIR/termada"
  install_tmp=""
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
  termada dashboard --open                         # open the dashboard in a browser
  claude mcp add --scope user termada -- termada serve --stdio  # let Claude Code use it
EOF
