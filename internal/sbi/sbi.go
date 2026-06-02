//go:build tamago && riscv64

// Package sbi wraps the RISC-V Supervisor Binary Interface: an ECALL traps
// S->M into the M-mode firmware (OpenSBI), which services the request and
// returns. honk uses it for the early console, the timer, and poweroff while it
// has no native drivers. See RV64.md Part 2 (ABI in §2.1, calls in §2.2).
package sbi

// Extension IDs (a7) and the legacy function EIDs (RV64.md Part 2.2 / Appendix D).
const (
	extBase = 0x10       // Base extension
	extTime = 0x54494d45 // "TIME"
	extSRST = 0x53525354 // "SRST"

	legacyConsolePutchar = 0x01
	legacyShutdown       = 0x08
)

// call issues an ecall with a7=eid, a6=fid, a0..a2=args and returns the SBI
// error/value pair (a0/a1). For legacy EIDs (< 0x10) fid is ignored and only
// the a0 result is meaningful. Defined in sbi_riscv64.s.
func call(eid, fid, a0, a1, a2 uintptr) (err, val int)

// ConsolePutchar writes one byte to the SBI console (legacy console_putchar,
// EID 0x01) — the early-boot console before a native UART driver exists.
func ConsolePutchar(c byte) { call(legacyConsolePutchar, 0, uintptr(c), 0, 0) }

// Shutdown powers the system off (legacy shutdown, EID 0x08); it does not return.
func Shutdown() { call(legacyShutdown, 0, 0, 0, 0) }

// SetTimer programs the next supervisor timer interrupt for absolute time t (in
// `time` counter ticks) via the TIME extension; the same write clears any
// pending timer interrupt. Used once honk has a trap handler (RV64.md Part 4).
func SetTimer(t uint64) { call(extTime, 0, uintptr(t), 0, 0) }

// ProbeExtension reports whether the SBI extension eid is available (Base, FID 3).
func ProbeExtension(eid uintptr) bool {
	_, v := call(extBase, 3, eid, 0, 0)
	return v != 0
}
