// Package vmm holds the pure, host-testable core of honk's RISC-V H-extension
// hypervisor (HONK.md Phase E, milestone M11): the bits that are just logic -
// the guest payload's instruction encoding and the G-stage (guest-physical ->
// supervisor-physical) page-table layout.
//
// The hardware-contact half - the H/VS CSR programming and the HS<->VS world
// switch - is necessarily bare-metal assembly and lives in board/virt
// (vmm.go + vmm_riscv64.s). Splitting the encodable logic out here keeps it
// behind a `go test`able boundary (HONK.md §8: pure logic is host-tested, the
// hardware is proven in QEMU), exactly as the kv/image/p9 packages are.
//
// This package has no bare-metal dependency and imports only encoding/binary.
package vmm

import "encoding/binary"

// Trap cause codes the H-extension adds (RISC-V Privileged ISA, hypervisor
// chapter). A trap taken from VS-mode into HS-mode reports these in scause.
const (
	// EcallFromVS is scause for an environment call from VS-mode - the guest's
	// SBI call, which honk's VMM emulates. (Ecall from U/VU is 8; HS/S is 9.)
	EcallFromVS = 10

	// Guest-page-fault causes: the guest's address could not be translated by
	// the G-stage page table. They are fatal for M11 (the map is static and
	// complete); reporting them aids debugging a wrong map.
	InstGuestPageFault  = 20
	LoadGuestPageFault  = 21
	StoreGuestPageFault = 23
)

// hgatp.MODE encodings (RV64). Sv39x4 is "Sv39 with a 2-bit-wider guest
// physical address", so its root page table is four pages (16 KiB). SatpSv39 is
// the satp/vsatp MODE for ordinary Sv39 - the guest's own (VS-stage) paging.
const (
	HgatpBare   = 0
	HgatpSv39x4 = 8
	SatpSv39    = 8
)

// Sv39 page-table entry bits (the G-stage uses the identical PTE format as
// ordinary Sv39, including the U bit). G-stage accesses are always checked as
// U-mode, so every valid G-stage leaf must set U; A/D are pre-set since honk
// does not swap (the Svade-portable choice, RV64.md §5.3).
const (
	pteV = 1 << 0 // valid
	pteR = 1 << 1 // readable
	pteW = 1 << 2 // writable
	pteX = 1 << 3 // executable
	pteU = 1 << 4 // user (required for G-stage leaves)
	pteA = 1 << 6 // accessed
	pteD = 1 << 7 // dirty
)

// LeafRWX is the permission set honk maps guest RAM with in the G-stage: read,
// write, execute, user, accessed, dirty. G-stage accesses are checked as
// U-mode, so the U bit is required; A/D are pre-set since honk does not swap
// (the Svade-portable choice, RV64.md §5.3). (G is reserved in G-stage PTEs.)
const LeafRWX = pteV | pteR | pteW | pteX | pteU | pteA | pteD

// VSLeafRWX is the permission set for VS-stage (the guest's own Sv39) leaves:
// read, write, execute, accessed, dirty - but NOT user, because the guest runs
// in VS-mode (supervisor) and an S-mode fetch of a U=1 page faults (RV64.md
// §5.5). honk seeds a guest an identity VS map with these bits so a hand-rolled
// payload can prove two-stage translation by turning on its own paging.
const VSLeafRWX = pteV | pteR | pteW | pteX | pteA | pteD

// MegapageSize is the size of a level-1 Sv39 leaf (a 2 MiB "megapage"). honk
// maps a guest as a run of these, so its RAM must be 2 MiB-aligned.
const MegapageSize = 2 << 20

// Table geometry. The Sv39x4 G-stage root is 16 KiB (2048 entries, 16 KiB-
// aligned); every other table - a G-stage level-1 table, and the VS-stage root
// and level-1 table (ordinary Sv39, 512 entries) - is one 4 KiB page.
const (
	RootSize      = 16384
	L1Size        = 4096
	PageTableSize = 4096
)

