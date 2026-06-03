//go:build tamago && riscv64

package virt

import (
	_ "unsafe"

	"github.com/usbarmory/tamago/riscv64"
)

// nsPerTick converts the platform `time` counter to nanoseconds. The QEMU virt
// timebase is 10 MHz, so one tick = 100 ns.
//
// TODO(phase1): read /cpus/timebase-frequency from the DTB instead of hardcoding
// (RV64.md Part 7.1) so the same image is correct on other boards.
const nsPerTick = 100

// cpu is honk's RISC-V core. It is the TamaGo riscv64.CPU used by every board,
// here only for its monotonic/wall clock (GetTime/SetTime, backed by the time
// CSR) and to install the S-mode exception handler (trap.go). honk deliberately
// keeps its own SBI-backed idle and SIE-masked interrupt model rather than the
// CPU's machine-mode defaults — so it never calls cpu.Init()/InitSupervisor(),
// EnableInterrupts(), or the DefaultIdleGovernor (see idle and trap.go).
var cpu = &riscv64.CPU{
	Counter:         readTime, // the time CSR (RDTIME, time_riscv64.s)
	TimerMultiplier: nsPerTick,
	TimerOffset:     1, // nonzero so GetTime() is valid before the clock is seeded
}

// nanotime is the runtime's monotonic clock, backed by the free-running `time`
// CSR via cpu.GetTime (= Counter*TimerMultiplier + TimerOffset). honk has no
// RTC, so wall time is the same clock with TimerOffset seeded from the build
// time at boot and refined by NTP (clock.go, SetWallClock).
//
//go:linkname nanotime runtime/goos.Nanotime
func nanotime() int64 { return cpu.GetTime() }

// SetWallClock makes time.Now() read approximately unixNanos by adjusting the
// clock offset (cpu.TimerOffset).
func SetWallClock(unixNanos int64) { cpu.SetTime(unixNanos) }

// timerTicks converts an absolute nanotime() deadline into a raw `time`-counter
// value for arming the SBI timer (idle). nanotime() carries the wall-clock
// offset (cpu.TimerOffset) but the hardware counter does not, so the offset is
// removed here. Without this, a clock seeded to ~2026 arms the timer decades of
// ticks away and wfi never wakes — which the old busy-poll RX masked by never
// idling.
func timerTicks(deadline int64) uint64 {
	return uint64((float64(deadline) - float64(cpu.TimerOffset)) / cpu.TimerMultiplier)
}

func readTime() uint64 // time_riscv64.s
