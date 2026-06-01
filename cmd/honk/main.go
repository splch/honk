// Command honk is the Honk OS kernel.
//
// Honk OS is a small educational operating system written in pure Go. The Go
// runtime — goroutines, channels, and the garbage collector — runs directly in
// RISC-V supervisor mode under OpenSBI as the kernel itself. main wires the
// console to the board's console device and hands control to the interactive
// shell.
package main

import (
	"github.com/splch/honk/kernel/board"
	"github.com/splch/honk/kernel/console"
	"github.com/splch/honk/kernel/shell"
)

func main() {
	con := console.New(board.Console())
	shell.Run(con)
}