// Guest memory layout, shared by the payload generators and the runtime so the
// guest-physical addresses they each compute agree (one owner of the decision).
// The guest's RAM begins at GuestBase, like a real RISC-V machine at
// 0x8000_0000; honk's G-stage maps it. VSRootOff/VSL1Off place the VS-stage page
// tables honk seeds into the guest's RAM, page-aligned and clear of the (small)
// payload at offset 0.
const (
	GuestBase = 0x8000_0000
	VSRootOff = 0x4000
	VSL1Off   = 0x5000
)

// pte packs a physical address and flag bits into an Sv39 page-table entry:
// PPN occupies bits 53:10, so pa>>12 is shifted left by 10.
func pte(pa, flags uint64) uint64 { return (pa>>12)<<10 | flags }

// megapageCount is how many 2 MiB megapage leaves cover size bytes.
func megapageCount(size uint64) uint64 { return (size + MegapageSize - 1) / MegapageSize }

// mapMegapages writes the single root entry pointing at the level-1 table l1
// (at address l1PA), then fills consecutive level-1 entries mapping
// [base, base+size) to [pa, pa+size) as 2 MiB megapage leaves with leafFlags.
// It is the one place the megapage page-table layout lives, shared by the
// G-stage (Sv39x4) and VS-stage (Sv39) builders.
//
// base must be 1 GiB-aligned and size <= 1 GiB, so every leaf shares one root
// slot and one level-1 table (512 megapages = 1 GiB) - ample for honk's guests
// and keeping both tables to the simple two-level shape. (The root index is the
// same value for Sv39 and Sv39x4 at honk's GuestBase, so no per-mode mask.)
func mapMegapages(root, l1 []byte, l1PA, pa, base, size, leafFlags uint64) {
	vpn2 := base >> 30
	vpn1 := (base >> 21) & 0x1ff
	for i, n := uint64(0), megapageCount(size); i < n; i++ {
		binary.LittleEndian.PutUint64(l1[(vpn1+i)*8:], pte(pa+i*MegapageSize, leafFlags))
	}
	binary.LittleEndian.PutUint64(root[vpn2*8:], pte(l1PA, pteV))
}

// WriteGStage fills a 16-KiB Sv39x4 root and a 4-KiB level-1 table so that
// [guestBase, guestBase+size) (guest-physical) maps to [guestPA, guestPA+size)
// (supervisor-physical) as 2 MiB megapages with G-stage (RWX|U|A|D) leaves.
// l1PA is the supervisor-physical address of the level-1 table (honk is
// identity-mapped, so it equals the slice's address).
//
// The map is what hgatp = Hgatp(rootPA) then translates against.
func WriteGStage(root, l1 []byte, l1PA, guestPA, guestBase, size uint64) {
	mapMegapages(root, l1, l1PA, guestPA, guestBase, size, LeafRWX)
}

// WriteVSStage fills an ordinary Sv39 root (4 KiB) and level-1 table so the
// guest's own paging identity-maps [guestBase, guestBase+size): guest-virtual
// equals guest-physical, as 2 MiB VS-stage leaves (RWX|A|D, no U). honk seeds
// this into the guest's RAM; a guest that loads vsatp with this root and fences
// then runs under two-stage translation (VS-stage here, then the G-stage above).
// l1PA is the guest-physical address of the level-1 table.
func WriteVSStage(root, l1 []byte, l1PA, guestBase, size uint64) {
	mapMegapages(root, l1, l1PA, guestBase, guestBase, size, VSLeafRWX)
}

// Hgatp returns the hgatp value selecting Sv39x4 G-stage translation rooted at
// the table whose supervisor-physical address is rootPA. VMID is left 0 (honk
// runs one guest at a time).
func Hgatp(rootPA uint64) uint64 { return HgatpSv39x4<<60 | rootPA>>12 }

