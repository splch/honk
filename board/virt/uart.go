// honk - QEMU virt board: NS16550A UART (input path).
//
// OpenSBI already configured the UART for its console (8N1, divisor, FIFO) and
// honk's output still goes through the SBI console, so honk only needs the
// receive path: enable the RX interrupt and drain bytes on IRQ.

//go:build tamago && riscv64

package virt

const (
	uartBase = 0x10000000
	uartIRQ  = 10 // QEMU virt UART0 PLIC source

	uartRBR = uartBase + 0 // receive buffer (read)
	uartIER = uartBase + 1 // interrupt enable
	uartLSR = uartBase + 5 // line status

	lsrDataReady = 0x01 // LSR: byte available
	ierRxReady   = 0x01 // IER: enable received-data-available interrupt
)

// uartInit enables the UART receive interrupt. It deliberately leaves the FIFO
// control and line settings as OpenSBI configured them: toggling FCR would
// reset the FIFO and discard any byte already received before honk started.
func uartInit() {
	mmioWrite8(uartIER, ierRxReady)
}

//go:nosplit
func uartRxReady() bool { return mmioRead8(uartLSR)&lsrDataReady != 0 }

//go:nosplit
func uartReadByte() byte { return mmioRead8(uartRBR) }
