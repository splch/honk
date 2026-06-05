// honk - a tiny interactive shell over the UART console (M1).

//go:build tamago && riscv64

package main

import (
	"fmt"
	"runtime"
	"strings"

	"honk/board/virt"
)

// runShell reads bytes from the console channel, line-edits with echo, and
// dispatches commands. It returns only if the input channel closes.
func runShell(in <-chan byte) {
	fmt.Print("\nhonk: shell ready (type 'help')\nhonk> ")

	var line []byte
	for b := range in {
		switch b {
		case '\r', '\n':
			fmt.Print("\r\n")
			exec(string(line))
			line = line[:0]
			fmt.Print("honk> ")
		case 0x7f, 0x08: // DEL / backspace
			if len(line) > 0 {
				line = line[:len(line)-1]
				fmt.Print("\b \b")
			}
		default:
			if b >= 0x20 && b < 0x7f { // printable
				line = append(line, b)
				fmt.Printf("%c", b) // echo (terminal is raw under -nographic)
			}
		}
	}
}

func exec(line string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return
	}
	switch fields[0] {
	case "help":
		fmt.Println("commands: help  harts  uptime  mem  echo <text>  fault  exit")
	case "harts":
		fmt.Printf("harts: %d online  GOMAXPROCS=%d  this=hart %d\n",
			virt.NumHarts(), runtime.GOMAXPROCS(-1), virt.CurrentHart())
	case "uptime":
		fmt.Printf("uptime: %s\n", virt.Uptime())
	case "mem":
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		fmt.Printf("mem: heap=%dKiB sys=%dKiB goroutines=%d numgc=%d\n",
			m.HeapAlloc/1024, m.Sys/1024, runtime.NumGoroutine(), m.NumGC)
	case "echo":
		fmt.Println(strings.Join(fields[1:], " "))
	case "fault":
		fmt.Println("fault: raising a supervisor exception...")
		virt.Fault() // prints trap CSRs and halts; does not return
	case "exit", "halt", "quit":
		fmt.Println("honk: shutting down")
		virt.Shutdown() // does not return
	default:
		fmt.Printf("unknown command: %q (try 'help')\n", fields[0])
	}
}
