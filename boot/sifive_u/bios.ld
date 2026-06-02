/* Linker script for the Phase 0 sifive_u trampoline BIOS (see bios.s).
 * The trampoline holds only .text; it runs at the sifive_u reset/load address
 * and immediately jumps into the TamaGo runtime. */
OUTPUT_ARCH( "riscv" )
OUTPUT_FORMAT( "elf64-littleriscv" )
ENTRY( _start )

SECTIONS
{
	. = 0x80000000;
	.text : { *(.text) }
	. += 0x8000;
	_end = .;
}
