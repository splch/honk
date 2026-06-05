// honk - QEMU virt board support.
//
// Package virt provides the runtime/goos overlay and low-level support for
// running honk as an HS-mode payload under OpenSBI on the QEMU `virt` machine.
//
// honk is its own board: TamaGo has no `virt` package, and TamaGo's riscv64
// support boots in M-mode. honk instead boots in HS-mode under OpenSBI (the
// mainline RISC-V model), so this package supplies its own cpuinit and the
// full set of runtime/goos hooks rather than reusing tamago/riscv64's.
//
// This package is only meant to be used with
// `GOOS=tamago GOARCH=riscv64 GOOSPKG=github.com/usbarmory/tamago`.
package virt

import (
	"math"
	"time"
	_ "unsafe"

	"runtime/goos"
)

// Boot arguments captured by cpuinit (boot_riscv64.s) from the OpenSBI
// hand-off: a0 = boot hartid, a1 = device tree blob pointer.
var (
	bootHartID uint64
	bootDTB    uint64
)

// BootHart returns the hart id honk booted on (the hart OpenSBI entered).
func BootHart() uint64 { return bootHartID }

// DTB returns the physical address of the device tree blob OpenSBI passed in
// a1. Parsing it (RAM size, hart count, MMIO bases) lands in a later milestone.
func DTB() uintptr { return uintptr(bootDTB) }

// Uptime returns the monotonic time since boot.
func Uptime() time.Duration { return time.Duration(nanotime()) }

// Fault deliberately raises an S-mode exception (EBREAK) to exercise the fatal
// trap path; it prints the trap CSRs and powers off. It does not return.
func Fault() { triggerFault() }

// hwinit0 runs before the runtime World is started (no allocation possible).
//
//go:linkname hwinit0 runtime/goos.Hwinit0
func hwinit0() {}

// hwinit1 runs early in runtime setup, after the World is up. We wire the
// optional goos termination hooks here so a returning (or deadlocked) kernel
// powers the machine off cleanly via SBI instead of spinning.
//
//go:linkname hwinit1 runtime/goos.Hwinit1
func hwinit1() {
	goos.Exit = func(int32) { Shutdown() }
	goos.Idle = idle
}

// idle is the runtime CPU idle governor. With no timer IRQ yet (M1), a finite
// deadline is handled by returning immediately so the scheduler busy-polls
// nanotime; "nothing to do, forever" means the kernel is done, so we power off.
func idle(until int64) {
	if until == math.MaxInt64 {
		Shutdown()
	}
}

// Shutdown powers off the machine via the SBI System Reset extension, which on
// QEMU virt cleanly exits the emulator. It is global: all harts stop.
func Shutdown() {
	sbiCall(sbiExtSRST, 0, 0, 0, 0) // type 0 = shutdown, reason 0 = none
	for {
		// Unreachable under OpenSBI; guard against a no-op SRST so we never
		// fall back into runtime code in an undefined state.
	}
}
