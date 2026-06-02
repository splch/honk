# Minimal M-mode reset trampoline for the QEMU sifive_u board (Phase 0).
#
# QEMU's sifive_u boot ROM enters this image in M-mode at the link address
# (0x80000000, see bios.ld); we jump straight to TamaGo's RISC-V entry point.
# RT0_RISCV64_TAMAGO is the address of _rt0_riscv64_tamago in the linked honk
# ELF, emitted into build/bios.cfg by the Makefile (go tool nm).
#
# Phase 1 deletes this entirely: on the virt board, OpenSBI is the firmware and
# mret's into honk's ELF entry in S-mode (see DESIGN.md section 4, RV64.md Part 1).
.align 2
.include "bios.cfg"

.section .text
.globl _start
_start:
	li	t0, RT0_RISCV64_TAMAGO
	jr	t0
