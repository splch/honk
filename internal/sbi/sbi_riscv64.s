#include "textflag.h"

// func call(eid, fid, a0, a1, a2 uintptr) (err, val int)
//
// SBI ABI (RV64.md §2.1): a7=EID, a6=FID, a0-a5 args; returns a0=error, a1=value.
// All registers except a0/a1 are preserved by the firmware across the ecall.
TEXT ·call(SB),NOSPLIT,$0-56
	MOV	eid+0(FP), A7
	MOV	fid+8(FP), A6
	MOV	a0+16(FP), A0
	MOV	a1+24(FP), A1
	MOV	a2+32(FP), A2
	ECALL
	MOV	A0, err+40(FP)
	MOV	A1, val+48(FP)
	RET
