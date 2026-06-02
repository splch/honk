//go:build tamago && riscv64

// Package plic drives the SiFive Platform-Level Interrupt Controller (RV64.md
// §4.2). Each hart owns two interrupt "contexts"; the S-mode context for hart h
// is 2*h+1 (the #1 PLIC gotcha — the per-hart stride is twice the per-context
// stride).
package plic

import "github.com/splch/honk/internal/mmio"

// Register block offsets from the PLIC base.
const (
	priorityBase = 0x000000 // + 4*source
	enableBase   = 0x002000 // + 0x80*context
	thresholdoff = 0x200000 // + 0x1000*context
	claimoff     = 0x200004 // + 0x1000*context
)

// PLIC is a driver bound to one hart's S-mode context.
type PLIC struct {
	base    uintptr
	context uintptr
}

// New returns a driver for the PLIC at base, targeting hart's S-mode context.
func New(base uintptr, hart int) *PLIC {
	return &PLIC{base: base, context: uintptr(2*hart + 1)}
}

// EnableSource routes interrupt source src to this context: priority 1 (nonzero
// = enabled), the context's enable bit set, and the threshold lowered to 0 so
// any nonzero priority is delivered.
func (p *PLIC) EnableSource(src uintptr) {
	mmio.W32(p.base+priorityBase+4*src, 1)
	en := p.base + enableBase + 0x80*p.context + (src/32)*4
	mmio.W32(en, mmio.R32(en)|(1<<(src%32)))
	mmio.W32(p.base+thresholdoff+0x1000*p.context, 0)
}

// Claim returns the highest-priority pending source for this context (and marks
// it in-progress), or 0 if none is pending.
func (p *PLIC) Claim() uint32 {
	return mmio.R32(p.base + claimoff + 0x1000*p.context)
}

// Complete signals end-of-handling for src; the source cannot re-fire until
// this is written back (RV64.md Appendix F #18).
func (p *PLIC) Complete(src uint32) {
	mmio.W32(p.base+claimoff+0x1000*p.context, src)
}
