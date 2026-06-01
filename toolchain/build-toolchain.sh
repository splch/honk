#!/usr/bin/env bash
# Build the patched Embedded Go toolchain for Honk OS into ./.toolchain/go.
#
# It clones the embeddedgo/go fork at a pinned tag, applies the Honk S-mode
# runtime patch (honk-smode-go1.24.5.patch), and runs make.bash. An existing Go
# (>=1.22) is required as the bootstrap compiler.
set -euo pipefail

REPO=https://github.com/embeddedgo/go
TAG=go1.24.5-embedded

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
DEST="$ROOT/.toolchain/go"
PATCH="$HERE/honk-smode-go1.24.5.patch"

if [ -x "$DEST/bin/go" ]; then
	echo "Honk toolchain already built: $DEST/bin/go"
	exit 0
fi
command -v go  >/dev/null || { echo "error: need an existing Go (>=1.22) as bootstrap" >&2; exit 1; }
command -v git >/dev/null || { echo "error: need git" >&2; exit 1; }

echo ">> cloning $REPO @ $TAG"
rm -rf "$DEST"
mkdir -p "$ROOT/.toolchain"
git clone --depth 1 -b "$TAG" "$REPO" "$DEST"

echo ">> applying $(basename "$PATCH")"
git -C "$DEST" apply "$PATCH"

echo ">> building toolchain (make.bash; this takes a few minutes)"
( cd "$DEST/src" && GOROOT_BOOTSTRAP="$(go env GOROOT)" ./make.bash )

echo ">> done: $DEST/bin/go"
