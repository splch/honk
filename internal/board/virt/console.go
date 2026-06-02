//go:build tamago && riscv64

package virt

import (
	_ "unsafe"

	"github.com/splch/honk/internal/sbi"
)

// printk emits one byte to the console. During early boot (before a native
// NS16550A UART driver, RV64.md Part 7.3) the SBI console is the only sink.
//
//go:linkname printk runtime/goos.Printk
func printk(c byte) { sbi.ConsolePutchar(c) }