// GuestRange validates that the guest-physical range [gpa, gpa+length) lies
// within the guest's RAM [base, base+ramSize) and returns the matching offsets
// into the host buffer that backs that RAM (host index = gpa - base, since
// honk's G-stage maps guest RAM as one contiguous region). ok is false for a
// range that starts below base, runs past the end, or overflows - so the host
// emulator refuses a guest's bad pointer instead of reading out of bounds.
//
// This is the bounds discipline every device backend that follows a guest
// pointer (DBCN console, and the virtio backends to come) needs, so it lives
// once here and is host-tested against adversarial inputs. It is //go:nosplit
// because the emulator calls it from the non-descheduling guest-run region.
//
//go:nosplit
func GuestRange(gpa, length, base, ramSize uint64) (start, end uint64, ok bool) {
	if length == 0 {
		return 0, 0, true // empty range: valid, nothing to access
	}
	if gpa < base {
		return 0, 0, false
	}
	start = gpa - base
	end = start + length
	if end < start || end > ramSize { // length overflow, or past the end of RAM
		return 0, 0, false
	}
	return start, end, true
}

// MMIOAccess describes a guest load/store to an emulated device register, as
// decoded from the faulting instruction. The host emulator gets the faulting
// address from htval; this supplies the rest: direction, width, the register
// (rd for a load, rs2 for a store), and whether a load sign-extends.
type MMIOAccess struct {
	Store  bool
	Width  int // access width in bytes: 1, 2, 4, or 8
	Reg    int // rd for a load, rs2 for a store
	Signed bool
}

// DecodeMMIO decodes a 32-bit RV64 integer load/store into the fields the MMIO
// emulator needs. ok is false for anything that is not such an instruction -
// including the reserved funct3 widths - so the emulator reports an unexpected
// fault rather than silently mis-emulating a malformed access. It is the
// trap-and-emulate keystone every emulated device (interrupt controller, the
// virtio backends to come) reuses, so it lives once here and is host-tested.
//
//go:nosplit
func DecodeMMIO(insn uint32) (MMIOAccess, bool) {
	funct3 := int((insn >> 12) & 7)
	switch insn & 0x7f {
	case 0x03: // LOAD: lb/lh/lw/ld (0..3), lbu/lhu/lwu (4..6); 7 reserved
		if funct3 == 7 {
			return MMIOAccess{}, false
		}
		return MMIOAccess{Width: 1 << (funct3 & 3), Reg: int((insn >> 7) & 0x1f), Signed: funct3 < 4}, true
	case 0x23: // STORE: sb/sh/sw/sd (0..3); 4..7 reserved
		if funct3 > 3 {
			return MMIOAccess{}, false
		}
		return MMIOAccess{Store: true, Width: 1 << funct3, Reg: int((insn >> 20) & 0x1f)}, true
	}
	return MMIOAccess{}, false
}

// SignExtend returns val as an integer load of the given width (1/2/4/8 bytes)
// would leave it in a register: truncated to the width, then sign-extended to
// 64 bits when signed (lb/lh/lw) rather than zero-extended (lbu/lhu/lwu/ld).
//
//go:nosplit
func SignExtend(val uint64, width int, signed bool) uint64 {
	if width >= 8 {
		return val
	}
	bits := uint(width) * 8
	val &= (uint64(1) << bits) - 1
	if signed && val&(uint64(1)<<(bits-1)) != 0 {
		val |= ^uint64(0) << bits
	}
	return val
}

// The guest payloads are hand-rolled VS-mode programs honk fully controls, so
// each milestone proves the H-extension mechanism against code it can decode
// and host-test (the instruction encoders live in encode.go, the SBI numbers in
// sbi.go). M11's DemoGuest is straight-line; M12's TimerGuest adds a trap
// handler and a timer loop.

// DemoGuest builds the M11 guest: a VS-mode payload that prints msg one
// character at a time via the legacy SBI console_putchar call, then asks the
// (emulated) SBI to shut down. Each character costs three instructions
// (li a7,1; li a0,c; ecall); the payload ends with a shutdown ecall and a
// self-loop guard. It returns raw little-endian RV64 machine code to copy into
// guest memory and execute at the guest's entry point.
//
// Only printable bytes (< 128) are used, so each char fits an addi immediate;
// callers pass plain ASCII.
func DemoGuest(msg string) []byte {
	var ins []uint32
	for i := 0; i < len(msg); i++ {
		ins = append(ins,
			addi(regA7, 0, SBIConsolePutchar), // li a7, 1
			addi(regA0, 0, int(msg[i])),       // li a0, <char>
			opEcall,                           // ecall -> VMM prints it
		)
	}
	ins = append(ins,
		addi(regA7, 0, SBIShutdown), // li a7, 8
		opEcall,                     // ecall -> VMM stops the guest
		opJ0,                        // j .   (unreached; guards a missed stop)
	)

	return assemble(ins)
}

