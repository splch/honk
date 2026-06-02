//go:build tamago && riscv64

package virt

import _ "unsafe"

// nsPerTick converts the platform `time` counter to nanoseconds. The QEMU virt
// timebase is 10 MHz, so one tick = 100 ns.
//
// TODO(phase1): read /cpus/timebase-frequency from the DTB instead of hardcoding
// (RV64.md Part 7.1) so the same image is correct on other boards.
const nsPerTick = 100

// nanotime is the runtime's monotonic clock, backed by the free-running `time`
// CSR (readable from S-mode). Implemented without allocation as it can run very
// early. readTime is defined in sbi_riscv64.s.
//
//go:linkname nanotime runtime/goos.Nanotime
func nanotime() int64 { return int64(readTime() * nsPerTick) }

func readTime() uint64 // sbi_riscv64.s
