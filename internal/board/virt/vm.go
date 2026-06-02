//go:build tamago && riscv64

package virt

import (
	"runtime"
	"unsafe"
)

// Sv39 PTE flags (RV64.md §5.1, Appendix E).
const (
	pteV = 1 << 0
	pteR = 1 << 1
	pteW = 1 << 2
	pteX = 1 << 3
	pteA = 1 << 6
	pteD = 1 << 7

	pageSize = 1 << 12 // 4 KiB
	megaSize = 1 << 21 // 2 MiB megapage

	// Leaf permissions, with A/D preset so honk is correct on Svade hardware
	// (which faults on A=0 / D=0) as well as QEMU's Svadu (RV64.md §5.3).
	leafRX = pteV | pteR | pteX | pteA | pteD // text: read + execute, never write
	leafRW = pteV | pteR | pteW | pteA | pteD // data/heap/stack/MMIO: read + write, never exec
)

func setSATP(satp uint64) // vm_riscv64.s
func readSATP() uint64    // vm_riscv64.s

// pageTables keeps the Sv39 tables alive: the GC must not reclaim them, and as
// a []byte they hold no Go pointers so they are never scanned into.
var pageTables []byte

func pa2pte(pa uint64) uint64  { return (pa >> 12) << 10 }
func pageDown(x uint64) uint64 { return x &^ (pageSize - 1) }
func pageUp(x uint64) uint64   { return (x + pageSize - 1) &^ (pageSize - 1) }

// enablePaging builds an identity-mapped Sv39 table that enforces W^X and
// switches it on. The mapping is identity (VA==PA) and honk's text is mapped in
// place, so the instruction fetched after the satp write stays mapped
// (RV64.md §5.2). Kernel text is R|X; everything else — data, heap, stack, and
// device MMIO — is R|W and never executable. No page is both writable and
// executable, so a stray write to code or a jump into data becomes a reported
// fault rather than an exploit primitive (DESIGN.md §9).
func enablePaging() {
	ts, te := runtime.TextRegion()
	tStart, tEnd := pageDown(ts), pageUp(te)

	const nTables = 8 // root + level1 + up to 6 level-0 tables (text spans ≤ 2 MiB)
	tbl, pa := allocTables(nTables)
	root, level1 := tbl[0], tbl[1]
	next := 2

	// Low devices (CLINT, PLIC, UART, virtio, test): identity 1 GiB gigapage
	// [0, 1 GiB), R/W, no-exec. honk reaches firmware via ecall today, but this
	// readies the MMIO window for native drivers.
	root[0] = pa2pte(0) | leafRW
	// Main RAM gigapage [0x80000000, 0xC0000000): pointer to the level-1 table.
	root[2] = pa2pte(pa[1]) | pteV

	rs, re := ramStart, ramStart+ramSize
	for va := uint64(0x80000000); va < 0xc0000000; va += megaSize {
		if va < rs || va >= re {
			continue // OpenSBI's region and any hole: leave unmapped
		}
		i := (va >> 21) & 0x1ff
		if va+megaSize > tStart && va < tEnd {
			// This 2 MiB range holds executable text: map it at 4 KiB so text
			// (R|X) and surrounding data (R|W) get distinct permissions.
			l0, l0pa := tbl[next], pa[next]
			next++
			level1[i] = pa2pte(l0pa) | pteV
			for off := uint64(0); off < megaSize; off += pageSize {
				p := va + off
				perm := uint64(leafRW)
				if p >= tStart && p < tEnd {
					perm = leafRX
				}
				l0[(p>>12)&0x1ff] = pa2pte(p) | perm
			}
		} else {
			level1[i] = pa2pte(va) | leafRW // 2 MiB megapage, R/W, no-exec
		}
	}

	setSATP((8 << 60) | (pa[0] >> 12)) // MODE = Sv39, root PPN
	if readSATP()>>60 != 8 {
		// Writing satp with an unsupported MODE is silently ignored (Appendix
		// F #11); fall back to running unpaged rather than chasing phantoms.
		puts("honk/virt: WARNING — Sv39 did not engage; running unpaged\n")
		return
	}
	puts("honk/virt: Sv39 paging on, W^X (text ")
	printHex(tStart)
	puts("..")
	printHex(tEnd)
	puts(")\n")
}

// allocTables reserves n page-aligned 4 KiB tables (zeroed = all-invalid PTEs)
// and returns each as a []uint64 view plus its physical address (== virtual,
// since honk is identity-mapped).
func allocTables(n int) (tbls []*[512]uint64, pas []uint64) {
	pageTables = make([]byte, (n+1)*pageSize) // +1 page of alignment slack
	addr := uintptr(unsafe.Pointer(&pageTables[0]))
	skip := int((-addr) & (pageSize - 1))
	tbls = make([]*[512]uint64, n)
	pas = make([]uint64, n)
	for i := range tbls {
		o := skip + i*pageSize
		tbls[i] = (*[512]uint64)(unsafe.Pointer(&pageTables[o]))
		pas[i] = uint64(addr) + uint64(o)
	}
	return tbls, pas
}
