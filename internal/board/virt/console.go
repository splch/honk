//go:build tamago && riscv64

package virt

import _ "unsafe"

// printk emits one byte to the console. During early boot (before hwinit1) the
// SBI console is the only output sink; a native NS16550A UART driver is a later
// milestone (RV64.md Part 7.3). Implemented via the SBI ecall in sbi_riscv64.s.
//
//go:linkname printk runtime/goos.Printk
func printk(c byte) { sbiPutchar(c) }

func sbiPutchar(c byte) // sbi_riscv64.s
func sbiShutdown()      // sbi_riscv64.s
