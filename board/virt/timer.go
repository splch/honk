// honk - QEMU virt board: monotonic time.

//go:build tamago && riscv64

package virt

import _ "unsafe"

// QEMU virt drives the RISC-V time CSR at 10 MHz, i.e. 100 ns per tick. The
// real timebase comes from the device tree (/cpus/timebase-frequency); honk
// reads it once DTB parsing lands.
const nsPerTick = 100 // 1e9 / 10e6

// nanotime is the runtime's monotonic clock. Reads the time CSR directly so it
// works from the very first instruction, with no dependency on driver state.
//
//go:linkname nanotime runtime/goos.Nanotime
//go:nosplit
func nanotime() int64 {
	return int64(readTime() * nsPerTick)
}
