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
// physical address", so its root page table is four pages (16 KiB).
const (
	HgatpBare   = 0
	HgatpSv39x4 = 8
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

// LeafRWX is the permission set honk maps guest RAM with: read, write, execute,
// user, accessed, dirty. (G is left clear; it is reserved in G-stage PTEs.)
const LeafRWX = pteV | pteR | pteW | pteX | pteU | pteA | pteD

// MegapageSize is the size of a level-1 Sv39 leaf (a 2 MiB "megapage"). honk
// maps a guest with a single megapage, so its RAM must be 2 MiB-aligned and the
// guest fits within 2 MiB - ample for the M11 payload.
const MegapageSize = 2 << 20

// RootSize and L1Size are the byte sizes (and required alignments) of the two
// G-stage tables. The Sv39x4 root is 16 KiB (2048 entries) and must be 16
// KiB-aligned; a normal next-level table is 4 KiB (512 entries).
const (
	RootSize = 16384
	L1Size   = 4096
)

// pte packs a physical address and flag bits into an Sv39 page-table entry:
// PPN occupies bits 53:10, so pa>>12 is shifted left by 10.
func pte(pa, flags uint64) uint64 { return (pa>>12)<<10 | flags }

// WriteGStage fills a 16-KiB Sv39x4 root and a 4-KiB level-1 table so that a
// single 2 MiB megapage maps guest-physical guestBase to supervisor-physical
// guestPA with LeafRWX permissions. rootPA and l1PA are the supervisor-physical
// addresses of the two tables (honk is identity-mapped, so they equal the Go
// slices' addresses). guestBase must be 2 MiB-aligned.
//
// The map is exactly what hgatp = (Sv39x4<<60 | rootPA>>12) then needs; the one
// megapage covers [guestBase, guestBase+2 MiB), enough for the whole payload.
func WriteGStage(root, l1 []byte, rootPA, l1PA, guestPA, guestBase uint64) {
	vpn2 := (guestBase >> 30) & 0x7ff // Sv39x4 root index is 11 bits
	vpn1 := (guestBase >> 21) & 0x1ff // level-1 index is 9 bits
	// level-1 entry: a 2 MiB leaf (R/W/X set => leaf) backing the guest's RAM.
	binary.LittleEndian.PutUint64(l1[vpn1*8:], pte(guestPA, LeafRWX))
	// root entry: a pointer to the level-1 table (no R/W/X => non-leaf, V only).
	binary.LittleEndian.PutUint64(root[vpn2*8:], pte(l1PA, pteV))
}

// Hgatp returns the hgatp value selecting Sv39x4 G-stage translation rooted at
// the table whose supervisor-physical address is rootPA. VMID is left 0 (honk
// runs one guest at a time).
func Hgatp(rootPA uint64) uint64 { return HgatpSv39x4<<60 | rootPA>>12 }

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

// assemble packs a RV64 instruction stream into little-endian machine code to
// copy into guest memory.
func assemble(ins []uint32) []byte {
	out := make([]byte, 4*len(ins))
	for i, in := range ins {
		binary.LittleEndian.PutUint32(out[4*i:], in)
	}
	return out
}
