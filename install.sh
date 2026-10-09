#!/bin/sh
# headroom installer for Linux x64 and macOS arm64.
#
#   curl -fsSL https://raw.githubusercontent.com/sigmashakeinc/headroom/main/install.sh | sh
#
# Environment:
#   VERSION      Release tag to install (for example v0.1.0). Default: latest.
#   INSTALL_DIR  Install directory. Default: $HOME/.local/bin.
#   HEADROOM_CHECKSUMS_FILE  Same as --checksums.
#
# Options:
#   --from-file <archive>  Install from a local .tar.gz. No download.
#   --checksums <file>     With --from-file: verify the archive sha256 against
#                          the entry for its file name. Fail on mismatch or
#                          missing entry. Without it, verification is skipped.
#   -h, --help             Show this help.
set -eu

REPO="sigmashakeinc/headroom"
BASE="https://github.com/${REPO}"

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: error: %s\n' "$*" >&2; exit 1; }

FROM_FILE=""
CHECKSUMS_FILE="${HEADROOM_CHECKSUMS_FILE:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --from-file)
      [ $# -ge 2 ] || die "--from-file needs a path"
      FROM_FILE="$2"
      shift 2
      ;;
    --checksums)
      [ $# -ge 2 ] || die "--checksums needs a path"
      CHECKSUMS_FILE="$2"
      shift 2
      ;;
    -h|--help)
      sed -n '2,16p' "$0" 2>/dev/null || true
      exit 0
      ;;
    *)
      die "unknown option: $1"
      ;;
  esac
done

INSTALL_DIR="${INSTALL_DIR:-${HOME:?HOME is not set}/.local/bin}"

need() { command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"; }
need tar
need uname
need mktemp

fetch() { # fetch <url> <dest>
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    die "need curl or wget"
  fi
}

sha256_of() { # sha256_of <file>
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d ' ' -f 1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d ' ' -f 1
  else
    die "need sha256sum or shasum"
  fi
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

if [ -n "$FROM_FILE" ]; then
  [ -f "$FROM_FILE" ] || die "file not found: $FROM_FILE"
  ARCHIVE="$FROM_FILE"
  say "Installing from local archive: $FROM_FILE"
  if [ -n "$CHECKSUMS_FILE" ]; then
    [ -f "$CHECKSUMS_FILE" ] || die "checksums file not found: $CHECKSUMS_FILE"
    NAME="$(basename "$FROM_FILE")"
    WANT="$(awk -v n="$NAME" '$2 == n || $2 == "*" n { print $1; exit }' "$CHECKSUMS_FILE")"
    [ -n "$WANT" ] || die "no checksum listed for $NAME. Nothing was installed."
    GOT="$(sha256_of "$ARCHIVE")"
    if [ "$WANT" != "$GOT" ]; then
      die "checksum mismatch for $NAME (expected $WANT, got $GOT). Nothing was installed."
    fi
    say "Checksum OK."
  else
    printf 'install.sh: warning: no checksums file given. Verification of the local archive is skipped.\n' >&2
  fi
else
  raw_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  raw_arch="$(uname -m)"
  case "$raw_arch" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) ARCH="$raw_arch" ;;
  esac
  case "$raw_os/$ARCH" in
    linux/amd64)  OS=linux ;;
    darwin/arm64) OS=darwin ;;
    *)
      printf 'headroom: unsupported platform %s/%s; supported: linux x64, macOS arm64, Windows x64\n' "$raw_os" "$ARCH" >&2
      exit 1
      ;;
  esac

  TAG="${VERSION:-}"
  if [ -z "$TAG" ]; then
    if command -v curl >/dev/null 2>&1; then
      TAG="$(curl -fsSL -o /dev/null -w '%{url_effective}' "${BASE}/releases/latest" | sed 's|.*/tag/||')"
    else
      need wget
      TAG="$(wget -q --max-redirect=0 -S -O /dev/null "${BASE}/releases/latest" 2>&1 | sed -n 's|.*[Ll]ocation: .*/tag/\(.*\)|\1|p' | tr -d '\r' | head -n 1)"
    fi
    [ -n "$TAG" ] || die "cannot resolve the latest release"
  fi

  NAME="headroom_${TAG}_${OS}_${ARCH}.tar.gz"
  say "Installing headroom ${TAG} (${OS}/${ARCH})"
  ARCHIVE="$TMP/$NAME"
  fetch "${BASE}/releases/download/${TAG}/${NAME}" "$ARCHIVE" || die "download failed: $NAME"
  fetch "${BASE}/releases/download/${TAG}/checksums.txt" "$TMP/checksums.txt" || die "download failed: checksums.txt"

  WANT="$(awk -v n="$NAME" '$2 == n || $2 == "*" n { print $1; exit }' "$TMP/checksums.txt")"
  [ -n "$WANT" ] || die "no checksum listed for $NAME"
  GOT="$(sha256_of "$ARCHIVE")"
  if [ "$WANT" != "$GOT" ]; then
    die "checksum mismatch for $NAME (expected $WANT, got $GOT). Nothing was installed."
  fi
  say "Checksum OK."
fi

mkdir -p "$TMP/x"
tar -xzf "$ARCHIVE" -C "$TMP/x" || die "cannot extract archive"
[ -f "$TMP/x/headroom" ] || die "archive does not contain a headroom binary"

mkdir -p "$INSTALL_DIR"
cp "$TMP/x/headroom" "$INSTALL_DIR/headroom.new"
chmod 755 "$INSTALL_DIR/headroom.new"
mv -f "$INSTALL_DIR/headroom.new" "$INSTALL_DIR/headroom"
say "Installed: $INSTALL_DIR/headroom"

case ":${PATH:-}:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    say ""
    say "$INSTALL_DIR is not on your PATH. Add this line to your shell profile:"
    say "  export PATH=\"$INSTALL_DIR:\$PATH\""
    ;;
esac
