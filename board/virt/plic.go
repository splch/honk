// honk - QEMU virt board: PLIC (Platform-Level Interrupt Controller).
//
// Register map per RV64.md §4.2. A hart owns two PLIC contexts; the S-mode
// context is 2*hartid+1 (the +1 is the #1 PLIC footgun).

//go:build tamago && riscv64

package virt

const (
	plicBase       = 0x0C000000
	plicPriority   = plicBase + 0x000000 // + 4*source
	plicEnable     = plicBase + 0x002000 // + 0x80*context + 4*(source/32)
	plicThreshold  = plicBase + 0x200000 // + 0x1000*context
	plicClaimCompl = plicBase + 0x200004 // + 0x1000*context (read=claim, write=complete)
)

// plicContext returns the S-mode interrupt context for a hart. It is nosplit so
// it is safe on the trap path (handleIRQ) even if not inlined.
//
//go:nosplit
func plicContext(hart uint64) uint64 { return 2*hart + 1 }

func plicSetPriority(source, priority uint32) {
	mmioWrite32(uintptr(plicPriority)+uintptr(source)*4, priority)
}

func plicSetThreshold(ctx uint64, threshold uint32) {
	mmioWrite32(uintptr(plicThreshold)+uintptr(ctx)*0x1000, threshold)
}

func plicEnableSource(ctx uint64, source uint32) {
	addr := uintptr(plicEnable) + uintptr(ctx)*0x80 + uintptr(source/32)*4
	mmioWrite32(addr, mmioRead32(addr)|(1<<(source%32)))
}

//go:nosplit
func plicClaim(ctx uint64) uint32 {
	return mmioRead32(uintptr(plicClaimCompl) + uintptr(ctx)*0x1000)
}

//go:nosplit
func plicComplete(ctx uint64, irq uint32) {
	mmioWrite32(uintptr(plicClaimCompl)+uintptr(ctx)*0x1000, irq)
}
