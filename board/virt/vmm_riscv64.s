// honk - QEMU virt board: H-extension world switch (HS <-> VS) and the H/VS
// CSR accessors the VMM needs (M11). See vmm.go for the run loop and design.
//
// The world switch follows the classic trampoline shape (cf. xv6's user trap
// vector): guestEnter saves honk's HS context into the vcpu frame, publishes
// the frame pointer in sscratch, loads the guest's 31 GPRs, and `sret`s into
// VS-mode. When the guest traps back to HS, guestVec (installed in stvec for
// the duration of the run) saves the guest GPRs into the same frame, restores
// honk's HS context, and returns - so guestEnter "returns" to its Go caller via
// guestVec. No stack is used on the trap path (all memory goes through the
// frame pointer in a temp), so guestVec needs no stack switch.
//
// Go's riscv64 ABIInternal has no callee-saved general registers; the only
// state a Go caller relies on across the call is SP, GP, TP, and g (X27), plus
// RA to return. So those five are the entire HS context saved/restored here;
// the guest's values for them round-trip through the frame like any other GPR.

//go:build tamago && riscv64

#include "textflag.h"

// vcpu frame layout (must match the Go struct in vmm.go):
//   gpr[0..31] at 0..248  (x0 slot unused; gpr[i] at 8*i)
//   pc         at 256     (guest sepc: entry value in, exit value out)
//   hsRA       at 264
//   hsSP       at 272
//   hsGP       at 280
//   hsTP       at 288
//   hsG        at 296

// func guestEnter(v *vcpu)
//
// Enter the guest. sepc/hstatus/hgatp/vsatp/sstatus.SPP are already programmed
// by the Go caller; this routine only swaps the integer register file and srets.
TEXT ·guestEnter(SB),NOSPLIT,$0-8
	MOV	v+0(FP), T0		// T0 = frame pointer (read before clobbering SP)

	// Save honk's HS context so guestVec can return to our Go caller.
	MOV	RA, 264(T0)
	MOV	SP, 272(T0)
	MOV	GP, 280(T0)
	MOV	TP, 288(T0)
	MOV	g,  296(T0)

	// Publish the frame in sscratch so guestVec can find it on the guest trap.
	// (sscratch normally holds this hart's trap-stack top; the Go caller
	// snapshots and restores it around the run, and honk's own trap vector is
	// not installed while the guest runs.)
	CSRRW	T0, SSCRATCH, ZERO

	// Load the guest's 31 GPRs from the frame. T0 (x5) is the base, so it is
	// loaded last; x0 is hardwired zero and skipped.
	MOV	8(T0), RA		// x1
	MOV	16(T0), SP		// x2
	MOV	24(T0), GP		// x3
	MOV	32(T0), TP		// x4
	MOV	48(T0), T1		// x6
	MOV	56(T0), T2		// x7
	MOV	64(T0), S0		// x8
	MOV	72(T0), S1		// x9
	MOV	80(T0), A0		// x10
	MOV	88(T0), A1		// x11
	MOV	96(T0), A2		// x12
	MOV	104(T0), A3		// x13
	MOV	112(T0), A4		// x14
	MOV	120(T0), A5		// x15
	MOV	128(T0), A6		// x16
	MOV	136(T0), A7		// x17
	MOV	144(T0), S2		// x18
	MOV	152(T0), S3		// x19
	MOV	160(T0), S4		// x20
	MOV	168(T0), S5		// x21
	MOV	176(T0), S6		// x22
	MOV	184(T0), S7		// x23
	MOV	192(T0), S8		// x24
	MOV	200(T0), S9		// x25
	MOV	208(T0), S10		// x26
	MOV	216(T0), g		// x27
	MOV	224(T0), T3		// x28
	MOV	232(T0), T4		// x29
	MOV	240(T0), T5		// x30
	MOV	248(T0), T6		// x31
	MOV	40(T0), T0		// x5 last (drops the base; sret follows)

	WORD	$0x10200073		// sret -> VS-mode (V=1) at sepc
	RET				// unreachable (sret does not return here)

// guestVec is installed in stvec while the guest runs. On a VS->HS trap the
// hardware has set sepc/scause/stval/htval and switched to HS; this saves the
// guest GPRs into the frame and returns into guestEnter's Go caller.
TEXT guestVec(SB),NOSPLIT|NOFRAME,$0
	// Swap in the frame pointer: T6 = frame, sscratch = guest's x31.
	CSRRW	T6, SSCRATCH, T6

	// Save guest x1..x30 (x0 skipped; x31 lives in sscratch for now). T6 is the
	// base; T5 (x30) is saved here before being reused as a scratch below.
	MOV	RA, 8(T6)		// x1
	MOV	SP, 16(T6)		// x2
	MOV	GP, 24(T6)		// x3
	MOV	TP, 32(T6)		// x4
	MOV	T0, 40(T6)		// x5
	MOV	T1, 48(T6)		// x6
	MOV	T2, 56(T6)		// x7
	MOV	S0, 64(T6)		// x8
	MOV	S1, 72(T6)		// x9
	MOV	A0, 80(T6)		// x10
	MOV	A1, 88(T6)		// x11
	MOV	A2, 96(T6)		// x12
	MOV	A3, 104(T6)		// x13
	MOV	A4, 112(T6)		// x14
	MOV	A5, 120(T6)		// x15
	MOV	A6, 128(T6)		// x16
	MOV	A7, 136(T6)		// x17
	MOV	S2, 144(T6)		// x18
	MOV	S3, 152(T6)		// x19
	MOV	S4, 160(T6)		// x20
	MOV	S5, 168(T6)		// x21
	MOV	S6, 176(T6)		// x22
	MOV	S7, 184(T6)		// x23
	MOV	S8, 192(T6)		// x24
	MOV	S9, 200(T6)		// x25
	MOV	S10, 208(T6)		// x26
	MOV	g,  216(T6)		// x27
	MOV	T3, 224(T6)		// x28
	MOV	T4, 232(T6)		// x29
	MOV	T5, 240(T6)		// x30

	CSRRS	ZERO, SSCRATCH, T5	// T5 = guest x31 (parked in sscratch)
	MOV	T5, 248(T6)
	CSRRS	ZERO, SEPC, T5		// T5 = guest pc
	MOV	T5, 256(T6)

	// Restore honk's HS context and return into guestEnter's Go caller.
	MOV	264(T6), RA
	MOV	272(T6), SP
	MOV	280(T6), GP
	MOV	288(T6), TP
	MOV	296(T6), g
	RET

