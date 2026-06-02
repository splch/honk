//go:build tamago && riscv64

package virt

import (
	"sync/atomic"
	_ "unsafe"
)

// wallOffset (ns) is added to the monotonic `time` counter to produce wall-clock
// time. honk has no RTC, so it is seeded from the build time at boot and refined
// by NTP (clock.go). atomic so getRandomData/nanotime can read it lock-free.
var wallOffset atomic.Int64

// SetWallClock makes time.Now() read approximately unixNanos.
func SetWallClock(unixNanos int64) {
	wallOffset.Store(unixNanos - int64(readTime()*nsPerTick))
}

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
func nanotime() int64 { return int64(readTime()*nsPerTick) + wallOffset.Load() }

// timerTicks converts an absolute nanotime() deadline into a raw `time`-counter
// value for arming the SBI timer (idle). nanotime() carries the wall-clock
// offset (clock.go) but the hardware counter does not, so the offset is removed
// here. Without this, a clock seeded to ~2026 arms the timer decades of ticks
// away and wfi never wakes — which the old busy-poll RX masked by never idling.
func timerTicks(deadline int64) uint64 {
	return uint64(deadline-wallOffset.Load()) / nsPerTick
}

func readTime() uint64 // time_riscv64.s
