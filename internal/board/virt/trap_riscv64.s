#include "textflag.h"

// setTrapVector installs trapVector as the S-mode trap handler (stvec, Direct
// mode — a Go function entry is naturally aligned, so the low MODE bits are 0).
TEXT ·setTrapVector(SB),NOSPLIT,$0
	MOV	$·trapVector(SB), T0
	CSRRW	T0, STVEC, ZERO
	RET

// enableTimerIRQ sets sie.STIE so a pending supervisor-timer interrupt can wake
// `wfi`. honk keeps sstatus.SIE = 0, so the timer never actually traps; per the
// RISC-V spec, WFI still completes on any sie-enabled pending interrupt
// regardless of the global enable, and execution simply resumes after it.
TEXT ·enableTimerIRQ(SB),NOSPLIT,$0
	MOV	$(1<<5), T0
	CSRRS	T0, SIE, ZERO
	RET

// wfi waits for an interrupt (low-power idle). WFI has no Go asm mnemonic.
TEXT ·wfi(SB),NOSPLIT,$0
	WORD	$0x10500073 // wfi
	RET

// func readSCause() uint64
TEXT ·readSCause(SB),NOSPLIT,$0-8
	CSRRS	ZERO, SCAUSE, T0
	MOV	T0, ret+0(FP)
	RET

// func readSEPC() uint64
TEXT ·readSEPC(SB),NOSPLIT,$0-8
	CSRRS	ZERO, SEPC, T0
	MOV	T0, ret+0(FP)
	RET

// func readSTVAL() uint64
TEXT ·readSTVAL(SB),NOSPLIT,$0-8
	CSRRS	ZERO, STVAL, T0
	MOV	T0, ret+0(FP)
	RET
