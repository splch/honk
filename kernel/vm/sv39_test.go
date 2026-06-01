package vm

import "testing"

// These tests run on your laptop — `go test ./kernel/vm` — with no QEMU and no
// hardware. Being able to unit-test page-table logic is one of Honk's reasons to
// exist; a C teaching kernel can't do this.

func TestPTEPack(t *testing.T) {
	pa := uintptr(0x80200000)
	p := makePTE(pa, Read|Write|Exec)
	if p.PA() != pa {
		t.Errorf("PA round-trip: got %#x, want %#x", p.PA(), pa)
	}
	if !p.Leaf() {
		t.Error("an R/W/X entry should be a leaf")
	}
	if p&Valid == 0 {
		t.Error("makePTE should set Valid")
	}
	if p.Flags() != Valid|Read|Write|Exec {
		t.Errorf("flags = %#b", p.Flags())
	}
}

func TestVPNSplit(t *testing.T) {
	// 0x8020_0000 decomposes as VPN[2]=2, VPN[1]=1, VPN[0]=0, offset=0.
	va := uintptr(0x80200000)
	for level, want := range map[int]int{2: 2, 1: 1, 0: 0} {
		if got := vpn(level, va); got != want {
			t.Errorf("vpn(%d, %#x) = %d, want %d", level, va, got, want)
		}
	}
}

func TestMapAndLookup(t *testing.T) {
	m := NewMapper()
	m.Map(0x10000000, 0x10000000, Read|Write)        // a device page (no exec, no user)
	m.Map(0x80200000, 0x81000000, Read|Exec|User)    // a user code page, va != pa

	if pte, ok := m.Lookup(0x80200000); !ok {
		t.Fatal("mapped code page should resolve")
	} else if pte.PA() != 0x81000000 || pte&Exec == 0 || pte&User == 0 {
		t.Errorf("code page pte = %#x (pa=%#x)", pte, pte.PA())
	}
	if pte, ok := m.Lookup(0x10000000); !ok || pte&Write == 0 || pte&User != 0 {
		t.Errorf("device page pte = %#x, ok=%v", pte, ok)
	}
	if _, ok := m.Lookup(0x40000000); ok {
		t.Error("an unmapped address must not resolve")
	}
}

func TestMakeSATP(t *testing.T) {
	const root = 0x80210000
	satp := MakeSATP(root)
	if mode := satp >> 60; mode != 8 {
		t.Errorf("MODE = %d, want 8 (Sv39)", mode)
	}
	if ppn := satp & (1<<44 - 1); ppn != root>>12 {
		t.Errorf("root PPN = %#x, want %#x", ppn, root>>12)
	}
}
