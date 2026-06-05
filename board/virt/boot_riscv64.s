// honk - QEMU virt board: RISC-V 64 HS-mode startup, SMP, and SBI calls.
//
// Built on the TamaGo runtime (github.com/usbarmory/tamago); honk supplies the
// HS-mode startup and the runtime/goos overlay this file is part of.

//go:build tamago

#include "textflag.h"

// cpuinit is the first honk instruction stream executed on the boot hart.
//
// OpenSBI's fw_dynamic enters the boot trampoline (tools/mkboot) in HS-mode;
// the trampoline jumps to honk's ELF entry (_rt0_riscv64_tamago), which jumps
// to goos.CPUInit, whose riscv64 stub jumps here. The SBI boot contract (a0/a1
// preserved through the trampoline) hands us:
//
//	X10 (a0) = boot hartid
//	X11 (a1) = physical address of the device tree blob (DTB)
//	sstatus.SIE = 0, sie = 0, satp = 0 (paging off / bare addressing)
//
// We run in HS-mode under OpenSBI, so we must NOT touch any M-mode CSR
// (mstatus/mie/mtvec/...): OpenSBI owns them and the access would trap. This
// is the whole reason honk supplies its own cpuinit instead of reusing
// tamago/riscv64's M-mode one (built out via the absence of its package).
TEXT cpuinit(SB),NOSPLIT|NOFRAME,$0
	// Stash the OpenSBI boot arguments before the runtime bring-up clobbers
	// a0/a1. These are honk-owned globals, so the runtime never touches them.
	MOV	X10, ·bootHartID(SB)
	MOV	X11, ·bootDTB(SB)

	// Record this hart's id in tp. CurrentHart()/goos.ProcID read tp, and the
	// Go runtime never touches it (X4 is skipped throughout the runtime).
	MOV	X10, TP

	// Enable the FPU: sstatus.FS = Initial (0b01 << 13). The lp64d ABI emits
	// hard-float instructions and the reset state of FS is Off, so the first
	// FP op would otherwise raise an illegal-instruction trap.
	MOV	$(1<<13), T0
	CSRRS	T0, SSTATUS, ZERO

	// Install the S-mode trap vector and enable external interrupts on the
	// boot hart. No source fires until InitConsole programs the PLIC/UART, so
	// this is safe even before the Go runtime is up.
	MOV	$trapEntry(SB), T0
	CSRRW	T0, STVEC, ZERO
	MOV	$(1<<9), T0
	CSRRS	T0, SIE, ZERO		// sie.SEIE (S-mode external)
	MOV	$(1<<1), T0
	CSRRS	T0, SSTATUS, ZERO	// sstatus.SIE (global S-mode interrupts)

	// Boot stack pointer at the top of usable RAM:
	//	sp = RamStart + RamSize - RamStackOffset
	MOV	runtime∕goos·RamStart(SB), X2
	MOV	runtime∕goos·RamSize(SB), T1
	MOV	runtime∕goos·RamStackOffset(SB), T2
	ADD	T1, X2
	SUB	T2, X2

	// Hand off to the privilege-agnostic Go runtime bring-up (sets up g0/m0,
	// runs hwinit0/osinit/schedinit/hwinit1, then starts main).
	JMP	_rt0_tamago_start(SB)

// secondaryEntry is where each non-boot hart begins, started via SBI HSM
// hart_start (see smp.go). The SBI HSM contract hands us X10 (a0) = hartid and
// X11 (a1) = opaque, in HS-mode with satp=0, SIE=0, and an UNDEFINED stack -
// so this code uses only registers and memory until it adopts a runtime stack.
//
// It parks the hart in a spin loop until honkTask (smp.go) publishes a Go M
// (stack, g, and runtime.mstart) in this hart's slot, then adopts them and
// jumps into mstart, becoming a full Go scheduler M. This mirrors tamago/amd64
// ·apstart and needs no runtime fork - goos.Task is the supported hook.
TEXT secondaryEntry(SB),NOSPLIT|NOFRAME,$0
	// per-hart id in tp (as cpuinit does for the boot hart)
	MOV	X10, TP

	// enable the FPU (sstatus.FS = Initial)
	MOV	$(1<<13), T0
	CSRRS	T0, SSTATUS, ZERO

	// install the trap vector for exception safety (no interrupts enabled on
	// secondary harts: only the boot hart services the UART)
	MOV	$trapEntry(SB), T0
	CSRRW	T0, STVEC, ZERO

	SLLI	$2, X10, T4		// T4 = hartid*4 (uint32 index)
	SLLI	$3, X10, T5		// T5 = hartid*8 (uint64 index)

	// readyFlag[hartid] = 1, then fence so the boot hart observes readiness
	MOV	$·readyFlag(SB), T0
	ADD	T4, T0
	MOV	$1, T1
	MOVW	T1, (T0)
	FENCE

park:
	// spin until taskPC[hartid] != 0
	MOV	$·taskPC(SB), T0
	ADD	T5, T0
	MOV	(T0), T3		// T3 = mstart PC
	BEQ	T3, ZERO, park
	FENCE				// acquire: see the taskSP/taskGP writes

	// adopt the runtime-provided stack and g0
	MOV	$·taskSP(SB), T0
	ADD	T5, T0
	MOV	(T0), X2		// SP = taskSP[hartid]

	MOV	$·taskGP(SB), T0
	ADD	T5, T0
	MOV	(T0), g			// g  = taskGP[hartid] (X27)

	JMP	(T3)			// -> runtime.mstart, never returns

// func secondaryEntryPC() uintptr  - physical entry for SBI HSM hart_start.
TEXT ·secondaryEntryPC(SB),NOSPLIT,$0-8
	MOV	$secondaryEntry(SB), T0
	MOV	T0, ret+0(FP)
	RET

// func readtp() uint64  - the current hart id, stashed in tp at hart entry.
TEXT ·readtp(SB),NOSPLIT,$0-8
	MOV	TP, T0
	MOV	T0, ret+0(FP)
	RET

// func sbiPutchar(c byte)
//
// SBI legacy console_putchar (EID 0x01): one byte per ecall, available on
// every SBI implementation. Used as the early console before a UART driver.
TEXT ·sbiPutchar(SB),NOSPLIT,$0-1
	MOVBU	c+0(FP), X10		// a0 = character
	MOV	$1, X17			// a7 = EID 0x01 (console_putchar)
	ECALL
	RET

// func sbiCall(eid, fid, arg0, arg1, arg2 uint64) (err int64, val int64)
//
// Generic SBI v0.2+ ecall: EID in a7, FID in a6, args in a0-a2, returning
// a0=error, a1=value. All registers except a0/a1 are preserved by the SEE.
TEXT ·sbiCall(SB),NOSPLIT,$0-56
	MOV	eid+0(FP), X17		// a7 = EID
	MOV	fid+8(FP), X16		// a6 = FID
	MOV	arg0+16(FP), X10	// a0
	MOV	arg1+24(FP), X11	// a1
	MOV	arg2+32(FP), X12	// a2
	ECALL
	MOV	X10, err+40(FP)
	MOV	X11, val+48(FP)
	RET

// func readTime() uint64
//
// Reads the time CSR (rdtime). Always readable from S-mode under OpenSBI
// (mcounteren.TM is set by firmware). On QEMU virt the timebase is 10 MHz.
TEXT ·readTime(SB),NOSPLIT,$0-8
	RDTIME	X10
	MOV	X10, ret+0(FP)
	RET
