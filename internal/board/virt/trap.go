//go:build tamago && riscv64

package virt

import "github.com/splch/honk/internal/sbi"

// implemented in trap_riscv64.s
func setTrapVector()
func enableTimerIRQ()
func enableExtIRQ()
func wfi()
func readSCause() uint64
func readSEPC() uint64
func readSTVAL() uint64

const sCauseInterrupt = 1 << 63 // scause top bit: 1 = interrupt, 0 = exception

// trapVector is honk's S-mode trap handler, installed in stvec by setTrapVector.
// It is the raw trap entry (no register save, no sret). Because honk keeps
// sstatus.SIE = 0, maskable interrupts never trap (they only wake wfi; see
// idle), so this is reached only for synchronous EXCEPTIONS — faults with
// nothing to resume to. It reports the cause and powers off. (RV64.md Part 3;
// cause codes in Appendix C.)
func trapVector() {
	scause := readSCause()
	puts("\nhonk/virt: supervisor ")
	if scause&sCauseInterrupt != 0 {
		puts("interrupt")
	} else {
		puts("exception")
	}
	puts(" scause=")
	printHex(scause)
	puts(" sepc=")
	printHex(readSEPC())
	puts(" stval=")
	printHex(readSTVAL())
	puts("\nhonk/virt: halting.\n")
	sbi.Shutdown()
}

// puts and printHex write to the console one byte at a time without allocating,
// so they are safe from the trap handler. They go through printk, which targets
// the SBI console early and the UART once it is up.
func puts(s string) {
	for i := 0; i < len(s); i++ {
		printk(s[i])
	}
}

func printHex(v uint64) {
	const digits = "0123456789abcdef"
	printk('0')
	printk('x')
	for shift := 60; shift >= 0; shift -= 4 {
		printk(digits[(v>>uint(shift))&0xf])
	}
}
