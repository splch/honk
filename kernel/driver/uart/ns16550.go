// Package uart is a minimal polled NS16550A UART driver.
package uart

import "github.com/splch/honk/kernel/arch/riscv64"

// NS16550A register offsets and line-status bits.
const (
	rbr = 0 // receiver buffer (read)
	thr = 0 // transmit holding (write)
	lsr = 5 // line status register

	lsrDR   = 0x01 // receive data ready
	lsrTHRE = 0x20 // transmit holding register empty
)

// Device is a polled NS16550A UART at a fixed MMIO base address. It satisfies
// console.Device.
type Device struct{ base uintptr }

// New returns a driver for the UART at base.
func New(base uintptr) *Device { return &Device{base: base} }

// Putc transmits one byte, blocking until the UART is ready to accept it.
func (d *Device) Putc(c byte) {
	for riscv64.R8(d.base+lsr)&lsrTHRE == 0 {
	}
	riscv64.W8(d.base+thr, c)
}

// Getc blocks until a byte is available on the UART and returns it.
func (d *Device) Getc() byte {
	for riscv64.R8(d.base+lsr)&lsrDR == 0 {
	}
	return riscv64.R8(d.base + rbr)
}
