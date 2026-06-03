#include "textflag.h"

// enableTimerIRQ sets sie.STIE so a pending supervisor-timer interrupt can wake
// `wfi`. honk keeps sstatus.SIE = 0, so the timer never actually traps; per the
// RISC-V spec, WFI still completes on any sie-enabled pending interrupt
// regardless of the global enable, and execution simply resumes after it.
TEXT ·enableTimerIRQ(SB),NOSPLIT,$0
	MOV	$(1<<5), T0
	CSRRS	T0, SIE, ZERO
	RET

// enableExtIRQ sets sie.SEIE so a pending S-external (PLIC) interrupt can wake
// `wfi`, exactly as enableTimerIRQ does for the timer (sstatus.SIE stays 0, so
// the interrupt never traps — idle drains it).
TEXT ·enableExtIRQ(SB),NOSPLIT,$0
	MOV	$(1<<9), T0
	CSRRS	T0, SIE, ZERO
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
