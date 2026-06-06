// honk - QEMU virt board: trap handling (interrupt service + fatal faults).

//go:build tamago && riscv64

package virt

import "sync/atomic"

// trapStacks are per-hart interrupt stacks. trapEntry switches to its hart's
// stack via sscratch (set in cpuinit/secondaryEntry) so the handler never runs
// on - and so can never overflow or corrupt - the interrupted goroutine's
// stack. honk has no MMU guard pages, so this is the only thing standing
// between a deep trap and silent memory corruption; every interrupt source in
// the roadmap (NVMe, virtio-gpu/input/net, the VMM) lands here.
//
// trapStackSize is 1<<14; the SLLI shift in boot_riscv64.s MUST match.
const trapStackSize = 1 << 14

var trapStacks [maxHarts][trapStackSize]byte

// implemented in trap_riscv64.s
func trapEntryPC() uintptr
func triggerFault()
func readScause() uint64
func readSepc() uint64
func readStval() uint64

// MMIO accessors (trap_riscv64.s).
func mmioRead8(addr uintptr) uint8
func mmioWrite8(addr uintptr, v uint8)
func mmioRead32(addr uintptr) uint32
func mmioWrite32(addr uintptr, v uint32)

// handleIRQ services an S-mode interrupt synchronously, from trap context. It
// claims the PLIC, drains the UART into the input ring, and completes - leaving
// nothing pending, so the sret in trapEntry does not storm.
//
// CONTRACT: handleIRQ and everything it transitively calls MUST be //go:nosplit
// and free of floating-point operations. trapEntry saves only integer
// caller-saved registers and runs on the interrupted goroutine's stack; it does
// NOT save FP state or grow the stack. Introducing a stack split or an FP op on
// this path would silently corrupt the interrupted goroutine.
//
//go:nosplit
func handleIRQ() {
	ctx := plicContext(bootHartID)
	irq := plicClaim(ctx)
	if irq == 0 {
		return // spurious
	}
	if irq == uartIRQ {
		for uartRxReady() {
			ringPush(uartReadByte())
		}
	}
	plicComplete(ctx, irq)
}

// handleFault is the fatal path for an S-mode exception (a kernel bug, or the
// `fault` shell command's EBREAK). It prints the trap CSRs with the raw SBI
// console - no allocation, safe from trap context - and powers off.
//
//go:nosplit
func handleFault() {
	putStr("\n\nhonk: FATAL supervisor trap\n  scause=0x")
	putHex(readScause())
	putStr("  sepc=0x")
	putHex(readSepc())
	putStr("  stval=0x")
	putHex(readStval())
	putStr("\n")
	Shutdown()
}

//go:nosplit
func putStr(s string) {
	for i := 0; i < len(s); i++ {
		sbiPutchar(s[i])
	}
}

//go:nosplit
func putHex(v uint64) {
	const digits = "0123456789abcdef"
	var buf [16]byte
	for i := 0; i < 16; i++ {
		buf[15-i] = digits[v&0xf]
		v >>= 4
	}
	for i := 0; i < 16; i++ {
		sbiPutchar(buf[i])
	}
}

// Input ring: single-producer (the boot hart's trap handler) / single-consumer
// (consoleReader). uint32 indices wrap; the difference is the live count.
const ringSize = 1024 // power of two

var (
	ringBuf  [ringSize]byte
	ringHead uint32 // next index to read
	ringTail uint32 // next index to write
)

//go:nosplit
func ringPush(b byte) {
	t := atomic.LoadUint32(&ringTail)
	if t-atomic.LoadUint32(&ringHead) >= ringSize {
		return // full: drop
	}
	ringBuf[t&(ringSize-1)] = b
	atomic.StoreUint32(&ringTail, t+1)
}

func ringPop() (byte, bool) {
	h := atomic.LoadUint32(&ringHead)
	if h == atomic.LoadUint32(&ringTail) {
		return 0, false
	}
	b := ringBuf[h&(ringSize-1)]
	atomic.StoreUint32(&ringHead, h+1)
	return b, true
}
