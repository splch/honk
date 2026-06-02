//go:build tamago && riscv64

// Package uart drives an NS16550A UART over MMIO (RV64.md §7.3). The register
// offsets below are the standard 8250/16550 layout; QEMU's virt UART is at
// 0x10000000.
package uart

import "github.com/splch/honk/internal/mmio"

// Register offsets (DLAB=0) and status bits.
const (
	rbr = 0 // receive buffer (read)
	thr = 0 // transmit holding (write)
	ier = 1 // interrupt enable
	fcr = 2 // FIFO control (write)
	lcr = 3 // line control
	lsr = 5 // line status

	lsrDR   = 0x01 // data ready
	lsrTHRE = 0x20 // transmit holding empty
)

// UART is a memory-mapped NS16550A.
type UART struct{ base uintptr }

// New returns a driver for the UART at the given MMIO base address.
func New(base uintptr) *UART { return &UART{base: base} }

// Init configures 8N1, enables and clears the FIFOs, and enables the receive
// interrupt. Clearing DLAB (last) is mandatory or RBR/THR stay aliased to the
// divisor and the UART appears dead (RV64.md §7.3, Appendix F #23).
func (u *UART) Init() {
	mmio.W8(u.base+ier, 0x00) // interrupts off during setup
	mmio.W8(u.base+lcr, 0x80) // DLAB on
	mmio.W8(u.base+0, 0x01)   // divisor low (QEMU ignores the rate)
	mmio.W8(u.base+1, 0x00)   // divisor high
	mmio.W8(u.base+lcr, 0x03) // 8N1, DLAB off
	mmio.W8(u.base+fcr, 0x07) // enable + clear RX/TX FIFOs
	mmio.W8(u.base+ier, 0x01) // enable received-data-available interrupt
}

// Tx writes one byte, waiting for the transmit holding register to drain.
func (u *UART) Tx(c byte) {
	for mmio.R8(u.base+lsr)&lsrTHRE == 0 {
	}
	mmio.W8(u.base+thr, c)
}

// Write writes p to the UART, satisfying io.Writer so the UART console and an
// SSH session can share one command runner.
func (u *UART) Write(p []byte) (int, error) {
	for _, c := range p {
		u.Tx(c)
	}
	return len(p), nil
}

// Rx returns the next received byte, or ok=false if the RX FIFO is empty.
func (u *UART) Rx() (byte, bool) {
	if mmio.R8(u.base+lsr)&lsrDR == 0 {
		return 0, false
	}
	return mmio.R8(u.base + rbr), true
}
