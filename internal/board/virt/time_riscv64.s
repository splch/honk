#include "textflag.h"

// readTime returns the platform `time` counter (RDTIME = csrrs rd, time, x0),
// always readable from S-mode (OpenSBI sets mcounteren.TM). func readTime() uint64
TEXT ·readTime(SB),NOSPLIT,$0-8
	RDTIME	T0
	MOV	T0, ret+0(FP)
	RET

// setSTimecmp writes stimecmp (CSR 0x14D, Sstc): csrrw x0, stimecmp, t0.
// The Go riscv64 assembler has no symbolic name for this CSR, so the
// instruction is emitted as a raw word. func setSTimecmp(ticks uint64)
TEXT ·setSTimecmp(SB),NOSPLIT,$0-8
	MOV	ticks+0(FP), T0	// T0 = x5 = rs1
	WORD	$0x14d29073	// csrrw x0, 0x14D, t0
	RET
