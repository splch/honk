//go:build tamago && riscv64

// Package virt provides hardware initialization and the runtime seam for the
// QEMU virt machine, booting as an S-mode payload under OpenSBI.
//
// Unlike TamaGo's sifive_u board (which boots in machine mode), virt enters in
// supervisor mode: OpenSBI owns M-mode, performs trap delegation, and enters
// honk in S-mode. This package supplies an S-mode cpuinit (see boot_riscv64.s)
// and routes console and poweroff through the SBI firmware interface rather
// than raw machine-mode CSRs/MMIO. The runtime "seam" is documented in
// DESIGN.md §6; the boot path and the load-base trampoline in DESIGN.md §4.
//
// Memory layout (qemu -m 512M, RAM [0x80000000,0xA0000000)): OpenSBI occupies
// the base; honk's managed region starts at RamStart and its text is linked at
// RamStart+0x10000 (see the Makefile -T flag for TARGET=virt).
package virt

import (
	"runtime/goos"
	"unsafe"

	"github.com/splch/honk/internal/fdt"
	"github.com/splch/honk/internal/sbi"
)

// Memory available to the Go runtime, above the OpenSBI-reserved region.
//
//go:linkname ramStart runtime/goos.RamStart
var ramStart uint64 = 0x80200000

//go:linkname ramSize runtime/goos.RamSize
var ramSize uint64 = 0x1fe00000 // 510 MiB: 0xA0000000 - 0x80200000

//go:linkname ramStackOffset runtime/goos.RamStackOffset
var ramStackOffset uint64 = 0x100

// Boot arguments captured by cpuinit (boot_riscv64.s): a0=hartid, a1=DTB.
// dtbPtr is an unsafe.Pointer (not uintptr) so go vet's unsafeptr check stays
// happy; the address is outside any Go heap span, so the GC ignores it.
var (
	hartID uintptr
	dtbPtr unsafe.Pointer
)

// Hardware discovered from the device tree by hwinit1 (RV64.md Part 7.1).
var (
	Model      string
	RAMBase    uint64
	RAMSize    uint64
	Harts      int
	TimebaseHz uint32
)

// The pre-runtime hook (runtime/goos.Hwinit0) is provided by tamago/riscv64 as
// an empty function; honk's S-mode CPU bring-up happens earlier, in cpuinit
// (boot_riscv64.s), which overrides tamago's machine-mode cpuinit via the
// linkcpuinit build tag (see the Makefile).

// hwinit1 is the post-bootstrap hook (the Go world is up, so allocation and
// function calls are safe): wire up firmware-backed termination/idle and probe
// the device tree.
//
//go:linkname hwinit1 runtime/goos.Hwinit1
func hwinit1() {
	cpu.SetSupervisorExceptionHandler(trapVector) // S-mode fault handler (RV64.md Part 3)
	enableTimerIRQ()                              // sie.STIE, so the timer can wake wfi (RV64.md Part 4)
	goos.Exit = exit
	goos.Idle = idle
	probe()
	enablePaging() // Sv39 identity map with W^X (RV64.md Part 5, DESIGN.md §9)
	initClock()    // seed the wall clock from build time, before any timers (DESIGN.md §15.5)
	initEntropy()  // virtio-rng FIRST: seed crypto/rand before any key (DESIGN.md §15.4)
	initConsole()  // take over the NS16550A UART (RV64.md Part 7.3)
	initNet()      // bring up a virtio-net device if attached (RV64.md Part 7.4)
	// The FAT32 disk mount (disk.go) and the gVisor TCP/IP stack (netstack.go) come
	// up from package init()s, not here: hwinit1 runs on the system stack, where
	// defer — used throughout go-diskfs and gVisor — is forbidden, and before
	// package-level init()s their own packages need (DESIGN.md §15.3).
}

const maxInt64 = 1<<63 - 1

// exit powers the machine off via SBI; honk has no caller to return to.
func exit(int32) { sbi.Shutdown() }

// idle is the runtime's CPU idle governor (called with the absolute nanotime
// deadline of the next pending timer, or math.MaxInt64 if none). It first
// drains any pending console input (clearing the external interrupt), then arms
// the S-timer at the deadline and waits in low-power wfi. The hart is also woken
// early by UART input (sie.SEIE), so honk sleeps until either a timer fires or a
// key is pressed — it no longer needs to busy-poll, and it stays alive for
// interactive input rather than powering off when no goroutine is runnable.
func idle(until int64) {
	drainConsole()
	if until != 0 && until != maxInt64 {
		if until <= nanotime() {
			return // the deadline has passed; a goroutine is ready to run
		}
		sbi.SetTimer(timerTicks(until)) // wake at the deadline (raw timer ticks)
	}
	cpu.WaitInterrupt() // wfi: woken by the timer (if armed) or by UART input
}

// probe parses the firmware device tree and records the discovered hardware,
// logging a one-line summary. It runs in hwinit1, before main.
func probe() {
	t, err := fdt.Parse(deviceTree())
	if err != nil {
		print("honk/virt: device-tree parse failed: ", err.Error(), "\n")
		return
	}
	Model = t.Model()
	RAMBase, RAMSize, _ = t.Memory()
	TimebaseHz, _ = t.TimebaseFrequency()
	Harts = t.HartCount()

	print("honk/virt: ", Model, ", ", Harts, " hart(s), RAM ",
		int(RAMSize>>20), " MiB, timebase ", int(TimebaseHz)/1_000_000, " MHz\n")
}

// deviceTree returns a copy of the firmware DTB. It is copied out of the
// firmware-owned region (which sits inside honk's RAM window and could be reused
// by the allocator) into the Go heap before parsing.
func deviceTree() []byte {
	if dtbPtr == nil {
		return nil
	}
	hdr := unsafe.Slice((*byte)(dtbPtr), 8)
	if be32(hdr) != 0xd00dfeed {
		return nil
	}
	total := int(be32(hdr[4:]))
	return append([]byte(nil), unsafe.Slice((*byte)(dtbPtr), total)...)
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
