package dtb

import (
	"testing"

	"github.com/splch/honk/kernel/internal/fdt"
)

// These tests run on the host — `go test ./kernel/dtb` — with no QEMU and no
// hardware. They build valid FDT blobs in memory with package fdt (the write
// side of this parser) and check that every query extracts the right value,
// then feed garbage in to confirm a bad blob is a clean (nil, false).

// virtBlob is a hand-built device tree shaped like QEMU 'virt': root cells 2/2,
// a console UART under /soc, RAM, three harts (one disabled), and a /chosen
// stdout-path plus a /serial0 alias pointing at the UART.
func virtBlob() []byte {
	var b fdt.Builder
	b.Begin("") // root
	b.U32("#address-cells", 2)
	b.U32("#size-cells", 2)
	b.Str("compatible", "honk-virt")

	b.Begin("chosen")
	b.Str("stdout-path", "/soc/serial@10000000")
	b.End()

	b.Begin("aliases")
	b.Str("serial0", "/soc/serial@10000000")
	b.End()

	b.Begin("memory@80000000")
	b.Str("device_type", "memory")
	b.Reg(0x80000000, 0x2000000) // cells 2/2 from root
	b.End()

	b.Begin("cpus")
	b.U32("#address-cells", 1)
	b.U32("#size-cells", 0)
	for i, status := range []string{"okay", "okay", "disabled"} {
		b.Begin("cpu@" + string(rune('0'+i)))
		b.Str("device_type", "cpu")
		b.Str("status", status)
		b.U32("reg", uint32(i)) // cells 1/0
		b.End()
	}
	b.End()

	b.Begin("soc")
	b.U32("#address-cells", 2)
	b.U32("#size-cells", 2)
	b.Begin("serial@10000000")
	b.Str("compatible", "ns16550a")
	b.Reg(0x10000000, 0x100) // cells 2/2 from soc
	b.End()
	b.End()

	b.End() // root
	return b.Bytes()
}

func TestParseInvalid(t *testing.T) {
	for name, blob := range map[string][]byte{
		"nil":       nil,
		"short":     {1, 2, 3},
		"bad magic": make([]byte, 64),
		"truncated": virtBlob()[:50],
	} {
		if _, ok := Parse(blob); ok {
			t.Errorf("%s: Parse should report not-a-tree", name)
		}
	}
}

func TestStdoutConsole(t *testing.T) {
	tree, ok := Parse(virtBlob())
	if !ok {
		t.Fatal("virtBlob should parse")
	}
	n, ok := tree.Stdout()
	if !ok {
		t.Fatal("/chosen stdout-path should resolve")
	}
	if n.Name != "serial@10000000" {
		t.Errorf("stdout node = %q", n.Name)
	}
	if !n.Compatible("ns16550a") {
		t.Errorf("console compatible = %v", n.Compatibles())
	}
	addr, size, ok := n.Reg()
	if !ok || addr != 0x10000000 || size != 0x100 {
		t.Errorf("console reg = (%#x, %#x, %v)", addr, size, ok)
	}
}

func TestMemoryAndCPUs(t *testing.T) {
	tree, _ := Parse(virtBlob())
	if base, size, ok := tree.Memory(); !ok || base != 0x80000000 || size != 0x2000000 {
		t.Errorf("Memory = (%#x, %#x, %v)", base, size, ok)
	}
	if n := tree.NumCPUs(); n != 2 { // three cpu nodes, one disabled
		t.Errorf("NumCPUs = %d, want 2", n)
	}
}

func TestNodeLookupAndCompatible(t *testing.T) {
	tree, _ := Parse(virtBlob())
	if _, ok := tree.Node("/soc/serial@10000000"); !ok {
		t.Error("absolute path should resolve")
	}
	if _, ok := tree.Node("/soc/nope"); ok {
		t.Error("missing path must not resolve")
	}
	if n, ok := tree.Compatible("ns16550a"); !ok || n.Name != "serial@10000000" {
		t.Errorf("Compatible search = %v, %v", n, ok)
	}
	if _, ok := tree.Compatible("acme,nonesuch"); ok {
		t.Error("unknown compatible must not match")
	}
}

func TestStdoutAliasAndBaud(t *testing.T) {
	// stdout-path given as an /aliases name with a ":baud" suffix.
	var b fdt.Builder
	b.Begin("")
	b.U32("#address-cells", 2)
	b.U32("#size-cells", 2)
	b.Begin("chosen")
	b.Str("stdout-path", "serial0:115200")
	b.End()
	b.Begin("aliases")
	b.Str("serial0", "/uart@9000000")
	b.End()
	b.Begin("uart@9000000")
	b.Str("compatible", "arm,pl011")
	b.Reg(0x9000000, 0x1000)
	b.End()
	b.End()
	tree, ok := Parse(b.Bytes())
	if !ok {
		t.Fatal("blob should parse")
	}
	n, ok := tree.Stdout()
	if !ok || n.Name != "uart@9000000" || !n.Compatible("arm,pl011") {
		t.Errorf("alias+baud stdout = %v, %v", n, ok)
	}
}
