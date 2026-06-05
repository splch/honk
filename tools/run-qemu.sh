#!/usr/bin/env bash
# Run honk under QEMU virt: OpenSBI in M-mode, honk as the HS-mode payload.
#
# OpenSBI's fw_dynamic enters the boot trampoline (boot.bin) at 0x80200000,
# which jumps to honk.elf (loaded by -device loader at its link address).
#
#   MEM   QEMU RAM size   (default 512M; must match board/virt/mem.go ramSize)
#   SMP   number of harts (default 4)
#
# Quit an interactive session with Ctrl-A then x.
set -euo pipefail
cd "$(dirname "$0")/.."

{ [ -f boot.bin ] && [ -f honk.elf ]; } || tools/build.sh

exec qemu-system-riscv64 \
	-machine virt \
	-cpu rv64,h=true \
	-smp "${SMP:-4}" \
	-m "${MEM:-512M}" \
	-nographic \
	-bios default \
	-no-reboot \
	-kernel boot.bin \
	-device loader,file=honk.elf
