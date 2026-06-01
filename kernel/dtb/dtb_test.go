package dtb

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// These tests run on the host — `go test ./kernel/dtb` — with no QEMU and no
// hardware. They build valid FDT blobs in memory (fdtBuilder, below, mirrors the
// QEMU 'virt' layout) and check that every query extracts the right value, then
// feed garbage in to confirm a bad blob is a clean (nil, false).

// virtBlob is a hand-built device tree shaped like QEMU 'virt': root cells 2/2,
// a console UART under /soc, RAM, three harts (one disabled), and a /chosen
// stdout-path plus a /serial0 alias pointing at the UART.
func virtBlob() []byte {
	var b fdtBuilder
	b.begin("") // root
	b.u32Prop("#address-cells", 2)
	b.u32Prop("#size-cells", 2)
	b.strProp("compatible", "honk-virt")

	b.begin("chosen")
	b.strProp("stdout-path", "/soc/serial@10000000")
	b.end()

	b.begin("aliases")
	b.strProp("serial0", "/soc/serial@10000000")
	b.end()

	b.begin("memory@80000000")
	b.strProp("device_type", "memory")
	b.regProp(0x80000000, 0x2000000) // cells 2/2 from root
	b.end()

	b.begin("cpus")
	b.u32Prop("#address-cells", 1)
	b.u32Prop("#size-cells", 0)
	for i, status := range []string{"okay", "okay", "disabled"} {
		b.begin("cpu@" + string(rune('0'+i)))
		b.strProp("device_type", "cpu")
		b.strProp("status", status)
		b.u32Prop("reg", uint32(i)) // cells 1/0
		b.end()
	}
	b.end()

	b.begin("soc")
	b.u32Prop("#address-cells", 2)
	b.u32Prop("#size-cells", 2)
	b.begin("serial@10000000")
	b.strProp("compatible", "ns16550a")
	b.regProp(0x10000000, 0x100) // cells 2/2 from soc
	b.end()
	b.end()

	b.end() // root
	return b.bytes()
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
	var b fdtBuilder
	b.begin("")
	b.u32Prop("#address-cells", 2)
	b.u32Prop("#size-cells", 2)
	b.begin("chosen")
	b.strProp("stdout-path", "serial0:115200")
	b.end()
	b.begin("aliases")
	b.strProp("serial0", "/uart@9000000")
	b.end()
	b.begin("uart@9000000")
	b.strProp("compatible", "arm,pl011")
	b.regProp(0x9000000, 0x1000)
	b.end()
	b.end()
	tree, ok := Parse(b.bytes())
	if !ok {
		t.Fatal("blob should parse")
	}
	n, ok := tree.Stdout()
	if !ok || n.Name != "uart@9000000" || !n.Compatible("arm,pl011") {
		t.Errorf("alias+baud stdout = %v, %v", n, ok)
	}
}

// --- fdtBuilder: emits a valid FDT blob for tests ---

type fdtBuilder struct {
	structb bytes.Buffer
	strings bytes.Buffer
	offsets map[string]uint32
}

func (b *fdtBuilder) tok(v uint32) { binary.Write(&b.structb, binary.BigEndian, v) }
func (b *fdtBuilder) begin(name string) {
	b.tok(tokBeginNode)
	b.structb.WriteString(name)
	b.structb.WriteByte(0)
	b.pad()
}
func (b *fdtBuilder) end() { b.tok(tokEndNode) }

func (b *fdtBuilder) prop(name string, val []byte) {
	if b.offsets == nil {
		b.offsets = map[string]uint32{}
	}
	off, ok := b.offsets[name]
	if !ok {
		off = uint32(b.strings.Len())
		b.offsets[name] = off
		b.strings.WriteString(name)
		b.strings.WriteByte(0)
	}
	b.tok(tokProp)
	b.tok(uint32(len(val)))
	b.tok(off)
	b.structb.Write(val)
	b.pad()
}

func (b *fdtBuilder) strProp(name, s string) { b.prop(name, append([]byte(s), 0)) }
func (b *fdtBuilder) u32Prop(name string, v uint32) {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	b.prop(name, buf[:])
}
func (b *fdtBuilder) regProp(addr, size uint64) {
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[0:], addr)
	binary.BigEndian.PutUint64(buf[8:], size)
	b.prop("reg", buf[:])
}

func (b *fdtBuilder) pad() {
	for b.structb.Len()%4 != 0 {
		b.structb.WriteByte(0)
	}
}

func (b *fdtBuilder) bytes() []byte {
	b.tok(tokEnd)
	const hdr, memrsv = 40, 16 // header + one (empty) memory-reservation entry
	structOff := uint32(hdr + memrsv)
	stringsOff := structOff + uint32(b.structb.Len())

	var out bytes.Buffer
	put := func(v uint32) { binary.Write(&out, binary.BigEndian, v) }
	put(magic)
	put(stringsOff + uint32(b.strings.Len())) // totalsize
	put(structOff)
	put(stringsOff)
	put(hdr) // off_mem_rsvmap
	put(17)  // version
	put(16)  // last_comp_version
	put(0)   // boot_cpuid_phys
	put(uint32(b.strings.Len()))
	put(uint32(b.structb.Len()))
	out.Write(make([]byte, memrsv)) // terminating reservation entry
	out.Write(b.structb.Bytes())
	out.Write(b.strings.Bytes())
	return out.Bytes()
}
