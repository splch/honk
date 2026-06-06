#!/usr/bin/env bash
# Locate the installed tamago-go toolchain (auto-installing via the host go on
# first use) and export TAMAGO_GO (its `go` binary) and TAMAGO_ROOT (its
# GOROOT). Sourced by build.sh and vet.sh.
#
# The `go run .../cmd/tamago` wrapper cannot be used with GOOS=tamago set (the
# host go would try to build the wrapper itself for tamago), so we invoke the
# installed toolchain binary directly. It must also run with its own GOROOT:
# callers export GOROOT="$TAMAGO_ROOT" to override any inherited one (mise/asdf
# set one globally, which otherwise selects the host compiler).

find_tamago() {
	[ -n "${TAMAGO:-}" ] && { echo "$TAMAGO"; return; }
	ls -d "$HOME"/Library/Caches/tamago-go/*/bin/go \
	      "$HOME"/.cache/tamago-go/*/bin/go 2>/dev/null | sort | tail -1
}

TAMAGO_GO="$(find_tamago || true)"
if [ -z "$TAMAGO_GO" ] || [ ! -x "$TAMAGO_GO" ]; then
	echo "tamago-go not found; installing via host go (one-time)..." >&2
	go run github.com/usbarmory/tamago/cmd/tamago version >/dev/null
	TAMAGO_GO="$(find_tamago || true)"
fi
[ -x "$TAMAGO_GO" ] || { echo "error: could not locate tamago-go binary" >&2; exit 1; }

TAMAGO_ROOT="$(cd "$(dirname "$TAMAGO_GO")/.." && pwd)"
