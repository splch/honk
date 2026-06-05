// honk - QEMU virt board: memory layout for the Go runtime.

//go:build tamago && riscv64

package virt

import _ "unsafe"

// honk loads above the boot trampoline: OpenSBI enters the trampoline at
// 0x80200000 (tools/mkboot), which jumps to honk linked at 0x80400000.
// RamStart is honk's load address, so the Go runtime allocator never hands out
// firmware- or trampoline-owned memory.
//
//go:linkname ramStart runtime/goos.RamStart
var ramStart uint64 = 0x80400000

// RamSize ends below where QEMU places the device tree (top of RAM), so the
// runtime arena and boot stack never clobber the DTB. Sized for `-m 512M`
// (RAM 0x80000000-0xA0000000, DTB ~0x9fe00000); device-tree driven sizing
// lands in a later milestone. Keep tools/run-qemu.sh's -m in sync.
//
//go:linkname ramSize runtime/goos.RamSize
var ramSize uint64 = 0x9fe00000 - 0x80400000 // 0x1DA00000

// RamStackOffset is the negative offset from the end of RAM at which the boot
// (g0) stack is placed.
//
//go:linkname ramStackOffset runtime/goos.RamStackOffset
var ramStackOffset uint64 = 0x100
