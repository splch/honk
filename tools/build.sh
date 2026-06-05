#!/usr/bin/env bash
# Build the honk kernel ELF (with the TamaGo Go distribution) and the boot
# trampoline that lets OpenSBI's fw_dynamic enter it (see tools/mkboot).
#
# The tamago-go toolchain must be invoked directly (the `go run .../cmd/tamago`
# wrapper can't be used with GOOS=tamago set, since that makes the host `go`
# try to build the wrapper itself for tamago). We locate the installed
# toolchain, auto-installing it via the host `go` on first use.
set -euo pipefail
cd "$(dirname "$0")/.."

# honk links above the boot trampoline (which OpenSBI enters at 0x80200000).
TEXT_ADDR=0x80400000

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

# The tamago-go binary must use its own GOROOT; override any inherited GOROOT
# (e.g. mise/asdf set one globally, which otherwise selects the host compile).
TAMAGO_ROOT="$(cd "$(dirname "$TAMAGO_GO")/.." && pwd)"

env \
	GOROOT="$TAMAGO_ROOT" \
	GOOS=tamago GOARCH=riscv64 GOOSPKG=github.com/usbarmory/tamago \
	"$TAMAGO_GO" build \
	-trimpath \
	-ldflags "-T $TEXT_ADDR -R 0x1000" \
	-o honk.elf \
	./kernel

# Emit the 24-byte boot trampoline (jumps to honk.elf's entry point).
go run ./tools/mkboot honk.elf boot.bin
