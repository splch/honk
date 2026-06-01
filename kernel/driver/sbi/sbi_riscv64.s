// SBI legacy console ecalls for the noos/riscv64 target. (On other targets the
// stubs in sbi_stub.go stand in so the package still builds and tests on the
// host.) Mirrors the proven sbiPutchar in the toolchain runtime patch: the char
// goes in a0=X10, the SBI extension id in a7=X17, and ecall traps to OpenSBI.

//go:build noos

#include "textflag.h"

// func putchar(c byte)
TEXT ·putchar(SB),NOSPLIT|NOFRAME,$0-1
	MOVBU	c+0(FP), X10	// a0 = c
	MOV	$1, X17		// a7 = SBI EID console_putchar
	ECALL
	RET

// func getchar() int
TEXT ·getchar(SB),NOSPLIT|NOFRAME,$0-8
	MOV	$2, X17		// a7 = SBI EID console_getchar
	ECALL
	MOV	X10, ret+0(FP)	// a0 = the byte, or -1 when no input is ready
	RET
