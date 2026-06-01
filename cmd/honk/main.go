// Command honk is the Honk OS kernel.
//
// Honk OS is a small educational operating system written in pure Go. The Go
// runtime — goroutines, channels, and the garbage collector — runs directly in
// RISC-V supervisor mode under OpenSBI as the kernel itself. main wires up the
// console over a UART and hands control to the interactive shell.
package main

import (
	"github.com/splch/honk/kernel/arch/riscv64"
	"github.com/splch/honk/kernel/console"
	"github.com/splch/honk/kernel/driver/uart"
	"github.com/splch/honk/kernel/shell"
)

func main() {
	con := console.New(uart.New(riscv64.UART0))
	shell.Run(con)
}
