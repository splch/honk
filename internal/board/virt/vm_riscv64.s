#include "textflag.h"

// setSATP switches on Sv39 translation. The sfence.vma before drains pending
// page-table writes; the one after flushes stale/cached (including invalid)
// entries (RV64.md §5.2, §5.4). honk is identity-mapped, so the instruction
// fetched after the satp write is at the same address and stays mapped.
// func setSATP(satp uint64)
TEXT ·setSATP(SB),NOSPLIT,$0-8
	MOV	satp+0(FP), T0
	WORD	$0x12000073 // sfence.vma
	CSRRW	T0, SATP, ZERO
	WORD	$0x12000073 // sfence.vma
	RET

// func readSATP() uint64
TEXT ·readSATP(SB),NOSPLIT,$0-8
	CSRRS	ZERO, SATP, T0
	MOV	T0, ret+0(FP)
	RET
