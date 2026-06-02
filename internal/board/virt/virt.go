//go:build tamago && riscv64

// Package virt provides hardware initialization and the runtime seam for the
// QEMU virt machine, booting as an S-mode payload under OpenSBI.
//
// Unlike TamaGo's sifive_u board (which boots in machine mode), virt enters in
// supervisor mode: OpenSBI owns M-mode, performs trap delegation, and mret's
// into honk's ELF entry. This package therefore supplies an S-mode cpuinit (see
// boot_riscv64.s) and routes console and poweroff through the SBI firmware
// interface rather than raw machine-mode CSRs/MMIO. The full runtime "seam" is
// documented in DESIGN.md §6.
//
// Memory layout (qemu -m 512M, RAM [0x80000000,0xA0000000)): OpenSBI occupies
// the base; honk's managed region starts at RamStart and its text is linked at
// RamStart+0x10000 (see the Makefile -T flag for TARGET=virt).
package virt

import (
	"runtime/goos"
	_ "unsafe"
)

// Memory available to the Go runtime, above the OpenSBI-reserved region.
//
//go:linkname ramStart runtime/goos.RamStart
var ramStart uint64 = 0x80200000

//go:linkname ramSize runtime/goos.RamSize
var ramSize uint64 = 0x1fe00000 // 510 MiB: 0xA0000000 - 0x80200000

//go:linkname ramStackOffset runtime/goos.RamStackOffset
var ramStackOffset uint64 = 0x100

// hwinit0 is the pre-runtime hook; the S-mode cpuinit (boot_riscv64.s) already
// did the CPU bring-up, so nothing more is required before the world starts.
//
//go:linkname hwinit0 runtime/goos.Hwinit0
func hwinit0() {}

// hwinit1 is the post-bootstrap hook: now that the Go world (and thus function
// calls) is running, wire up firmware-backed termination and idle.
//
//go:linkname hwinit1 runtime/goos.Hwinit1
func hwinit1() {
	goos.Exit = exit
	goos.Idle = idle
}

const maxInt64 = 1<<63 - 1

// exit powers the machine off via SBI; honk has no caller to return to.
func exit(int32) { sbiShutdown() }

// idle is the runtime's CPU idle governor. The Go runtime advances time via the
// free-running `time` counter (see nanotime), so finite sleeps resolve by
// re-polling — we simply return. A request to idle until math.MaxInt64 means no
// goroutine will ever run again, so power off cleanly via SBI.
//
// TODO(phase1): replace busy-polling with a real S-timer (Sstc stimecmp or SBI
// set_timer) + wfi once the trap handler lands (RV64.md Part 4).
func idle(until int64) {
	if until == maxInt64 {
		sbiShutdown()
	}
}
