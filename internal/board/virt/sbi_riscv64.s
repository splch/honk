#include "textflag.h"

// SBI is the firmware ABI: an ECALL traps S->M into OpenSBI (see RV64.md Part 2).
// a7 = extension ID, a6 = function ID, a0-a5 = args.

// sbiPutchar writes one byte to the SBI console (legacy console_putchar,
// EID 0x01). func sbiPutchar(c byte)
TEXT ·sbiPutchar(SB),NOSPLIT,$0-1
	MOVBU	c+0(FP), A0
	MOV	$1, A7
	ECALL
	RET

// sbiShutdown powers the machine off (legacy SBI shutdown, EID 0x08); it does
// not return. func sbiShutdown()
TEXT ·sbiShutdown(SB),NOSPLIT,$0-0
	MOV	$8, A7
	ECALL
	RET

// readTime returns the platform `time` counter (RDTIME = csrrs rd, time, x0),
// always readable from S-mode (OpenSBI sets mcounteren.TM). func readTime() uint64
TEXT ·readTime(SB),NOSPLIT,$0-8
	RDTIME	T0
	MOV	T0, ret+0(FP)
	RET
