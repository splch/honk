#include "textflag.h"

// cpuinit performs supervisor-mode CPU bring-up. It is the target of
// runtime/goos.CPUInit, entered immediately after OpenSBI mret's into honk in
// S-mode (a0 = hartid, a1 = DTB physical address). It mirrors the machine-mode
// tamago riscv64.Init but touches only S-level CSRs (OpenSBI owns M-mode), then
// transfers to the privilege-agnostic Go runtime entry _rt0_tamago_start.
TEXT cpuinit(SB),NOSPLIT|NOFRAME,$0
	// stash the firmware boot args before anything can clobber them:
	// a0 = hartid, a1 = DTB physical address (consumed in hwinit1).
	MOV	A0, ·hartID(SB)
	MOV	A1, ·dtbPtr(SB)

	// mask supervisor interrupts (OpenSBI leaves SIE clear; be explicit)
	MOV	$0, T0
	CSRRW	T0, SIE, ZERO

	// enable the FPU: sstatus.FS = Initial (0b01), bit 13, so the first
	// lp64d floating-point instruction does not trap
	MOV	$(1<<13), T0
	CSRRS	T0, SSTATUS, ZERO

	// stack pointer = RamStart + RamSize - RamStackOffset (top of RAM)
	MOV	runtime∕goos·RamStart(SB), X2
	MOV	runtime∕goos·RamSize(SB), T1
	MOV	runtime∕goos·RamStackOffset(SB), T2
	ADD	T1, X2
	SUB	T2, X2

	JMP	_rt0_tamago_start(SB)
