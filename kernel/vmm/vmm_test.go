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

// TestWriteGStage checks the Sv39x4 G-stage tables map a multi-megapage region:
// the right root/level-1 indices, a non-leaf root pointer to the level-1 table,
// and one G-stage leaf (RWX|U|A|D, correct PPN) per megapage. A wrong U bit, a
// wrong index, or an off-by-one in the size->megapage-count math is exactly the
// "works on QEMU / silent guest-page fault" class, so it is pinned here.
func TestWriteGStage(t *testing.T) {
	root := make([]byte, RootSize)
	l1 := make([]byte, L1Size)

	const (
		l1PA      = 0x9000_4000
		guestPA   = 0x9020_0000      // 2 MiB aligned host buffer
		guestBase = 0x8000_0000      // guest sees its RAM here
		size      = 2 * MegapageSize // two megapages -> two leaves
	)
	WriteGStage(root, l1, l1PA, guestPA, guestBase, size)

	vpn2 := uint64(guestBase) >> 30           // = 2
	vpn1 := (uint64(guestBase) >> 21) & 0x1ff // = 0
	if vpn2 != 2 || vpn1 != 0 {
		t.Fatalf("indices vpn2=%d vpn1=%d, want 2,0", vpn2, vpn1)
	}

	rootE := binary.LittleEndian.Uint64(root[vpn2*8:])
	if rootE&pteV == 0 || rootE&(pteR|pteW|pteX) != 0 {
		t.Fatalf("root entry %#x: want a valid non-leaf pointer", rootE)
	}
	if got := (rootE >> 10) << 12; got != l1PA {
		t.Fatalf("root entry points at %#x, want l1 table %#x", got, uint64(l1PA))
	}

	const wantFlags = pteV | pteR | pteW | pteX | pteU | pteA | pteD
	for i := uint64(0); i < 2; i++ { // one leaf per megapage
		leaf := binary.LittleEndian.Uint64(l1[(vpn1+i)*8:])
		if leaf&0xff != wantFlags {
			t.Fatalf("leaf %d flags = %#x, want %#x (RWX|U|A|D|V)", i, leaf&0xff, uint64(wantFlags))
		}
		if leaf&pteU == 0 {
			t.Fatalf("leaf %d U bit clear: G-stage accesses are U-mode and would fault", i)
		}
		if got := (leaf >> 10) << 12; got != uint64(guestPA)+i*MegapageSize {
			t.Fatalf("leaf %d maps to %#x, want %#x", i, got, uint64(guestPA)+i*MegapageSize)
		}
	}
	// The third level-1 slot (beyond the mapped region) stays invalid.
	if v := binary.LittleEndian.Uint64(l1[(vpn1+2)*8:]); v != 0 {
		t.Fatalf("l1[%d] = %#x, want 0 (only two megapages mapped)", vpn1+2, v)
	}
	// Every root slot but vpn2 stays invalid.
	for i := 0; i < RootSize/8; i++ {
		if uint64(i) == vpn2 {
			continue
		}
		if v := binary.LittleEndian.Uint64(root[i*8:]); v != 0 {
			t.Fatalf("root[%d] = %#x, want 0", i, v)
		}
	}
}

// TestWriteVSStage checks the VS-stage (ordinary Sv39) identity map honk seeds
// for a paging guest: a non-leaf root pointer, and one identity leaf per
// megapage with RWX|A|D but NOT U (the guest runs in VS-mode/supervisor, and an
// S-mode fetch of a U=1 page faults). The missing-U requirement is the inverse
// of the G-stage's required-U, so it is pinned separately.
func TestWriteVSStage(t *testing.T) {
	root := make([]byte, PageTableSize)
	l1 := make([]byte, PageTableSize)

	const (
		l1PA      = 0x8000_5000 // guest-physical address of the level-1 table
		guestBase = 0x8000_0000
		size      = 2 * MegapageSize
	)
	WriteVSStage(root, l1, l1PA, guestBase, size)

	vpn2 := uint64(guestBase) >> 30
	rootE := binary.LittleEndian.Uint64(root[vpn2*8:])
	if rootE&pteV == 0 || rootE&(pteR|pteW|pteX) != 0 {
		t.Fatalf("VS root entry %#x: want a valid non-leaf pointer", rootE)
	}
	if got := (rootE >> 10) << 12; got != l1PA {
		t.Fatalf("VS root points at %#x, want l1 %#x", got, uint64(l1PA))
	}

	const wantFlags = pteV | pteR | pteW | pteX | pteA | pteD // NO U
	for i := uint64(0); i < 2; i++ {
		leaf := binary.LittleEndian.Uint64(l1[i*8:])
		if leaf&0xff != wantFlags {
			t.Fatalf("VS leaf %d flags = %#x, want %#x (RWX|A|D|V, no U)", i, leaf&0xff, uint64(wantFlags))
		}
		if leaf&pteU != 0 {
			t.Fatalf("VS leaf %d has U set: an S-mode fetch of a U page faults", i)
		}
		if got := (leaf >> 10) << 12; got != uint64(guestBase)+i*MegapageSize {
			t.Fatalf("VS leaf %d maps %#x, want identity %#x", i, got, uint64(guestBase)+i*MegapageSize)
		}
	}
}

