// mkboot writes a minimal RISC-V boot trampoline.
//
// QEMU's RISC-V fw_dynamic boot (OpenSBI via `-bios default`) jumps to the
// load *base* of the `-kernel` image, not its ELF entry point (see QEMU
// hw/riscv/boot.c, commit "Use load address rather than entry point for
// fw_dynamic"). A Go-linked ELF puts its header page at the load base and its
// _rt0 entry mid-.text, so OpenSBI cannot enter it directly.
//
// mkboot emits a tiny position-fixed flat binary, loaded by `-kernel` at the
// OpenSBI jump target (0x80200000), that simply jumps to the honk ELF's real
// entry point (honk itself is loaded separately via `-device loader`, which
// honors the ELF's program-header addresses). a0 (hartid) and a1 (DTB) from
// the SBI hand-off are preserved - the trampoline only clobbers t0.
//
// Usage: mkboot <honk.elf> <boot.bin>
package main

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mkboot <kernel.elf> <boot.bin>")
		os.Exit(2)
	}

	f, err := elf.Open(os.Args[1])
	if err != nil {
		fatal(err)
	}
	defer f.Close()
	entry := f.Entry

	// Trampoline executed at 0x80200000:
	//   auipc t0, 0      ; t0 = pc (this instruction's address)
	//   ld    t0, 16(t0) ; t0 = entry (the .dword at pc+16)
	//   jr    t0         ; jump to honk's _rt0, a0/a1 intact
	//   nop              ; pad so the .dword is 8-byte aligned
	//   .dword entry
	buf := make([]byte, 24)
	binary.LittleEndian.PutUint32(buf[0:], 0x00000297)  // auipc t0, 0
	binary.LittleEndian.PutUint32(buf[4:], 0x0102B283)  // ld    t0, 16(t0)
	binary.LittleEndian.PutUint32(buf[8:], 0x00028067)  // jr    t0
	binary.LittleEndian.PutUint32(buf[12:], 0x00000013) // nop
	binary.LittleEndian.PutUint64(buf[16:], entry)      // entry point

	if err := os.WriteFile(os.Args[2], buf, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("mkboot: %s -> %s (entry %#x)\n", os.Args[1], os.Args[2], entry)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "mkboot:", err)
	os.Exit(1)
}
