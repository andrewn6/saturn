#!/usr/bin/env sh
# Saturn installer — fetches the latest release for the host OS/arch.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/andrewn6/saturn/main/install.sh | sh
#
# Override:
#   VERSION=v0.2.0 sh install.sh
#   INSTALL_DIR=$HOME/.local/bin sh install.sh

set -eu

REPO="andrewn6/saturn"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) echo "saturn: unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

case "$OS" in
    linux|darwin) ;;
    *) echo "saturn: unsupported OS: $OS" >&2; exit 1 ;;
esac

if [ -z "${VERSION:-}" ]; then
    VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
        | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)"
fi
if [ -z "${VERSION:-}" ]; then
    echo "saturn: could not determine latest version (rate-limited?)" >&2
    echo "       try: VERSION=v0.1.0 sh install.sh" >&2
    exit 1
fi

ASSET="saturn_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$VERSION/$ASSET"

echo "Saturn $VERSION -> $INSTALL_DIR/saturn"
echo "  $URL"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

curl -fsSL "$URL" | tar -xz -C "$TMP"

if [ ! -f "$TMP/saturn" ]; then
    echo "saturn: archive missing 'saturn' binary" >&2
    exit 1
fi

if [ -w "$INSTALL_DIR" ]; then
    install -m 0755 "$TMP/saturn" "$INSTALL_DIR/saturn"
else
    echo "  (sudo required to write to $INSTALL_DIR)"
    sudo install -m 0755 "$TMP/saturn" "$INSTALL_DIR/saturn"
fi

echo
echo "Installed. Verify with: saturn"
echo
echo "Runtime requirements:"
echo "  - tmux              required for the watch UI's attach feature"
echo "  - claude / opencode at least one for actual agent runs"
echo
