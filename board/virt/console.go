// honk - QEMU virt board: early console.

//go:build tamago && riscv64

package virt

import _ "unsafe"

// printk is the runtime's character-output hook. It runs before the World is
// up, so it must not allocate or split the stack: it routes straight to the
// SBI console. A real NS16550A UART driver replaces this in M1.
//
//go:linkname printk runtime/goos.Printk
//go:nosplit
func printk(c byte) {
	sbiPutchar(c)
}
