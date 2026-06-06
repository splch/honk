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

// RISC-V opcodes the guest payload uses (RV64I). The guest is deliberately
// trivial straight-line code so the milestone proves the H-extension mechanism
// against a payload honk fully controls (HONK.md M11).
const (
	regA0 = 10 // a0 = SBI argument
	regA7 = 17 // a7 = SBI extension id

	opEcall = 0x00000073 // ecall (trap to the SEE; from VS-mode -> HS, cause 10)
	opJ0    = 0x0000006f // jal x0, 0 (an infinite self-loop: "j .")

	// Legacy SBI extension ids the guest invokes and honk's VMM emulates.
	SBIConsolePutchar = 1 // a7=1: console_putchar(a0)
	SBIShutdown       = 8 // a7=8: shutdown - the guest's "done" signal
)

// addi encodes `addi rd, rs1, imm` (the I-type used to load small immediates,
// i.e. `li rd, imm` for imm in [-2048, 2047]). funct3=0, opcode=0x13.
func addi(rd, rs1, imm int) uint32 {
	return uint32(imm&0xfff)<<20 | uint32(rs1)<<15 | uint32(rd)<<7 | 0x13
}

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

	out := make([]byte, 4*len(ins))
	for i, in := range ins {
		binary.LittleEndian.PutUint32(out[4*i:], in)
	}
	return out
}
