# OpenSBI on QEMU virt enters the next stage at the kernel's *load base*, not at
# e_entry — and the Go linker never places _rt0 there (this is also why TamaGo's
# sifive_u board uses an nm-extracted BIOS; cf. RV64.md Appendix F #2). honk
# loads this tiny trampoline onto the runtime-unused ELF-header page at the load
# base via QEMU `-device loader`, and it jumps to the Go runtime entry.
#
# RT0_RISCV64_TAMAGO (the address of _rt0_riscv64_tamago in the linked honk ELF)
# is emitted into build/trampoline.cfg by the Makefile (go tool nm).
.include "trampoline.cfg"

.section .text
.globl _start
_start:
	li	t0, RT0_RISCV64_TAMAGO
	jr	t0
