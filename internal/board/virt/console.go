//go:build tamago && riscv64

package virt

import (
	crand "crypto/rand"
	"fmt"
	"io"
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
			runCmd(uart0, strings.TrimSpace(string(line)))
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

// runCmd executes one shell command line, writing all output to w. The same
// runner backs both the local UART console and remote SSH sessions, so output
// is destination-agnostic (DESIGN.md §15.1).
func runCmd(w io.Writer, cmd string) {
	switch {
	case cmd == "":
	case cmd == "help":
		io.WriteString(w, "commands: help, ls, cat <file>, write <file> <text>, net, date, ntp, fetch <url>, rand\r\n")
	case cmd == "net":
		netCmd(w)
	case cmd == "date":
		fmt.Fprintf(w, "%s\r\n", time.Now().UTC().Format(time.RFC3339))
	case cmd == "ntp":
		t, err := ntpSync("pool.ntp.org")
		if err != nil {
			fmt.Fprintf(w, "ntp: %v\r\n", err)
			return
		}
		fmt.Fprintf(w, "ntp: clock set to %s\r\n", t.UTC().Format(time.RFC3339))
	case strings.HasPrefix(cmd, "fetch "):
		fetchURL(w, strings.TrimSpace(cmd[len("fetch "):]))
	case cmd == "rand":
		var b [16]byte
		crand.Read(b[:]) // crypto/rand -> getRandomData -> virtio-rng
		fmt.Fprintf(w, "rand: %x\r\n", b)
	case cmd == "ls":
		if FS == nil {
			io.WriteString(w, "no disk\r\n")
			return
		}
		listDisk(w)
	case strings.HasPrefix(cmd, "cat "):
		if FS == nil {
			io.WriteString(w, "no disk\r\n")
			return
		}
		data, err := ReadFile(strings.TrimSpace(cmd[len("cat "):]))
		if err != nil {
			io.WriteString(w, "no such file\r\n")
			return
		}
		writeText(w, data)
	case strings.HasPrefix(cmd, "write "):
		if FS == nil {
			io.WriteString(w, "no disk\r\n")
			return
		}
		name, text, ok := strings.Cut(strings.TrimSpace(cmd[len("write "):]), " ")
		if !ok || name == "" {
			io.WriteString(w, "usage: write <file> <text>\r\n")
			return
		}
		if err := WriteFile(name, []byte(text+"\n")); err != nil {
			fmt.Fprintf(w, "write failed: %v\r\n", err)
			return
		}
		fmt.Fprintf(w, "wrote %d bytes to %s\r\n", len(text)+1, name)
	default:
		fmt.Fprintf(w, "unknown command: %s\r\n", cmd)
	}
}

// writeText writes data to a raw terminal, translating bare \n to \r\n.
func writeText(w io.Writer, data []byte) {
	for _, c := range data {
		if c == '\n' {
			w.Write([]byte{'\r', '\n'})
			continue
		}
		w.Write([]byte{c})
	}
}
