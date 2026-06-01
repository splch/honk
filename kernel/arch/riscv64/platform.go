// Package riscv64 holds platform constants and low-level MMIO helpers for Honk
// OS on the QEMU 'virt' machine (RISC-V 64-bit, supervisor mode under OpenSBI).
package riscv64

import "unsafe"

// MMIO device base addresses on the QEMU virt machine. OpenSBI grants
// supervisor/user read+write to these pages, and Honk's goroutines run in
// U-mode with no paging, so direct memory-mapped I/O is permitted.
const (
	UART0 = 0x10000000 // NS16550A UART
	Test0 = 0x00100000 // sifive_test poweroff/reset device
)

// R8 reads one byte from an MMIO register. //go:noinline keeps the volatile load
// from being hoisted out of device poll loops.
//
//go:noinline
func R8(addr uintptr) byte { return *(*byte)(unsafe.Pointer(addr)) }

// W8 writes one byte to an MMIO register.
//
//go:noinline
func W8(addr uintptr, v byte) { *(*byte)(unsafe.Pointer(addr)) = v }

// W32 writes one 32-bit word to an MMIO register. //go:noinline also guarantees
// the store is emitted even when the caller never reads it back (e.g. Poweroff,
// whose store would otherwise be dead-eliminated before its infinite loop).
//
//go:noinline
func W32(addr uintptr, v uint32) { *(*uint32)(unsafe.Pointer(addr)) = v }

// Poweroff powers the machine off via QEMU's sifive_test device and never
// returns.
func Poweroff() {
	W32(Test0, 0x5555) // FINISHER_PASS
	for {
	}
}
