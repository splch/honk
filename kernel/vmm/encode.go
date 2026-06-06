package vmm

// This file is honk's tiny RV64 assembler: just the instruction encoders the
// hand-rolled guest payloads need, each a single function returning the 32-bit
// little-endian word. The guests are generated (not hand-assembled blobs) so
// they are host-testable - a test decodes the stream and asserts it is exactly
// the program intended (the "works on QEMU / silent fault" class of bug is
// caught here, not in the emulator). Only the opcodes the guests use are here;
// this is deliberately not a general assembler.
//
// Encodings follow the RISC-V Unprivileged ISA (RV64I + Zicsr) instruction
// formats; the bit layout for each format is noted at its encoder.

// Integer register numbers (x0..x31) the guests reference by ABI name.
const (
	regZero = 0  // x0:  hardwired zero
	regT0   = 5  // x5:  temporary
	regT1   = 6  // x6:  temporary
	regT2   = 7  // x7:  temporary (the paging guest's load destination)
	regS0   = 8  // x8:  saved (survives the guest's own trap, used as a counter)
	regA0   = 10 // x10: SBI arg0 / return value
	regA1   = 11 // x11: SBI arg1 / return value
	regA2   = 12 // x12: SBI arg2 (e.g. DBCN console_write base_addr_hi)
	regA6   = 16 // x16: SBI function id (FID)
	regA7   = 17 // x17: SBI extension id (EID)
)

// Whole-instruction encodings with no operands.
const (
	opEcall     = 0x00000073 // ecall:   environment call (from VS-mode -> HS, scause 10)
	opWFI       = 0x10500073 // wfi:     wait for interrupt
	opSret      = 0x10200073 // sret:    return from an S/VS-mode trap
	opSfenceVMA = 0x12000073 // sfence.vma x0,x0: flush the (VS-stage, under V=1) TLB
	opJ0        = 0x0000006f // jal x0,0: an infinite self-loop (".")
)

// CSR numbers the guests read/write. Under V=1 the standard supervisor CSR
// numbers alias to their VS counterparts (stvec->vstvec, etc.), so the guest
// uses the ordinary S addresses and the hardware redirects them.
const (
	csrSstatus = 0x100
	csrSie     = 0x104
	csrStvec   = 0x105
	csrSatp    = 0x180 // satp; under V=1 this aliases to vsatp (the guest's paging)
	csrTime    = 0xc01
)

// addi encodes `addi rd, rs1, imm` (I-type: imm[31:20] rs1 funct3=0 rd
// opcode=0x13). Also serves `li rd, imm` (rs1=x0) for imm in [-2048, 2047].
func addi(rd, rs1, imm int) uint32 {
	return uint32(imm&0xfff)<<20 | uint32(rs1)<<15 | uint32(rd)<<7 | 0x13
}

// add encodes `add rd, rs1, rs2` (R-type: funct7=0 rs2 rs1 funct3=0 rd
// opcode=0x33).
func add(rd, rs1, rs2 int) uint32 {
	return uint32(rs2)<<20 | uint32(rs1)<<15 | uint32(rd)<<7 | 0x33
}

// lui encodes `lui rd, imm20` (U-type: imm[31:12] rd opcode=0x37). The result's
// bit 31 is sign-extended to 64 bits, so lui only builds values whose bit 31 is
// clear; use loadAddr for high addresses.
func lui(rd int, imm20 uint32) uint32 {
	return (imm20&0xfffff)<<12 | uint32(rd)<<7 | 0x37
}

// slli encodes `slli rd, rs1, shamt` (RV64 I-type shift: funct7=0 shamt[5:0]
// rs1 funct3=1 rd opcode=0x13).
func slli(rd, rs1, shamt int) uint32 {
	return uint32(shamt&0x3f)<<20 | uint32(rs1)<<15 | 1<<12 | uint32(rd)<<7 | 0x13
}

// csrrw / csrrs encode the atomic CSR read-write / read-set (Zicsr, opcode
// 0x73, csr in bits 31:20). The named helpers below are the only forms used.
func csrrw(rd, rs1, csr int) uint32 {
	return uint32(csr&0xfff)<<20 | uint32(rs1)<<15 | 1<<12 | uint32(rd)<<7 | 0x73
}
func csrrs(rd, rs1, csr int) uint32 {
	return uint32(csr&0xfff)<<20 | uint32(rs1)<<15 | 2<<12 | uint32(rd)<<7 | 0x73
}

