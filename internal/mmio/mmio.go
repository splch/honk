//go:build tamago && riscv64

// Package mmio provides volatile memory-mapped I/O accessors. Reads and writes
// go through assembly so the compiler cannot reorder, coalesce, or elide device
// register accesses, which have side effects (RV64.md §7.2).
package mmio

// asm (mmio_riscv64.s); uintptr-only so the frame offsets stay 8-byte clean.
func r8(addr uintptr) uintptr
func r32(addr uintptr) uintptr
func w8(addr, v uintptr)
func w32(addr, v uintptr)

func R8(addr uintptr) uint8      { return uint8(r8(addr)) }
func R32(addr uintptr) uint32    { return uint32(r32(addr)) }
func W8(addr uintptr, v uint8)   { w8(addr, uintptr(v)) }
func W32(addr uintptr, v uint32) { w32(addr, uintptr(v)) }
