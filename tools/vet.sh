#!/usr/bin/env bash
# Run `go vet` over honk's packages under the tamago toolchain, so the
# GOOS=tamago files (the kernel and board packages) are actually analyzed.
# Mirrors build.sh's toolchain handling (tools/tamago.sh).
set -euo pipefail
cd "$(dirname "$0")/.."

# Locate the tamago-go toolchain (sets TAMAGO_GO, TAMAGO_ROOT).
. tools/tamago.sh

env \
	GOROOT="$TAMAGO_ROOT" \
	GOOS=tamago GOARCH=riscv64 GOOSPKG=github.com/usbarmory/tamago \
	"$TAMAGO_GO" vet "$@" ./kernel ./kernel/... ./board/... ./block
