// honk - QEMU virt board: memory layout for the Go runtime.

//go:build tamago && riscv64

package virt

import _ "unsafe"

// ramStart is honk's load address: it links and loads at 0x80400000 (above the
// boot trampoline). This MUST match the -T flag in tools/build.sh. Memory below
// it (OpenSBI, trampoline) is never part of the runtime's arena.
//
//go:linkname ramStart runtime/goos.RamStart
var ramStart uint64 = 0x80400000

// ramSize is the runtime arena size. hwinit0 derives the true value from the
// device tree (which OpenSBI/QEMU place at the top of usable RAM) before the
// runtime starts; this static value is only a fallback for a bogus DTB. The
// runtime grows the heap up to the g0 stack (cpuinit puts it just below the
// DTB), so this size and that stack top are kept consistent.
//
//go:linkname ramSize runtime/goos.RamSize
var ramSize uint64 = 64 << 20

// ramStackOffset is the gap left below the DTB for the boot (g0) stack top.
// cpuinit reads it; the runtime/goos contract also requires it.
//
//go:linkname ramStackOffset runtime/goos.RamStackOffset
var ramStackOffset uint64 = 0x100

// minRAMSize is the smallest plausible arena; hwinit0 ignores a DTB address
// that would imply less, keeping the fallback ramSize.
const minRAMSize = 16 << 20