// TestGuestRange pins the bounds discipline honk's device emulation depends on:
// a guest-physical range is accepted only if it lies wholly within the guest's
// RAM, and a bad pointer (before base, past the end, length overflow) is
// refused rather than read out of bounds. This is the keystone check every
// backend that follows a guest pointer reuses, so its edges are tested here.
func TestGuestRange(t *testing.T) {
	const base, size = 0x8000_0000, 0x20_0000 // 2 MiB of guest RAM

	check := func(name string, gpa, length uint64, wantStart, wantEnd uint64, wantOK bool) {
		t.Helper()
		start, end, ok := GuestRange(gpa, length, base, size)
		if ok != wantOK || (ok && (start != wantStart || end != wantEnd)) {
			t.Fatalf("%s: GuestRange(%#x,%#x) = (%#x,%#x,%v), want (%#x,%#x,%v)",
				name, gpa, length, start, end, ok, wantStart, wantEnd, wantOK)
		}
	}

	check("in range", base+0x400, 4, 0x400, 0x404, true)
	check("at base", base, 16, 0, 16, true)
	check("exact end", base+size-8, 8, size-8, size, true) // ends exactly at RAM end
	check("zero length", base+0x1000, 0, 0, 0, true)       // empty: valid
	check("below base", base-1, 4, 0, 0, false)
	check("past end", base+size-2, 4, 0, 0, false) // straddles the end
	check("far above", base+size+0x1000, 4, 0, 0, false)
	check("length overflow", base+0x1000, ^uint64(0), 0, 0, false) // start+length wraps
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

// TestDBCNGuestEncoding decodes the generated DBCN payload and asserts its
// structure: it probes SBI Base for the DBCN EID and branches to shutdown on
// absence, builds + stores the "dbcn" token into its own RAM, then issues a DBCN
// console_write (a7=DBCN EID, a6=write FID) before shutting down. A wrong EID,
// FID, or branch target is the "silent guest fault" class; QEMU then proves honk
// actually reads the buffer back through the G-stage.
func TestDBCNGuestEncoding(t *testing.T) {
	const token = 'd' | 'b'<<8 | 'c'<<16 | 'n'<<24
	code := decode(t, DBCNGuest())

	// probe: li a7,Base; li a6,probe; loadImm32 a0,DBCN; ecall; beq a1,x0,shutdown.
	if op, rd, _, _, imm := fields(code[0]); op != 0x13 || rd != regA7 || imm != SBIExtBase {
		t.Fatalf("code[0] = %#08x, want li a7,%#x (Base)", code[0], SBIExtBase)
	}
	if op, rd, _, _, imm := fields(code[1]); op != 0x13 || rd != regA6 || imm != SBIBaseProbeExtension {
		t.Fatalf("code[1] = %#08x, want li a6,%d (probe)", code[1], SBIBaseProbeExtension)
	}
	if got := luiAddiVal(code[2], code[3]); got != int64(SBIExtDBCN) {
		t.Fatalf("probe a0 builds %#x, want DBCN EID %#x", got, SBIExtDBCN)
	}
	if code[4] != opEcall {
		t.Fatalf("code[4] = %#08x, want ecall", code[4])
	}

	// shutdown sequence (li a7,8; ecall; j .) at the tail.
	sd := len(code) - 3
	if op, rd, _, _, imm := fields(code[sd]); op != 0x13 || rd != regA7 || imm != SBIShutdown {
		t.Fatalf("shutdown = %#08x, want li a7,%d", code[sd], SBIShutdown)
	}
	if code[sd+1] != opEcall || code[sd+2] != opJ0 {
		t.Fatalf("shutdown tail = %#08x %#08x", code[sd+1], code[sd+2])
	}
	// the probe branch (code[5]) targets the shutdown sequence on DBCN absence.
	if code[5]&0x707f != 0x0063 { // B-type, funct3=0 => beq
		t.Fatalf("code[5] = %#08x, want beq (probe-fail branch)", code[5])
	}
	if tgt := 5 + beqOff(code[5])/4; tgt != sd {
		t.Fatalf("probe-fail branch targets word %d, want shutdown at %d", tgt, sd)
	}

	// the token "dbcn" is built (lui+addi) and the DBCN write call sets a7=DBCN
	// EID (lui+addi) immediately followed by a6=write FID. Match only real
	// lui+addi pairs so the value reconstruction can't coincide on other words.
	isLUI := func(w uint32) bool { return w&0x7f == 0x37 }
	isADDI := func(w uint32) bool { return w&0x707f == 0x13 }
	var storedToken, wroteDBCN bool
	for i := 6; i+1 < sd; i++ {
		if !isLUI(code[i]) || !isADDI(code[i+1]) {
			continue
		}
		switch luiAddiVal(code[i], code[i+1]) {
		case int64(token):
			storedToken = true
		case int64(SBIExtDBCN):
			if i+2 < sd {
				if op, rd, _, _, imm := fields(code[i+2]); op == 0x13 && rd == regA6 && imm == SBIDBCNWrite {
					wroteDBCN = true
				}
			}
		}
	}
	if !storedToken {
		t.Fatal("the 'dbcn' token is never built (no lui+addi of it)")
	}
	if !wroteDBCN {
		t.Fatal("no DBCN console_write call (lui+addi a7,DBCN; li a6,write) found")
	}
}

// imm decoders for the variable-length jump/branch formats, used to verify the
// generated TimerGuest's control flow lands where intended.
func jalOff(in uint32) int {
	imm := (in>>31&1)<<20 | (in>>21&0x3ff)<<1 | (in>>20&1)<<11 | (in>>12&0xff)<<12
	if imm&(1<<20) != 0 {
		imm |= ^uint32((1 << 21) - 1)
	}
	return int(int32(imm))
}

func beqOff(in uint32) int {
	imm := (in>>31&1)<<12 | (in>>7&1)<<11 | (in>>25&0x3f)<<5 | (in>>8&0xf)<<1
	if imm&(1<<12) != 0 {
		imm |= ^uint32((1 << 13) - 1)
	}
	return int(int32(imm))
}

// bneOff decodes a bne's branch offset (the B-type immediate is scrambled
// identically to beq's).
func bneOff(in uint32) int { return beqOff(in) }

// luiAddiVal reconstructs the value built by a lui;addi pair (loadImm32). The
// addi immediate is a signed 12-bit field in bits [31:20], so it is
// sign-extended via int32 before the arithmetic shift.
func luiAddiVal(luiW, addiW uint32) int64 {
	return int64(int32(luiW&0xfffff000)) + int64(int32(addiW)>>20)
}

// TestEncoders checks the instruction encoders against the RV64 formats: the
// fixed-format ones against hand-computed words, and the variable-immediate
// ones (lui+addi, lui-address, jal, beq) by round-tripping representative
// values - including the bit-31 address case loadAddr exists to handle.
func TestEncoders(t *testing.T) {
	if got := addi(regA7, regZero, 8); got != 0x00800893 {
		t.Errorf("addi a7,zero,8 = %#08x, want 0x00800893", got)
	}
	if got := add(regA0, regA0, regT1); got != 0x00650533 {
		t.Errorf("add a0,a0,t1 = %#08x, want 0x00650533", got)
	}
	if opWFI != 0x10500073 || opSret != 0x10200073 {
		t.Errorf("wfi/sret = %#08x/%#08x", opWFI, opSret)
	}

	// loadImm32: a 32-bit value with bit 31 clear (e.g. the TIME EID) must
	// reconstruct exactly through the sign-extending addi.
	for _, v := range []uint32{0, 1, 0x7ff, 0x800, 0xc350, 0x12345678, SBIExtTime} {
		w := loadImm32(regA7, v)
		if got := luiAddiVal(w[0], w[1]); got != int64(v) {
			t.Errorf("loadImm32(%#x) reconstructs %#x", v, got)
		}
	}

	// loadAddr builds 0x8000_0000+off positively (lui would sign-extend it).
	a := loadAddr(regT0, 100)
	if a[0] != addi(regT0, regZero, 1) || a[1] != slli(regT0, regT0, 31) || a[2] != addi(regT0, regT0, 100) {
		t.Errorf("loadAddr words = %#08x %#08x %#08x", a[0], a[1], a[2])
	}

	// jal/beq/bne offsets, including negative (a backward self-loop) and the
	// odd-bit scrambling.
	for _, off := range []int{0, 4, -4, 40, 100, -100, 2044, -2048} {
		if got := jalOff(jal(regZero, off)); got != off {
			t.Errorf("jal off %d round-trips to %d", off, got)
		}
		if got := beqOff(beq(regS0, regZero, off)); got != off {
			t.Errorf("beq off %d round-trips to %d", off, got)
		}
		if got := bneOff(bne(regS0, regZero, off)); got != off {
			t.Errorf("bne off %d round-trips to %d", off, got)
		}
	}

	// sfence.vma and the doubleword load/store the paging guest uses.
	if opSfenceVMA != 0x12000073 {
		t.Errorf("sfence.vma = %#08x, want 0x12000073", opSfenceVMA)
	}
	if got := ld(regT2, regT1, 0); got != 0x00033383 {
		t.Errorf("ld t2,0(t1) = %#08x, want 0x00033383", got)
	}
	if got := sd(regT0, regT1, 0); got != 0x00533023 {
		t.Errorf("sd t0,0(t1) = %#08x, want 0x00533023", got)
	}
}

// TestPagingGuestEncoding decodes the generated paging payload and asserts its
// structure: it builds vsatp = Sv39<<60 | (vsRootGPA>>12), loads it into satp
// and fences, then stores+loads a sentinel and branches (bne) past the success
// print to the shutdown sequence on a mismatch. A wrong vsatp or a mis-scrambled
// branch is the "silent guest fault" class; QEMU then proves the H-extension
// actually translates through it.
func TestPagingGuestEncoding(t *testing.T) {
	const vsRootGPA = GuestBase + VSRootOff
	code := decode(t, PagingGuest("hi", vsRootGPA))

	// prologue: li t0,Sv39; slli t0,t0,60; lui/addi t1,ppn; add t0,t0,t1;
	// csrw satp,t0; sfence.vma.
	if op, rd, _, _, imm := fields(code[0]); op != 0x13 || rd != regT0 || imm != SatpSv39 {
		t.Fatalf("code[0] = %#08x, want li t0,%d (Sv39)", code[0], SatpSv39)
	}
	if code[1] != slli(regT0, regT0, 60) {
		t.Fatalf("code[1] = %#08x, want slli t0,t0,60", code[1])
	}
	if got := luiAddiVal(code[2], code[3]); got != int64(uint64(vsRootGPA)>>12) {
		t.Fatalf("ppn builds %#x, want vsRootGPA>>12 = %#x", got, uint64(vsRootGPA)>>12)
	}
	if code[4] != add(regT0, regT0, regT1) {
		t.Fatalf("code[4] = %#08x, want add t0,t0,t1", code[4])
	}
	if code[5] != csrw(csrSatp, regT0) {
		t.Fatalf("code[5] = %#08x, want csrw satp,t0", code[5])
	}
	if code[6] != opSfenceVMA {
		t.Fatalf("code[6] = %#08x, want sfence.vma", code[6])
	}

	// The payload ends in the shutdown sequence (li a7,8; ecall; j .).
	sd := len(code) - 3
	if op, rd, _, _, imm := fields(code[sd]); op != 0x13 || rd != regA7 || imm != SBIShutdown {
		t.Fatalf("shutdown = %#08x, want li a7,%d", code[sd], SBIShutdown)
	}
	if code[sd+1] != opEcall || code[sd+2] != opJ0 {
		t.Fatalf("shutdown tail = %#08x %#08x", code[sd+1], code[sd+2])
	}

	// The sentinel-verify branch is a bne that skips the success print on
	// mismatch, targeting the shutdown sequence.
	var found bool
	for i := 0; i < sd; i++ {
		if code[i]&0x707f == 0x1063 { // B-type, funct3=1 => bne
			if tgt := i + bneOff(code[i])/4; tgt != sd {
				t.Fatalf("bne targets word %d, want shutdown at %d", tgt, sd)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no bne (sentinel verify branch) found")
	}
}

// TestTimerGuest decodes the generated M12 payload and asserts its structure:
// it probes SBI Base for TIME, installs a handler whose address is the in-image
// offset (4-aligned), idles in a wfi self-loop, and its handler prints the tick
// and either reprograms the timer or shuts down. A wrong handler address or a
// mis-scrambled branch is exactly the "silent guest fault" class, so it is
// pinned here; QEMU then proves the H-extension actually delivers the timer.
func TestTimerGuest(t *testing.T) {
	const guestBase = 0x8000_0000
	tick := byte('*')
	code := decode(t, TimerGuest(tick, 5))

	// The handler address is loadAddr(t0, handlerOff): li t0,1; slli t0,31;
	// addi t0,t0,handlerOff. Recover the offset and check it points at the
	// handler's first instruction (li a7, console_putchar) and is 4-aligned.
	if code[0] != addi(regT0, regZero, 1) || code[1] != slli(regT0, regT0, 31) {
		t.Fatalf("prologue does not start with a guest-address load: %#08x %#08x", code[0], code[1])
	}
	handlerOff := int(int32(code[2]) >> 20)
	if handlerOff%4 != 0 || handlerOff/4 >= len(code) {
		t.Fatalf("handler offset %d invalid (len %d words)", handlerOff, len(code))
	}
	h := handlerOff / 4
	if op, rd, _, _, imm := fields(code[h]); op != 0x13 || rd != regA7 || imm != SBIConsolePutchar {
		t.Fatalf("handler[0] = %#08x, want li a7,%d (console_putchar)", code[h], SBIConsolePutchar)
	}
	if op, rd, _, _, imm := fields(code[h+1]); op != 0x13 || rd != regA0 || imm != int(tick) {
		t.Fatalf("handler[1] = %#08x, want li a0,%d (tick char)", code[h+1], tick)
	}

	// The probe must be li a7,Base; li a6,probe; loadImm32 a0,TIME; ecall; beq.
	if op, rd, _, _, imm := fields(code[9]); op != 0x13 || rd != regA7 || imm != SBIExtBase {
		t.Fatalf("probe a7 = %#08x, want li a7,%#x (Base)", code[9], SBIExtBase)
	}
	if op, rd, _, _, imm := fields(code[10]); op != 0x13 || rd != regA6 || imm != SBIBaseProbeExtension {
		t.Fatalf("probe a6 = %#08x, want li a6,%d (probe_extension)", code[10], SBIBaseProbeExtension)
	}
	if got := luiAddiVal(code[11], code[12]); got != SBIExtTime {
		t.Fatalf("probe a0 builds %#x, want TIME EID %#x", got, SBIExtTime)
	}
	if code[13] != opEcall {
		t.Fatalf("probe insn = %#08x, want ecall", code[13])
	}
	// The probe-fail branch (code[14]) and the handler-countdown branch both
	// target the shutdown sequence (the final li a7,8; ecall; j .).
	sd := len(code) - 3
	if op, rd, _, _, imm := fields(code[sd]); op != 0x13 || rd != regA7 || imm != SBIShutdown {
		t.Fatalf("shutdown = %#08x, want li a7,%d", code[sd], SBIShutdown)
	}
	if code[sd+1] != opEcall || code[sd+2] != opJ0 {
		t.Fatalf("shutdown tail = %#08x %#08x", code[sd+1], code[sd+2])
	}
	if tgt := 14 + beqOff(code[14])/4; tgt != sd {
		t.Fatalf("probe-fail branch targets word %d, want shutdown at %d", tgt, sd)
	}

	// The wait loop is a wfi followed by a jal x0 back onto itself (offset -4).
	var waited bool
	for i := 0; i+1 < len(code); i++ {
		if code[i] == opWFI && (code[i+1]&0x7f) == 0x6f && jalOff(code[i+1]) == -4 {
			waited = true
		}
	}
	if !waited {
		t.Fatal("no wfi; jal x0,-4 wait loop found")
	}
}
