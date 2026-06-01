// Package sbi is a console.Device backed by the RISC-V SBI legacy console calls
// (console_putchar / console_getchar), which OpenSBI implements for us via an
// ecall to M-mode firmware. It needs no device address and no MMIO knowledge, so
// it works on any SBI platform: it is the portable fallback console for a board
// whose UART has no driver yet, and the second implementation of console.Device
// (alongside the NS16550A UART) that proves the console seam is real.
package sbi

// Device is the SBI console. The zero value is ready to use.
type Device struct{}

// New returns an SBI-backed console device.
func New() *Device { return &Device{} }

// Putc writes one byte to the SBI console.
func (*Device) Putc(c byte) { putchar(c) }

// Getc blocks until the SBI console returns a byte. The legacy getchar call
// returns a negative value when no input is ready, so it polls.
func (*Device) Getc() byte {
	for {
		if c := getchar(); c >= 0 {
			return byte(c)
		}
	}
}
