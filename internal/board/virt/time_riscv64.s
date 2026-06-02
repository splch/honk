#include "textflag.h"

// readTime returns the platform `time` counter (RDTIME = csrrs rd, time, x0),
// always readable from S-mode (OpenSBI sets mcounteren.TM). func readTime() uint64
TEXT ·readTime(SB),NOSPLIT,$0-8
	RDTIME	T0
	MOV	T0, ret+0(FP)
	RET
