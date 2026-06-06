#!/usr/bin/env bash
# Build the honk kernel ELF (with the TamaGo Go distribution) and the boot
# trampoline that lets OpenSBI's fw_dynamic enter it (see tools/mkboot).
set -euo pipefail
cd "$(dirname "$0")/.."

# honk links above the boot trampoline (which OpenSBI enters at 0x80200000).
TEXT_ADDR=0x80400000

# Anti-rollback security version stamped into the core image (override to test
# A/B selection of a newer image).
CORE_VERSION="${CORE_VERSION:-1}"

# Build the immutable core image (signed + Merkle-tree'd) that the kernel embeds
# as its factory image. Forced to the host toolchain (the kernel build below
# runs under GOOS=tamago, which cannot build this host tool).
env -u GOOS -u GOARCH -u GOOSPKG \
	go run ./tools/mkimage -version "$CORE_VERSION" kernel/core kernel/core.img

# Locate the tamago-go toolchain (sets TAMAGO_GO, TAMAGO_ROOT).
. tools/tamago.sh

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
