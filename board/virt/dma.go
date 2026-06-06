// honk - QEMU virt board: DMA buffer helpers shared by the device drivers
// (NVMe, virtio-blk, virtio-net).
//
// honk is identity-mapped (satp=0) with a non-moving GC, so a pinned Go []byte
// is reachable by a device at its own address - no separate DMA arena or IOMMU
// mapping. These helpers are the single owner of that contract: allocate a
// stable, aligned buffer (dmaAlloc) and take its physical (== virtual) address
// (ptr).

//go:build tamago && riscv64

package virt

import "unsafe"

// dmaAlloc returns a zeroed, align-aligned []byte that stays put: honk's GC is
// non-moving and the returned slice keeps its backing array (and thus the
// over-allocation) alive, so a device may DMA to its address.
func dmaAlloc(size, align int) []byte {
	raw := make([]byte, size+align)
	off := (align - int(uintptr(unsafe.Pointer(&raw[0])))%align) % align
	return raw[off : off+size]
}

// ptr returns the physical (== virtual) address of a pinned buffer.
func ptr(b []byte) uint64 { return uint64(uintptr(unsafe.Pointer(&b[0]))) }

// writeAddr writes a 64-bit physical address to a Low/High MMIO register pair.
func writeAddr(low uintptr, addr uint64) {
	mmioWrite32(low, uint32(addr))
	mmioWrite32(low+4, uint32(addr>>32))
}
