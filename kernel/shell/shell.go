// Package shell implements Honk OS's interactive read-eval-print loop. It is
// ordinary Go running on the live runtime: strings, slices, maps, the GC, and
// the reflection-free parts of the standard library all work.
package shell

import (
	"runtime"
	"strconv"
	"strings"

	"github.com/splch/honk/kernel/arch/riscv64"
	"github.com/splch/honk/kernel/board"
	"github.com/splch/honk/kernel/console"
)

const banner = `
      __      Honk OS
 \ __( o)<    pure Go on RISC-V, supervisor mode under OpenSBI
  (  > )      goroutines and a garbage collector are the kernel
  ~~~~~~      type 'help' for commands
`

// Run prints the banner and serves the REPL on c. It does not return; the
// 'halt' command powers the machine off.
func Run(c *console.Console) {
	c.WriteString(banner + "\n")
	for {
		c.WriteString("honk> ")
		line := strings.TrimSpace(c.ReadLine())
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		switch f[0] {
		case "help":
			// Keep this list in sync with the cases below.
			c.WriteString("commands: help, honk, echo <text>, uname, mem, gc, stats, devices, clear, halt\n")
		case "honk":
			c.WriteString("HONK!  (a goose approves)\n")
		case "echo":
			c.WriteString(strings.Join(f[1:], " ") + "\n")
		case "uname":
			c.WriteString("Honk OS (pure Go) " + runtime.GOARCH + " S-mode/OpenSBI\n")
		case "mem":
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			c.WriteString("heap " + strconv.FormatUint(m.HeapAlloc, 10) +
				"B, total " + strconv.FormatUint(m.TotalAlloc, 10) +
				"B, numGC " + strconv.Itoa(int(m.NumGC)) + "\n")
		case "gc":
			runtime.GC()
			c.WriteString("gc done\n")
		case "stats":
			c.WriteString("goroutines " + strconv.Itoa(runtime.NumGoroutine()) +
				", " + runtime.GOOS + "/" + runtime.GOARCH +
				", " + runtime.Version() + "\n")
		case "devices":
			base, size := board.Memory()
			c.WriteString("ram @" + strconv.FormatUint(uint64(base), 16) +
				" " + strconv.FormatUint(uint64(size)>>20, 10) + "MiB, harts " +
				strconv.Itoa(board.NumCPUs()) + "\n")
		case "clear":
			c.WriteString("\x1b[2J\x1b[H")
		case "halt", "exit", "poweroff":
			c.WriteString("honk... powering off.\n")
			riscv64.Poweroff()
		default:
			c.WriteString("unknown: " + f[0] + " (try 'help')\n")
		}
	}
}
