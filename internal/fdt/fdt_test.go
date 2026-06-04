package fdt

import "testing"

// buildFDT emits a small but valid flattened device tree resembling QEMU virt:
// a root with 2/2 cells, a /memory node, and /cpus with one cpu and a timebase.
func buildFDT() []byte {
	var strs []byte
	off := map[string]uint32{}
	addStr := func(s string) uint32 {
		if o, ok := off[s]; ok {
			return o
		}
		o := uint32(len(strs))
		off[s] = o
		strs = append(append(strs, s...), 0)
		return o
	}

	var st []byte
	put32 := func(v uint32) { st = append(st, byte(v>>24), byte(v>>16), byte(v>>8), byte(v)) }
	pad := func() {
		for len(st)%4 != 0 {
			st = append(st, 0)
		}
	}
	begin := func(name string) { put32(tokBeginNode); st = append(append(st, name...), 0); pad() }
	end := func() { put32(tokEndNode) }
	prop := func(name string, val []byte) {
		put32(tokProp)
		put32(uint32(len(val)))
		put32(addStr(name))
		st = append(st, val...)
		pad()
	}

	u32 := func(v uint32) []byte { return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }
	u64 := func(v uint64) []byte {
		b := make([]byte, 8)
		for i := range b {
			b[i] = byte(v >> (56 - 8*i))
		}
		return b
	}
	str := func(s string) []byte { return append([]byte(s), 0) }

	begin("") // root
	prop("#address-cells", u32(2))
	prop("#size-cells", u32(2))
	prop("model", str("honk-test,virt"))
	begin("memory@80000000")
	prop("device_type", str("memory"))
	prop("reg", append(u64(0x80000000), u64(0x20000000)...))
	end()
	begin("cpus")
	prop("#address-cells", u32(1))
	prop("#size-cells", u32(0))
	prop("timebase-frequency", u32(10_000_000))
	begin("cpu@0")
	prop("device_type", str("cpu"))
	prop("riscv,isa-extensions", append(str("sstc"), str("zicsr")...)) // stringlist
	end()
	end()
	end() // root
	put32(tokEnd)

	const hdr = 40
	structOff, stringsOff := uint32(hdr), uint32(hdr)+uint32(len(st))
	total := stringsOff + uint32(len(strs))
	h := make([]byte, hdr)
	be := func(o int, v uint32) { h[o], h[o+1], h[o+2], h[o+3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v) }
	be(0, magic)
	be(4, total)
	be(8, structOff)
	be(12, stringsOff)
	be(16, hdr) // off_mem_rsvmap (unused by this parser)
	be(20, 17)  // version
	be(24, 16)  // last_comp_version
	be(32, uint32(len(strs)))
	be(36, uint32(len(st)))
	return append(append(h, st...), strs...)
}

func TestParse(t *testing.T) {
	tr, err := Parse(buildFDT())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := tr.Model(); got != "honk-test,virt" {
		t.Errorf("Model() = %q, want %q", got, "honk-test,virt")
	}
	base, size, ok := tr.Memory()
	if !ok || base != 0x80000000 || size != 0x20000000 {
		t.Errorf("Memory() = %#x, %#x, %v; want 0x80000000, 0x20000000, true", base, size, ok)
	}
	if hz, ok := tr.TimebaseFrequency(); !ok || hz != 10_000_000 {
		t.Errorf("TimebaseFrequency() = %d, %v; want 10000000, true", hz, ok)
	}
	if n := tr.HartCount(); n != 1 {
		t.Errorf("HartCount() = %d, want 1", n)
	}
}

func TestHartHasExtension(t *testing.T) {
	tr, err := Parse(buildFDT())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, ext := range []string{"sstc", "zicsr"} {
		if !tr.HartHasExtension(ext) {
			t.Errorf("HartHasExtension(%q) = false, want true", ext)
		}
	}
	if tr.HartHasExtension("v") {
		t.Error("HartHasExtension(\"v\") = true, want false")
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	cases := map[string][]byte{
		"empty":     nil,
		"short":     make([]byte, 8),
		"bad magic": make([]byte, 40),
		"truncated": buildFDT()[:24],
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			// Must return an error, never panic, on malformed firmware input.
			if _, err := Parse(b); err == nil {
				t.Errorf("Parse(%s) = nil error, want error", name)
			}
		})
	}
}
