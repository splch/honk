// Package board discovers the machine's devices from the device tree the
// firmware passed at boot, with fallbacks so the kernel always comes up — in
// QEMU, on real RISC-V hardware, or with no device tree at all. It is the one
// place that knows machine specifics; nothing above it hardcodes an address. A
// fork retargets Honk by replacing this package and registering its drivers.
package board

import (
	"github.com/splch/honk/kernel/arch/riscv64"
	"github.com/splch/honk/kernel/console"
	"github.com/splch/honk/kernel/device"
	"github.com/splch/honk/kernel/driver/sbi"
	"github.com/splch/honk/kernel/driver/uart"
	"github.com/splch/honk/kernel/dtb"
)

// Fallbacks used only when the firmware passes no device tree; with a device
// tree the real values come from it. The base is the QEMU 'virt' RAM start.
const (
	defaultRAMBase = 0x80000000
	defaultRAMSize = 32 << 20
)

// tree is the parsed device tree, or nil when the firmware passed none. It is
// parsed once at startup.
var tree = parseTree()

func parseTree() *dtb.Tree {
	t, _ := dtb.Parse(riscv64.DTB())
	return t
}

// Console returns the byte device for the system console, choosing in order:
// the UART named by the device tree's /chosen stdout-path when a driver is
// registered for it; the SBI console when the device tree names a console Honk
// has no driver for (SBI works on any platform); or the QEMU 'virt' NS16550A
// when there is no device tree. The result always works.
func Console() console.Device { return consoleFor(tree) }

func consoleFor(t *dtb.Tree) console.Device {
	if t == nil {
		return uart.New(riscv64.UART0)
	}
	if n, ok := t.Stdout(); ok {
		if base, _, ok := n.Reg(); ok {
			for _, compat := range n.Compatibles() {
				if ctor, ok := device.ConsoleDriver(compat); ok {
					return ctor(base)
				}
			}
		}
	}
	return sbi.New()
}

// Memory returns the base address and size in bytes of system RAM, from the
// device tree when present, otherwise the QEMU 'virt' defaults.
func Memory() (base, size uintptr) { return memoryOf(tree) }

func memoryOf(t *dtb.Tree) (base, size uintptr) {
	if t != nil {
		if b, s, ok := t.Memory(); ok {
			return b, s
		}
	}
	return defaultRAMBase, defaultRAMSize
}

// NumCPUs returns the number of usable harts, from the device tree when present,
// otherwise 1.
func NumCPUs() int { return cpusOf(tree) }

func cpusOf(t *dtb.Tree) int {
	if t != nil {
		if n := t.NumCPUs(); n > 0 {
			return n
		}
	}
	return 1
}
