// honk - QEMU virt board: console.
//
// Output is the SBI console (printk, below). Input is the NS16550A UART RX
// interrupt: the trap handler drains bytes into a ring (trap.go), and a reader
// goroutine moves them onto the Input channel - the "IRQ routes to a channel"
// path of HONK.md §1.

//go:build tamago && riscv64

package virt

import (
	"time"
	_ "unsafe"
)

// printk is the runtime's character-output hook. It runs before the World is
// up, so it must not allocate or split the stack: it routes straight to the
// SBI console.
//
//go:linkname printk runtime/goos.Printk
//go:nosplit
func printk(c byte) {
	sbiPutchar(c)
}

var consoleIn = make(chan byte, 256)

// Console returns the channel of bytes received from the UART.
func Console() <-chan byte { return consoleIn }

// InitConsole routes UART receive interrupts to the boot hart (the only hart
// with S-interrupts enabled) and starts the reader goroutine. Safe to call
// from any hart: it only programs the PLIC/UART (which route to the boot hart's
// context) and the boot hart's stvec/SIE were set in cpuinit.
func InitConsole() {
	ctx := plicContext(bootHartID)
	plicSetThreshold(ctx, 0) // unmask all priorities
	plicSetPriority(uartIRQ, 1)

	// Enable the UART RX interrupt and drain anything already received during
	// boot while the PLIC source is still masked - so no trap fires and races
	// this drain. A byte arriving in the gap before plicEnableSource stays
	// asserted (level-triggered) and is delivered once routing is enabled.
	uartInit()
	for uartRxReady() {
		consoleRing.Push(uartReadByte())
	}
	plicEnableSource(ctx, uartIRQ)

	go consoleReader()
}

// consoleReader drains the interrupt-filled ring onto the Input channel. It
// polls with a short sleep when idle; the finite timer keeps the runtime from
// the "idle forever" shutdown path while honk waits for input.
func consoleReader() {
	for {
		if b, ok := consoleRing.Pop(); ok {
			consoleIn <- b
			continue
		}
		time.Sleep(2 * time.Millisecond)
	}
}
