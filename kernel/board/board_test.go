package board

import (
	"testing"

	"github.com/splch/honk/kernel/device"
	"github.com/splch/honk/kernel/driver/sbi"
	"github.com/splch/honk/kernel/driver/uart"
	"github.com/splch/honk/kernel/dtb"
	"github.com/splch/honk/kernel/internal/fdt"
)

// These tests run on the host. They drive the discovery logic through the real
// parser by handing it device-tree blobs (blobWith, below), and check which
// console driver each path selects — without any hardware.

func TestConsoleNoDeviceTree(t *testing.T) {
	if _, ok := consoleFor(nil).(*uart.Device); !ok {
		t.Error("no device tree should fall back to the NS16550A default")
	}
}

func TestConsoleDiscoversNS16550(t *testing.T) {
	tree, ok := dtb.Parse(blobWith("ns16550a"))
	if !ok {
		t.Fatal("blob should parse")
	}
	if _, ok := consoleFor(tree).(*uart.Device); !ok {
		t.Error("an ns16550a console should resolve to the UART driver")
	}
}

func TestConsoleUnknownFallsBackToSBI(t *testing.T) {
	tree, _ := dtb.Parse(blobWith("acme,mystery-uart"))
	if _, ok := consoleFor(tree).(*sbi.Device); !ok {
		t.Error("a console with no registered driver should fall back to SBI")
	}
}

func TestMemoryAndCPUs(t *testing.T) {
	if b, s := memoryOf(nil); b != defaultRAMBase || s != defaultRAMSize {
		t.Errorf("default memory = %#x/%#x", b, s)
	}
	if n := cpusOf(nil); n != 1 {
		t.Errorf("default cpus = %d, want 1", n)
	}
	tree, _ := dtb.Parse(blobWith("ns16550a"))
	if b, s := memoryOf(tree); b != 0x80000000 || s != 0x4000000 {
		t.Errorf("discovered memory = %#x/%#x", b, s)
	}
	if n := cpusOf(tree); n != 2 {
		t.Errorf("discovered cpus = %d, want 2", n)
	}
}

func TestNS16550Registered(t *testing.T) {
	if _, ok := device.ConsoleDriver("ns16550a"); !ok {
		t.Error("the uart package should register the ns16550a driver on import")
	}
}

// blobWith builds a QEMU-virt-shaped device tree whose console UART reports the
// given "compatible" string: root cells 2/2, /chosen pointing at the UART, RAM
// at 0x80000000, two harts, and the UART at 0x10000000 under /soc.
func blobWith(consoleCompat string) []byte {
	var b fdt.Builder
	b.Begin("")
	b.U32("#address-cells", 2)
	b.U32("#size-cells", 2)
	b.Begin("chosen")
	b.Str("stdout-path", "/soc/serial@10000000")
	b.End()
	b.Begin("memory@80000000")
	b.Str("device_type", "memory")
	b.Reg(0x80000000, 0x4000000)
	b.End()
	b.Begin("cpus")
	b.U32("#address-cells", 1)
	b.U32("#size-cells", 0)
	for i := 0; i < 2; i++ {
		b.Begin("cpu@" + string(rune('0'+i)))
		b.Str("device_type", "cpu")
		b.U32("reg", uint32(i))
		b.End()
	}
	b.End()
	b.Begin("soc")
	b.U32("#address-cells", 2)
	b.U32("#size-cells", 2)
	b.Begin("serial@10000000")
	b.Str("compatible", consoleCompat)
	b.Reg(0x10000000, 0x100)
	b.End()
	b.End()
	b.End()
	return b.Bytes()
}