// csrw csr, rs1  (= csrrw x0, rs1, csr): write a CSR, discarding its old value.
func csrw(csr, rs1 int) uint32 { return csrrw(regZero, rs1, csr) }

// csrs csr, rs1  (= csrrs x0, rs1, csr): set the CSR bits in rs1.
func csrs(csr, rs1 int) uint32 { return csrrs(regZero, rs1, csr) }

// rdtime rd  (= csrrs rd, x0, time): read the time CSR into rd.
func rdtime(rd int) uint32 { return csrrs(rd, regZero, csrTime) }

// jal encodes `jal rd, off` (J-type, opcode=0x6F). off is a byte offset
// relative to the jal, must be even, and is scrambled into imm[20|10:1|11|19:12].
func jal(rd, off int) uint32 {
	u := uint32(off)
	imm := (u>>20&1)<<31 | (u>>1&0x3ff)<<21 | (u>>11&1)<<20 | (u>>12&0xff)<<12
	return imm | uint32(rd)<<7 | 0x6f
}

// beq encodes `beq rs1, rs2, off` (B-type, opcode=0x63, funct3=0). off is a
// byte offset relative to the beq, even, scrambled into imm[12|10:5|4:1|11].
func beq(rs1, rs2, off int) uint32 {
	u := uint32(off)
	return (u>>12&1)<<31 | (u>>5&0x3f)<<25 | uint32(rs2)<<20 | uint32(rs1)<<15 |
		(u>>1&0xf)<<8 | (u>>11&1)<<7 | 0x63
}

// bne encodes `bne rs1, rs2, off` (B-type, opcode=0x63, funct3=1): the same
// immediate scramble as beq, branching when rs1 != rs2.
func bne(rs1, rs2, off int) uint32 {
	u := uint32(off)
	return (u>>12&1)<<31 | (u>>5&0x3f)<<25 | uint32(rs2)<<20 | uint32(rs1)<<15 |
		(u>>1&0xf)<<8 | (u>>11&1)<<7 | 1<<12 | 0x63
}

// ld encodes `ld rd, off(rs1)` (I-type load doubleword: funct3=3, opcode=0x03).
func ld(rd, rs1, off int) uint32 {
	return uint32(off&0xfff)<<20 | uint32(rs1)<<15 | 3<<12 | uint32(rd)<<7 | 0x03
}

// sd encodes `sd rs2, off(rs1)` (S-type store doubleword: funct3=3, opcode=0x23);
// the immediate is split imm[11:5] (bits 31:25) and imm[4:0] (bits 11:7).
func sd(rs2, rs1, off int) uint32 {
	u := uint32(off)
	return (u>>5&0x7f)<<25 | uint32(rs2)<<20 | uint32(rs1)<<15 | 3<<12 | (u&0x1f)<<7 | 0x23
}

// loadImm32 builds rd = v for a 32-bit value whose bit 31 is clear, as
// `lui rd, hi; addi rd, rd, lo`, choosing hi so the sign-extended addi corrects
// to exactly v. (Values with bit 31 set would be sign-extended by lui; use
// loadAddr for those.)
func loadImm32(rd int, v uint32) []uint32 {
	hi := (v + 0x800) >> 12        // round so the signed addi below corrects
	lo := int32(v) - int32(hi<<12) // low part, in [-2048, 2047]
	return []uint32{lui(rd, hi), addi(rd, rd, int(lo))}
}

// loadAddr builds rd = (1<<31) + off (a guest RAM address: honk maps the guest
// at 0x8000_0000) as `li rd, 1; slli rd, rd, 31; addi rd, rd, off`. This avoids
// lui's bit-31 sign extension, which would make the address negative. off must
// fit a 12-bit signed immediate (the guest payload is far smaller than 2 KiB
// past its base, so its labels do).
func loadAddr(rd, off int) []uint32 {
	return []uint32{addi(rd, regZero, 1), slli(rd, rd, 31), addi(rd, rd, off)}
}
