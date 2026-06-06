package vmm

import (
	"encoding/binary"
	"testing"
)

// decode splits a little-endian RV64 instruction stream into 32-bit words.
func decode(t *testing.T, code []byte) []uint32 {
	t.Helper()
	if len(code)%4 != 0 {
		t.Fatalf("code length %d not a multiple of 4", len(code))
	}
	out := make([]uint32, len(code)/4)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(code[4*i:])
	}
	return out
}

// fields pulls the I-type / opcode fields out of an instruction word.
func fields(in uint32) (opcode, rd, funct3, rs1 uint32, imm int) {
	opcode = in & 0x7f
	rd = (in >> 7) & 0x1f
	funct3 = (in >> 12) & 0x7
	rs1 = (in >> 15) & 0x1f
	imm = int(in >> 20) // small positive immediates only, no sign extension needed
	return
}

// TestDemoGuestEncoding decodes the generated payload and asserts it is exactly
// the (li a7,EID; li a0,char; ecall) triples for each message byte, terminated
// by a shutdown ecall and a self-loop. This is the authoritative check that the
// hand-rolled guest is the program we think it is - the QEMU smoke test then
// proves the H-extension actually runs it.
func TestDemoGuestEncoding(t *testing.T) {
	msg := "hi\n"
	code := decode(t, DemoGuest(msg))

	want := 3*len(msg) + 3 // 3 per char + (li a7,8; ecall; j .)
	if len(code) != want {
		t.Fatalf("instruction count = %d, want %d", len(code), want)
	}

	for i, c := range []byte(msg) {
		base := i * 3
		// li a7, SBIConsolePutchar
		op, rd, f3, rs1, imm := fields(code[base])
		if op != 0x13 || rd != regA7 || f3 != 0 || rs1 != 0 || imm != SBIConsolePutchar {
			t.Fatalf("char %d insn0 = %#08x, want li a7,%d", i, code[base], SBIConsolePutchar)
		}
		// li a0, c
		op, rd, f3, rs1, imm = fields(code[base+1])
		if op != 0x13 || rd != regA0 || f3 != 0 || rs1 != 0 || imm != int(c) {
			t.Fatalf("char %d insn1 = %#08x, want li a0,%d", i, code[base+1], c)
		}
		// ecall
		if code[base+2] != opEcall {
			t.Fatalf("char %d insn2 = %#08x, want ecall", i, code[base+2])
		}
	}

	tail := 3 * len(msg)
	if op, rd, _, _, imm := fields(code[tail]); op != 0x13 || rd != regA7 || imm != SBIShutdown {
		t.Fatalf("tail li a7 = %#08x, want li a7,%d", code[tail], SBIShutdown)
	}
	if code[tail+1] != opEcall {
		t.Fatalf("tail ecall = %#08x, want ecall", code[tail+1])
	}
	if code[tail+2] != opJ0 {
		t.Fatalf("tail guard = %#08x, want j . (%#08x)", code[tail+2], uint32(opJ0))
	}
}

// TestWriteGStage checks the Sv39x4 G-stage tables map guestBase to guestPA via
// a 2 MiB megapage: the right root/level-1 indices, a non-leaf root pointer to
// the level-1 table, and a level-1 leaf with the guest's PPN and RWX|U|A|D. A
// wrong U bit or wrong index is exactly the "works on QEMU / silent guest-page
// fault" class, so it is pinned here.
func TestWriteGStage(t *testing.T) {
	root := make([]byte, RootSize)
	l1 := make([]byte, L1Size)

	const (
		rootPA    = 0x9000_0000
		l1PA      = 0x9000_4000
		guestPA   = 0x9020_0000 // 2 MiB aligned host buffer
		guestBase = 0x8000_0000 // guest sees its RAM here
	)
	WriteGStage(root, l1, rootPA, l1PA, guestPA, guestBase)

	// Indices for guestBase 0x8000_0000.
	vpn2 := (uint64(guestBase) >> 30) & 0x7ff // = 2
	vpn1 := (uint64(guestBase) >> 21) & 0x1ff // = 0
	if vpn2 != 2 || vpn1 != 0 {
		t.Fatalf("indices vpn2=%d vpn1=%d, want 2,0", vpn2, vpn1)
	}

	rootE := binary.LittleEndian.Uint64(root[vpn2*8:])
	if rootE&pteV == 0 {
		t.Fatal("root entry not valid")
	}
	if rootE&(pteR|pteW|pteX) != 0 {
		t.Fatalf("root entry %#x has R/W/X set; must be a non-leaf pointer", rootE)
	}
	if got := (rootE >> 10) << 12; got != l1PA {
		t.Fatalf("root entry points at %#x, want l1 table %#x", got, uint64(l1PA))
	}

	leaf := binary.LittleEndian.Uint64(l1[vpn1*8:])
	const wantFlags = pteV | pteR | pteW | pteX | pteU | pteA | pteD
	if leaf&0xff != wantFlags {
		t.Fatalf("leaf flags = %#x, want %#x (RWX|U|A|D|V)", leaf&0xff, uint64(wantFlags))
	}
	if leaf&pteU == 0 {
		t.Fatal("leaf U bit clear: G-stage accesses are U-mode and would fault")
	}
	if got := (leaf >> 10) << 12; got != guestPA {
		t.Fatalf("leaf maps to %#x, want guest RAM %#x", got, uint64(guestPA))
	}

	// Every other root/level-1 slot must be zero (invalid).
	for i := 0; i < RootSize/8; i++ {
		if uint64(i) == vpn2 {
			continue
		}
		if v := binary.LittleEndian.Uint64(root[i*8:]); v != 0 {
			t.Fatalf("root[%d] = %#x, want 0", i, v)
		}
	}
}

// TestHgatp checks the hgatp value selects Sv39x4 and encodes the root PPN.
func TestHgatp(t *testing.T) {
	const rootPA = 0x9000_0000
	h := Hgatp(rootPA)
	if mode := h >> 60; mode != HgatpSv39x4 {
		t.Fatalf("hgatp MODE = %d, want %d (Sv39x4)", mode, HgatpSv39x4)
	}
	if ppn := (h & ((1 << 44) - 1)); ppn != rootPA>>12 {
		t.Fatalf("hgatp PPN = %#x, want %#x", ppn, uint64(rootPA)>>12)
	}
}