// TimerGuest builds the M12 guest: a VS-mode payload that proves the timer +
// interrupt-injection path end to end. It probes the SBI Base extension for
// TIME (so the run ticks only if honk's Base/probe emulation works), installs
// its own VS trap vector, enables VS supervisor-timer interrupts, and arms an
// SBI timer. Each time honk injects a VS-timer interrupt, the guest's handler
// prints tick, reprograms the next timer, and returns; after ticks ticks it
// shuts down via SBI. tick must be a printable ASCII byte.
//
// The only data-dependent value is the handler's absolute address; the
// jump/branch offsets are computed from the emitted instruction indices, so
// editing the body cannot silently desynchronize them.
func TimerGuest(tick byte, ticks int) []byte {
	const (
		stie  = 1 << 5 // sie.STIE: VS supervisor-timer interrupt enable
		sie   = 1 << 1 // sstatus.SIE: VS global interrupt enable
		delta = 50000  // timer interval in time-CSR ticks (~5 ms at 10 MHz)
	)

	var ins []uint32
	emit := func(words ...uint32) { ins = append(ins, words...) }

	// arm sets a7/a6/a0 and ecalls SBI TIME set_timer(rdtime + delta). Used to
	// start the first timer and to reprogram from inside the handler.
	arm := func() {
		emit(rdtime(regA0))
		emit(loadImm32(regT1, delta)...)
		emit(add(regA0, regA0, regT1)) // a0 = now + delta
		emit(loadImm32(regA7, SBIExtTime)...)
		emit(addi(regA6, regZero, SBITimeSetTimer))
		emit(opEcall)
	}

	// prologue: install the handler, enable interrupts, init the tick counter.
	handlerLoad := len(ins)
	emit(loadAddr(regT0, 0)...)       // li t0, &handler  (offset patched below)
	emit(csrw(csrStvec, regT0))       // vstvec = handler
	emit(addi(regS0, regZero, ticks)) // s0 = remaining ticks (survives traps)
	emit(addi(regT0, regZero, stie))
	emit(csrs(csrSie, regT0)) // sie.STIE = 1
	emit(addi(regT0, regZero, sie))
	emit(csrs(csrSstatus, regT0)) // sstatus.SIE = 1

	// probe SBI Base for TIME; if unsupported, skip straight to shutdown.
	emit(addi(regA7, regZero, SBIExtBase))
	emit(addi(regA6, regZero, SBIBaseProbeExtension))
	emit(loadImm32(regA0, SBIExtTime)...)
	emit(opEcall)
	probeBranch := len(ins)
	emit(0) // beq a1, x0, shutdown  (patched)

	arm() // start the first timer

	// wait loop: idle until honk injects the timer interrupt.
	waitIdx := len(ins)
	emit(opWFI)
	waitJump := len(ins)
	emit(0) // jal x0, wait  (patched)

	// handler: print one tick, count down, reprogram or shut down.
	handlerIdx := len(ins)
	emit(addi(regA7, regZero, SBIConsolePutchar))
	emit(addi(regA0, regZero, int(tick)))
	emit(opEcall)
	emit(addi(regS0, regS0, -1))
	handlerBranch := len(ins)
	emit(0)      // beq s0, x0, shutdown  (patched)
	arm()        // reprogram the next tick (the set_timer also clears the injection)
	emit(opSret) // return to the wait loop

	// shutdown: end the run.
	shutdownIdx := len(ins)
	emit(addi(regA7, regZero, SBIShutdown))
	emit(opEcall)
	emit(opJ0) // guard against a missed stop

	// Patch the handler address (third word of loadAddr) and the relative
	// jump/branch offsets, now that every label index is known.
	ins[handlerLoad+2] = addi(regT0, regT0, handlerIdx*4)
	ins[probeBranch] = beq(regA1, regZero, (shutdownIdx-probeBranch)*4)
	ins[waitJump] = jal(regZero, (waitIdx-waitJump)*4)
	ins[handlerBranch] = beq(regS0, regZero, (shutdownIdx-handlerBranch)*4)

	return assemble(ins)
}

