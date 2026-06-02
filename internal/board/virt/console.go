//go:build tamago && riscv64

package virt

import (
	"strings"
	"time"
	_ "unsafe"

	"github.com/splch/honk/internal/plic"
	"github.com/splch/honk/internal/ring"
	"github.com/splch/honk/internal/sbi"
	"github.com/splch/honk/internal/uart"
)

// virt device MMIO (RV64.md Appendix A). TODO(phase1+): read these from the DTB
// (compatible "ns16550a", its reg base and interrupts) instead of hardcoding.
const (
	uartBase = 0x10000000
	plicBase = 0x0c000000
	uartIRQ  = 10
)

var (
	uart0 *uart.UART
	plic0 *plic.PLIC
	input *ring.Ring // allocated in initConsole, not at package-init (which
	// runs after hwinit1, when console() already needs it)
	uartReady bool // once true, the console is honk's own UART, not SBI
)

// printk emits one byte to the console: the SBI console during early boot, then
// honk's own NS16550A UART once initConsole has taken it over.
//
//go:linkname printk runtime/goos.Printk
func printk(c byte) {
	if uartReady {
		uart0.Tx(c)
		return
	}
	sbi.ConsolePutchar(c)
}

// initConsole takes ownership of the NS16550A UART for input and output, routes
// its RX interrupt through the PLIC to this hart's S-context, and starts the
// console goroutine. Called from hwinit1 after paging, so the device MMIO is
// mapped (R/W, no-exec).
func initConsole() {
	input = ring.New(256)
	uart0 = uart.New(uartBase)
	uart0.Init()

	plic0 = plic.New(plicBase, 0)
	plic0.EnableSource(uartIRQ)
	enableExtIRQ() // sie.SEIE: UART input now wakes wfi; idle drains it

	uartReady = true // printk now targets the UART
	go console()
}

// drainConsole services pending PLIC interrupts, buffering UART input. It runs
// from idle, so whenever the hart is about to sleep it claims and completes the
// interrupt — clearing it so wfi sleeps until the next byte arrives rather than
// spinning on a still-pending source.
func drainConsole() {
	if !uartReady {
		return
	}
	for {
		irq := plic0.Claim()
		if irq == 0 {
			return
		}
		if irq == uartIRQ {
			for {
				b, ok := uart0.Rx()
				if !ok {
					break
				}
				input.Push(b) // drop on overflow (256-byte buffer)
			}
		}
		plic0.Complete(irq)
	}
}

// console is a tiny line-buffered shell, demonstrating interrupt-driven I/O end
// to end: a keystroke raises a UART IRQ, wakes the hart from wfi, is drained
// into the ring by idle, is echoed here, and on Enter runs a command — which for
// ls/cat reads the virtio-blk disk through archive/tar.
func console() {
	puts("\r\ntype 'help'.\r\nhonk> ")
	var line []byte
	for {
		b, ok := input.Pop()
		if !ok {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		switch b {
		case '\r', '\n':
			puts("\r\n")
			run(strings.TrimSpace(string(line)))
			line = line[:0]
			puts("honk> ")
		case 0x7f, 0x08: // DEL / backspace
			if len(line) > 0 {
				line = line[:len(line)-1]
				puts("\b \b")
			}
		default:
			line = append(line, b)
			uart0.Tx(b) // echo
		}
	}
}

// run executes one shell command line.
func run(cmd string) {
	switch {
	case cmd == "":
	case cmd == "help":
		puts("commands: help, ls, cat <file>\r\n")
	case cmd == "ls":
		if Disk == nil {
			puts("no disk\r\n")
			return
		}
		listDisk()
	case strings.HasPrefix(cmd, "cat "):
		if Disk == nil {
			puts("no disk\r\n")
			return
		}
		data, err := ReadFile(strings.TrimSpace(cmd[len("cat "):]))
		if err != nil {
			puts("no such file\r\n")
			return
		}
		for _, c := range data {
			if c == '\n' {
				uart0.Tx('\r')
			}
			uart0.Tx(c)
		}
	default:
		puts("unknown command: ")
		puts(cmd)
		puts("\r\n")
	}
}