// func guestVecPC() uintptr  - stvec value for the guest run (4-byte aligned,
// Direct mode), installed by the Go caller around guestEnter.
TEXT ·guestVecPC(SB),NOSPLIT,$0-8
	MOV	$guestVec(SB), T0
	MOV	T0, ret+0(FP)
	RET

// func hfenceGVMA()  - HFENCE.GVMA x0, x0: order/flush G-stage translations
// after writing hgatp (required when hgatp.MODE changes). No assembler
// mnemonic, so emit the encoding directly.
TEXT ·hfenceGVMA(SB),NOSPLIT,$0
	WORD	$0x62000073		// hfence.gvma zero, zero
	RET

// H/VS CSR accessors. The Go assembler knows the H/VS CSR names, so these use
// named CSRRW/CSRRS (rs1, csr, rd order, per honk's trap_riscv64.s).

TEXT ·writeHgatp(SB),NOSPLIT,$0-8
	MOV	v+0(FP), T0
	CSRRW	T0, HGATP, ZERO
	RET

TEXT ·writeVsatp(SB),NOSPLIT,$0-8
	MOV	v+0(FP), T0
	CSRRW	T0, VSATP, ZERO
	RET

TEXT ·writeHstatus(SB),NOSPLIT,$0-8
	MOV	v+0(FP), T0
	CSRRW	T0, HSTATUS, ZERO
	RET

TEXT ·readHstatus(SB),NOSPLIT,$0-8
	CSRRS	ZERO, HSTATUS, T0
	MOV	T0, ret+0(FP)
	RET

TEXT ·writeSstatus(SB),NOSPLIT,$0-8
	MOV	v+0(FP), T0
	CSRRW	T0, SSTATUS, ZERO
	RET

TEXT ·readSstatus(SB),NOSPLIT,$0-8
	CSRRS	ZERO, SSTATUS, T0
	MOV	T0, ret+0(FP)
	RET

TEXT ·writeSepc(SB),NOSPLIT,$0-8
	MOV	v+0(FP), T0
	CSRRW	T0, SEPC, ZERO
	RET

TEXT ·writeStvec(SB),NOSPLIT,$0-8
	MOV	v+0(FP), T0
	CSRRW	T0, STVEC, ZERO
	RET

TEXT ·readStvec(SB),NOSPLIT,$0-8
	CSRRS	ZERO, STVEC, T0
	MOV	T0, ret+0(FP)
	RET

TEXT ·writeSscratch(SB),NOSPLIT,$0-8
	MOV	v+0(FP), T0
	CSRRW	T0, SSCRATCH, ZERO
	RET

TEXT ·readSscratch(SB),NOSPLIT,$0-8
	CSRRS	ZERO, SSCRATCH, T0
	MOV	T0, ret+0(FP)
	RET

TEXT ·readHtval(SB),NOSPLIT,$0-8
	CSRRS	ZERO, HTVAL, T0
	MOV	T0, ret+0(FP)
	RET

// hvip: hypervisor virtual-interrupt pending. honk sets bit 6 (VSTIP) to inject
// a supervisor-timer interrupt into the guest (M12).
TEXT ·writeHvip(SB),NOSPLIT,$0-8
	MOV	v+0(FP), T0
	CSRRW	T0, HVIP, ZERO
	RET

TEXT ·readHvip(SB),NOSPLIT,$0-8
	CSRRS	ZERO, HVIP, T0
	MOV	T0, ret+0(FP)
	RET

// hideleg: delegate VS-level interrupts to VS-mode. honk sets bit 6 so the
// injected VS timer is delivered to the guest, not trapped to HS.
TEXT ·writeHideleg(SB),NOSPLIT,$0-8
	MOV	v+0(FP), T0
	CSRRW	T0, HIDELEG, ZERO
	RET

TEXT ·readHideleg(SB),NOSPLIT,$0-8
	CSRRS	ZERO, HIDELEG, T0
	MOV	T0, ret+0(FP)
	RET

// hcounteren: let VS-mode read the time CSR (bit 1, TM), which the guest needs
// to compute its absolute SBI timer deadline.
TEXT ·writeHcounteren(SB),NOSPLIT,$0-8
	MOV	v+0(FP), T0
	CSRRW	T0, HCOUNTEREN, ZERO
	RET

TEXT ·readHcounteren(SB),NOSPLIT,$0-8
	CSRRS	ZERO, HCOUNTEREN, T0
	MOV	T0, ret+0(FP)
	RET

// sie: honk's own S-mode interrupt enables. The VMM enables STIE (the HS timer
// that bounds and times the guest) and masks SEIE during the run.
TEXT ·writeSie(SB),NOSPLIT,$0-8
	MOV	v+0(FP), T0
	CSRRW	T0, SIE, ZERO
	RET

TEXT ·readSie(SB),NOSPLIT,$0-8
	CSRRS	ZERO, SIE, T0
	MOV	T0, ret+0(FP)
	RET
