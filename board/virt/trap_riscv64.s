// honk - QEMU virt board: S-mode trap vector, CSR and MMIO helpers.
//
// honk installs its own S-mode trap handler (TamaGo's riscv64 handler is
// M-mode and never does a proper trap return). Interrupts are serviced
// synchronously here and on return we sret; exceptions are fatal.

//go:build tamago

#include "textflag.h"

// trapEntry is installed in stvec (Direct mode) on every hart by cpuinit /
// secondaryEntry. On a trap the hardware has set sepc/scause/stval and cleared
// sstatus.SIE; we are on the interrupted context's stack with g still valid.
//
//	scause < 0 (bit 63 set) => interrupt: service it and sret.
//	scause >= 0             => exception: fatal (handleFault never returns).
//
// We save only the integer caller-saved registers and call handleIRQ, which is
// nosplit and FP-free, so the interrupted goroutine's callee-saved and FP state
// are preserved untouched.
TEXT trapEntry(SB),NOSPLIT|NOFRAME,$0
	// Switch to this hart's dedicated trap stack (sscratch holds its top, set
	// in cpuinit/secondaryEntry); sscratch now holds the interrupted sp. The
	// handler thus never touches the interrupted goroutine's stack. SIE is 0
	// during the trap, so no nested interrupt reuses sscratch.
	CSRRW	X2, SSCRATCH, X2

	ADD	$-256, SP
	MOV	T0, 16(SP)		// save T0 before we clobber it with scause
	CSRRS	ZERO, SCAUSE, T0	// T0 = scause
	BLT	T0, ZERO, interrupt	// scause < 0 => interrupt

	// exception path: diagnose and halt (never returns)
	CALL	·handleFault(SB)

interrupt:
	MOV	RA, 0(SP)
	MOV	GP, 8(SP)
	MOV	T1, 24(SP)
	MOV	T2, 32(SP)
	MOV	T3, 40(SP)
	MOV	T4, 48(SP)
	MOV	T5, 56(SP)
	MOV	T6, 64(SP)
	MOV	A0, 72(SP)
	MOV	A1, 80(SP)
	MOV	A2, 88(SP)
	MOV	A3, 96(SP)
	MOV	A4, 104(SP)
	MOV	A5, 112(SP)
	MOV	A6, 120(SP)
	MOV	A7, 128(SP)

	CALL	·handleIRQ(SB)

	MOV	0(SP), RA
	MOV	8(SP), GP
	MOV	16(SP), T0
	MOV	24(SP), T1
	MOV	32(SP), T2
	MOV	40(SP), T3
	MOV	48(SP), T4
	MOV	56(SP), T5
	MOV	64(SP), T6
	MOV	72(SP), A0
	MOV	80(SP), A1
	MOV	88(SP), A2
	MOV	96(SP), A3
	MOV	104(SP), A4
	MOV	112(SP), A5
	MOV	120(SP), A6
	MOV	128(SP), A7
	ADD	$256, SP

	// Restore the interrupted sp; sscratch goes back to the trap stack top for
	// the next trap.
	CSRRW	X2, SSCRATCH, X2
	WORD	$0x10200073		// sret (no assembler mnemonic encoding)

// func trapEntryPC() uintptr  - stvec value (4-byte aligned, Direct mode).
TEXT ·trapEntryPC(SB),NOSPLIT,$0-8
	MOV	$trapEntry(SB), T0
	MOV	T0, ret+0(FP)
	RET

// func triggerFault()  - execute EBREAK (breakpoint, scause 3) to exercise the
// fatal-exception path. OpenSBI delegates breakpoints to S-mode.
TEXT ·triggerFault(SB),NOSPLIT,$0
	WORD	$0x00100073		// ebreak
	RET

// func fence()  - full memory + I/O barrier (orders DMA setup before doorbell).
TEXT ·fence(SB),NOSPLIT,$0
	FENCE
	RET

// CSR readers (S-mode trap CSRs).
TEXT ·readScause(SB),NOSPLIT,$0-8
	CSRRS	ZERO, SCAUSE, T0
	MOV	T0, ret+0(FP)
	RET

TEXT ·readSepc(SB),NOSPLIT,$0-8
	CSRRS	ZERO, SEPC, T0
	MOV	T0, ret+0(FP)
	RET

TEXT ·readStval(SB),NOSPLIT,$0-8
	CSRRS	ZERO, STVAL, T0
	MOV	T0, ret+0(FP)
	RET

// MMIO accessors (device registers must not be cached/reordered away).
TEXT ·mmioRead8(SB),NOSPLIT,$0-9
	MOV	addr+0(FP), T0
	MOVBU	(T0), T1
	MOVB	T1, ret+8(FP)
	RET

TEXT ·mmioWrite8(SB),NOSPLIT,$0-9
	MOV	addr+0(FP), T0
	MOVBU	v+8(FP), T1
	MOVB	T1, (T0)
	RET

TEXT ·mmioRead32(SB),NOSPLIT,$0-12
	MOV	addr+0(FP), T0
	MOVWU	(T0), T1
	MOVW	T1, ret+8(FP)
	RET

TEXT ·mmioWrite32(SB),NOSPLIT,$0-12
	MOV	addr+0(FP), T0
	MOVWU	v+8(FP), T1
	MOVW	T1, (T0)
	RET
