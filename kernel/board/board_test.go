package board

import (
	"encoding/binary"
	"testing"

	"github.com/splch/honk/kernel/device"
	"github.com/splch/honk/kernel/driver/sbi"
	"github.com/splch/honk/kernel/driver/uart"
	"github.com/splch/honk/kernel/dtb"
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
	var f fdt
	f.begin("")
	f.u32("#address-cells", 2)
	f.u32("#size-cells", 2)
	f.begin("chosen")
	f.str("stdout-path", "/soc/serial@10000000")
	f.end()
	f.begin("memory@80000000")
	f.str("device_type", "memory")
	f.reg(0x80000000, 0x4000000)
	f.end()
	f.begin("cpus")
	f.u32("#address-cells", 1)
	f.u32("#size-cells", 0)
	for i := 0; i < 2; i++ {
		f.begin("cpu@" + string(rune('0'+i)))
		f.str("device_type", "cpu")
		f.u32("reg", uint32(i))
		f.end()
	}
	f.end()
	f.begin("soc")
	f.u32("#address-cells", 2)
	f.u32("#size-cells", 2)
	f.begin("serial@10000000")
	f.str("compatible", consoleCompat)
	f.reg(0x10000000, 0x100)
	f.end()
	f.end()
	f.end()
	return f.blob()
}

// fdt is a compact builder that emits a valid FDT blob for tests.
type fdt struct {
	structb []byte
	strings []byte
	offsets map[string]uint32
}

func (f *fdt) tok(v uint32)       { f.structb = beAppend(f.structb, v) }
func (f *fdt) pad()               { f.structb = pad4(f.structb) }
func (f *fdt) end()               { f.tok(2) } // FDT_END_NODE
func (f *fdt) str(name, s string) { f.prop(name, append([]byte(s), 0)) }
func (f *fdt) u32(name string, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	f.prop(name, b[:])
}
func (f *fdt) reg(addr, size uint64) {
	var b [16]byte
	binary.BigEndian.PutUint64(b[0:], addr)
	binary.BigEndian.PutUint64(b[8:], size)
	f.prop("reg", b[:])
}

func (f *fdt) begin(name string) {
	f.tok(1) // FDT_BEGIN_NODE
	f.structb = append(f.structb, name...)
	f.structb = append(f.structb, 0)
	f.pad()
}

func (f *fdt) prop(name string, val []byte) {
	if f.offsets == nil {
		f.offsets = map[string]uint32{}
	}
	off, ok := f.offsets[name]
	if !ok {
		off = uint32(len(f.strings))
		f.offsets[name] = off
		f.strings = append(append(f.strings, name...), 0)
	}
	f.tok(3) // FDT_PROP
	f.tok(uint32(len(val)))
	f.tok(off)
	f.structb = append(f.structb, val...)
	f.pad()
}

func (f *fdt) blob() []byte {
	f.tok(9) // FDT_END
	const hdr, memrsv = 40, 16
	structOff := uint32(hdr + memrsv)
	stringsOff := structOff + uint32(len(f.structb))

	var out []byte
	put := func(v uint32) { out = beAppend(out, v) }
	put(0xd00dfeed)                          // magic
	put(stringsOff + uint32(len(f.strings))) // totalsize
	put(structOff)
	put(stringsOff)
	put(hdr) // off_mem_rsvmap
	put(17)  // version
	put(16)  // last_comp_version
	put(0)   // boot_cpuid_phys
	put(uint32(len(f.strings)))
	put(uint32(len(f.structb)))
	out = append(out, make([]byte, memrsv)...) // terminating reservation
	out = append(out, f.structb...)
	out = append(out, f.strings...)
	return out
}

func beAppend(b []byte, v uint32) []byte {
	var w [4]byte
	binary.BigEndian.PutUint32(w[:], v)
	return append(b, w[:]...)
}

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}