// PagingGuest builds a guest that proves two-stage translation by enabling its
// own VS-stage Sv39 paging. honk seeds an identity VS page table into the guest's
// RAM (WriteVSStage) whose root is at vsRootGPA; this payload loads vsatp with it
// (satp aliases to vsatp under V=1), fences, then - to prove the new map is live
// and honk's G-stage backs more than the first megapage - stores a sentinel
// through a guest-virtual address in the second megapage and reads it back,
// printing msg and halting only if it round-trips. A wrong map instead faults on
// the post-vsatp fetch or the store (reported by the run loop and leaving msg
// unprinted), so the printed msg is the end-to-end proof. msg is plain ASCII.
func PagingGuest(msg string, vsRootGPA uint64) []byte {
	const sentinel = 0x5a // an arbitrary byte to round-trip through the 2nd megapage

	var ins []uint32
	emit := func(words ...uint32) { ins = append(ins, words...) }

	// Enable the guest's own Sv39 paging: vsatp = (Sv39<<60) | (vsRootGPA>>12).
	// Sv39<<60 and the (~20-bit) PPN are disjoint, so add composes them.
	emit(addi(regT0, regZero, SatpSv39))
	emit(slli(regT0, regT0, 60))
	emit(loadImm32(regT1, uint32(vsRootGPA>>12))...)
	emit(add(regT0, regT0, regT1))
	emit(csrw(csrSatp, regT0)) // satp aliases to vsatp under V=1
	emit(opSfenceVMA)          // flush the guest's VS-stage TLB

	// Prove translation is live across the second megapage: write then read a
	// sentinel at guest VA GuestBase+MegapageSize (= 0x401 << 21).
	emit(addi(regT0, regZero, sentinel))
	emit(addi(regT1, regZero, (GuestBase+MegapageSize)>>21))
	emit(slli(regT1, regT1, 21))
	emit(sd(regT0, regT1, 0))
	emit(ld(regT2, regT1, 0))
	failBranch := len(ins)
	emit(0) // bne t2,t0,shutdown  (patched: mismatch -> halt without printing msg)

	// Success: print msg via the legacy SBI console, then shut down.
	for i := 0; i < len(msg); i++ {
		emit(addi(regA7, regZero, SBIConsolePutchar))
		emit(addi(regA0, regZero, int(msg[i])))
		emit(opEcall)
	}

	shutdownIdx := len(ins)
	emit(addi(regA7, regZero, SBIShutdown))
	emit(opEcall)
	emit(opJ0) // guard against a missed stop

	ins[failBranch] = bne(regT2, regT0, (shutdownIdx-failBranch)*4)
	return assemble(ins)
}

