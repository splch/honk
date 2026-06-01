// Package riscv64 holds platform constants and low-level MMIO helpers for Honk
// OS on the QEMU 'virt' machine (RISC-V 64-bit, supervisor mode under OpenSBI).
package riscv64

import "unsafe"

// MMIO device base addresses on the QEMU virt machine. With no paging yet,
// OpenSBI's PMP leaves these pages readable/writable from both S- and U-mode,
// so the accessors below reach them directly from user-mode goroutines.
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

// dtbPtr is the address of the device tree blob the firmware passed at boot
// (RISC-V delivers it to the kernel in register a1). On the noos/riscv64 target
// the runtime entry stub captures a1 across BSS-clear and publishes it, and this
// variable aliases it (see dtb_ptr_noos.go); on every other build it stays 0
// (dtb_ptr_host.go). While it is 0, DTB returns nil and the board falls back to
// its defaults, so the kernel boots either way.

// DTB returns a copy of the device tree blob the firmware passed at boot, or nil
// when none was captured. Parsing it (package dtb) is how the board discovers the
// console, RAM, and hart count instead of hardcoding them for one machine.
//
// The result is copied out of firmware memory on purpose: OpenSBI places the
// blob high in RAM, inside the region the Go allocator manages (the -M heap
// arena), so a slice aliasing it could be overwritten once allocation reaches
// that far. Callers read DTB early (board init, before the heap is used), so the
// source is still intact; the returned copy then lives in GC-managed memory and
// stays valid for the life of the parsed tree.
func DTB() []byte {
	if dtbPtr == 0 {
		return nil
	}
	head := unsafe.Slice((*byte)(unsafe.Pointer(dtbPtr)), 8)
	magic := uint32(head[0])<<24 | uint32(head[1])<<16 | uint32(head[2])<<8 | uint32(head[3])
	if magic != 0xd00dfeed { // FDT magic
		return nil
	}
	total := uint32(head[4])<<24 | uint32(head[5])<<16 | uint32(head[6])<<8 | uint32(head[7])
	return append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(dtbPtr)), total)...)
}
