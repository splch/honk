#include "textflag.h"

// func r8(addr uintptr) uintptr
TEXT ·r8(SB),NOSPLIT,$0-16
	MOV	addr+0(FP), T0
	MOVBU	(T0), T1
	MOV	T1, ret+8(FP)
	RET

// func r32(addr uintptr) uintptr
TEXT ·r32(SB),NOSPLIT,$0-16
	MOV	addr+0(FP), T0
	MOVWU	(T0), T1
	MOV	T1, ret+8(FP)
	RET

// func w8(addr, v uintptr)
TEXT ·w8(SB),NOSPLIT,$0-16
	MOV	addr+0(FP), T0
	MOV	v+8(FP), T1
	MOVB	T1, (T0)
	RET

// func w32(addr, v uintptr)
TEXT ·w32(SB),NOSPLIT,$0-16
	MOV	addr+0(FP), T0
	MOV	v+8(FP), T1
	MOVW	T1, (T0)
	RET