// DBCNGuest builds a guest that proves honk can read a guest-supplied buffer
// from guest memory through the G-stage - the keystone for device backends,
// which all follow guest pointers. It probes the SBI DBCN (debug console)
// extension, writes the token "dbcn" into its own RAM with a store, then calls
// DBCN console_write with that buffer's guest-physical address and length - so
// honk translates the address, reads the bytes the guest wrote, and prints
// them. If DBCN is unsupported it shuts down without printing, so the printed
// token is the end-to-end proof.
func DBCNGuest() []byte {
	// "dbcn" as one little-endian 32-bit value (bit 31 clear, so loadImm32 builds
	// it); stored as a doubleword, its low 4 bytes are the message honk reads.
	const token = 'd' | 'b'<<8 | 'c'<<16 | 'n'<<24
	const msgOff = 0x400 // buffer offset in guest RAM, clear of the (tiny) code
	const msgLen = 4

	var ins []uint32
	emit := func(words ...uint32) { ins = append(ins, words...) }

	// Probe SBI Base for DBCN; if absent, skip straight to shutdown.
	emit(addi(regA7, regZero, SBIExtBase))
	emit(addi(regA6, regZero, SBIBaseProbeExtension))
	emit(loadImm32(regA0, SBIExtDBCN)...) // a0 = the EID being probed
	emit(opEcall)
	probeBranch := len(ins)
	emit(0) // beq a1, x0, shutdown  (patched)

	// Build the buffer address a1 = GuestBase|msgOff (a2 = 0: the high half) and
	// store the token there, in the guest's own RAM.
	emit(addi(regA1, regZero, 1))
	emit(slli(regA1, regA1, 31)) // a1 = GuestBase (0x8000_0000)
	emit(addi(regT0, regZero, msgOff))
	emit(add(regA1, regA1, regT0)) // a1 = msgGPA
	emit(loadImm32(regT0, token)...)
	emit(sd(regT0, regA1, 0)) // guest RAM[msgGPA..] = "dbcn\0\0\0\0"

	// DBCN console_write(a0=len, a1=base_lo, a2=base_hi): honk reads the buffer.
	emit(addi(regA0, regZero, msgLen))
	emit(addi(regA2, regZero, 0))
	emit(loadImm32(regA7, SBIExtDBCN)...)
	emit(addi(regA6, regZero, SBIDBCNWrite))
	emit(opEcall)

	shutdownIdx := len(ins)
	emit(addi(regA7, regZero, SBIShutdown))
	emit(opEcall)
	emit(opJ0) // guard against a missed stop

	ins[probeBranch] = beq(regA1, regZero, (shutdownIdx-probeBranch)*4)
	return assemble(ins)
}

// The M13-groundwork demo MMIO device honk emulates: a guest-physical register
// window below guest RAM, so a guest access to it misses the G-stage map and
// faults to honk's MMIO trap-and-emulate path. Shared by the guest payload and
// the board emulator so they agree on the addresses (one owner).
const (
	MMIOBase     = 0x1000_0000  // device register window base (below GuestBase)
	MMIORegMagic = MMIOBase + 0 // load: honk returns MMIOMagic
	MMIORegOut   = MMIOBase + 8 // store: honk prints the low byte
	MMIOMagic    = 'M'          // the byte a load of MMIORegMagic returns
)

// MMIOGuest builds a guest that proves MMIO trap-and-emulate: honk catching a
// guest's load/store to an (unmapped) device-register address, decoding the
// faulting instruction, emulating the register, and resuming. The guest loads
// honk's magic byte from MMIORegMagic (proving load emulation) and prints it,
// then stores 'm','i','o' to MMIORegOut (proving store emulation) - printing
// "Mmio". No SBI console is used: the bytes reach honk's console only through
// the emulated device.
func MMIOGuest() []byte {
	const (
		regBase = regT1 // holds MMIOBase
		regCh   = regT0 // the byte to load/store
	)
	var ins []uint32
	emit := func(words ...uint32) { ins = append(ins, words...) }

	emit(loadImm32(regBase, MMIOBase)...)            // MMIOBase has bit 31 clear
	emit(lbu(regCh, regBase, MMIORegMagic-MMIOBase)) // load honk's magic ('M')
	emit(sb(regCh, regBase, MMIORegOut-MMIOBase))    // print it (store emulation)
	for _, c := range []byte{'m', 'i', 'o'} {
		emit(addi(regCh, regZero, int(c)))
		emit(sb(regCh, regBase, MMIORegOut-MMIOBase))
	}

	emit(addi(regA7, regZero, SBIShutdown))
	emit(opEcall)
	emit(opJ0) // guard against a missed stop
	return assemble(ins)
}

// assemble packs a RV64 instruction stream into little-endian machine code to
// copy into guest memory.
func assemble(ins []uint32) []byte {
	out := make([]byte, 4*len(ins))
	for i, in := range ins {
		binary.LittleEndian.PutUint32(out[4*i:], in)
	}
	return out
}
