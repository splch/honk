//go:build tamago && riscv64

// Package virtio implements a minimal virtio-mmio v2 block driver (RV64.md
// §7.4): the device handshake, one split virtqueue, and synchronous polled
// sector I/O exposed as io.ReaderAt and io.WriterAt. It is single-request and
// relies on honk's identity map so a page-aligned Go allocation's virtual
// address is also its physical (DMA) address.
package virtio

import (
	"errors"
	"unsafe"

	"github.com/splch/honk/internal/mmio"
)

// virtio-mmio v2 register offsets (RV64.md §7.4).
const (
	regMagic         = 0x000
	regVersion       = 0x004
	regDeviceID      = 0x008
	regDeviceFeat    = 0x010
	regDeviceFeatSel = 0x014
	regDriverFeat    = 0x020
	regDriverFeatSel = 0x024
	regQueueSel      = 0x030
	regQueueNumMax   = 0x034
	regQueueNum      = 0x038
	regQueueReady    = 0x044
	regQueueNotify   = 0x050
	regStatus        = 0x070
	regQueueDescLo   = 0x080
	regQueueDescHi   = 0x084
	regQueueDrvLo    = 0x090
	regQueueDrvHi    = 0x094
	regQueueDevLo    = 0x0a0
	regQueueDevHi    = 0x0a4
	regConfig        = 0x100 // device config space; blk capacity (sectors, u64)
)

const (
	magicValue       = 0x74726976 // "virt"
	deviceBlock      = 2
	statusAck        = 1
	statusDriver     = 2
	statusDriverOK   = 4
	statusFeaturesOK = 8

	descNext  = 1 // buffer continues in Next
	descWrite = 2 // device writes this buffer

	// featVersion1 is VIRTIO_F_VERSION_1 (feature bit 32 = bit 0 of the second
	// feature word). VIRTIO 1.x requires v2 (modern) MMIO drivers to negotiate
	// it; a compliant device may refuse FEATURES_OK otherwise (spec §6.1).
	featVersion1 = 1 << 0

	blkTypeIn    = 0      // read
	blkTypeOut   = 1      // write
	blkTypeFlush = 4      // VIRTIO_BLK_T_FLUSH: commit the volatile write cache
	blkFeatFlush = 1 << 9 // VIRTIO_BLK_F_FLUSH (word 0): device has a write cache
	SectorSize   = 512    // bytes
	queueSize    = 8      // descriptors per queue (power of two)
	pageSize     = 4096
)

type virtqDesc struct {
	Addr  uint64
	Len   uint32
	Flags uint16
	Next  uint16
}

type virtqAvail struct {
	Flags uint16
	Idx   uint16
	Ring  [queueSize]uint16
	_     uint16 // used_event
}

type virtqUsedElem struct {
	ID  uint32
	Len uint32
}

type virtqUsed struct {
	Flags uint16
	Idx   uint16
	Ring  [queueSize]virtqUsedElem
	_     uint16 // avail_event
}

type blkReqHeader struct {
	Type     uint32
	Reserved uint32
	Sector   uint64
}

// Block is a virtio-mmio block device. Methods are not safe for concurrent use.
type Block struct {
	base uintptr

	desc  *[queueSize]virtqDesc
	avail *virtqAvail
	used  *virtqUsed

	hdr    *blkReqHeader
	status *byte
	data   *[SectorSize]byte

	descPA, availPA, usedPA uint64
	hdrPA, statusPA, dataPA uint64
	usedIdxPA               uintptr // for a volatile poll of used.Idx

	lastUsed  uint16
	capacity  uint64 // sectors
	flushable bool   // device has a volatile write cache (VIRTIO_BLK_F_FLUSH)
}

// dmaKeep pins every DMA allocation for the life of the program so the GC never
// reclaims memory the device may write to.
var dmaKeep [][]byte

// dmaPage returns a page-aligned, GC-pinned page and its physical address
// (== virtual, under honk's identity map).
func dmaPage() (page []byte, pa uint64) {
	raw := make([]byte, 2*pageSize) // alignment slack
	dmaKeep = append(dmaKeep, raw)
	addr := uintptr(unsafe.Pointer(&raw[0]))
	off := int((-addr) & (pageSize - 1))
	return raw[off : off+pageSize], uint64(addr) + uint64(off)
}

// MagicValue/DeviceID let a caller probe an mmio slot before committing to it.
func MagicValue(base uintptr) uint32 { return mmio.R32(base + regMagic) }
func DeviceID(base uintptr) uint32   { return mmio.R32(base + regDeviceID) }

// IsBlock reports whether the mmio slot at base is a virtio block device.
func IsBlock(base uintptr) bool {
	return MagicValue(base) == magicValue && DeviceID(base) == deviceBlock
}

// New performs the virtio-mmio v2 handshake for the block device at base and
// returns a ready driver (RV64.md §7.4, "Init handshake").
func New(base uintptr) (*Block, error) {
	if MagicValue(base) != magicValue {
		return nil, errors.New("virtio: bad magic")
	}
	if mmio.R32(base+regVersion) != 2 {
		return nil, errors.New("virtio: not version 2")
	}
	if DeviceID(base) != deviceBlock {
		return nil, errors.New("virtio: not a block device")
	}

	b := &Block{base: base}
	b.allocDMA()

	// Reset, then ACKNOWLEDGE + DRIVER.
	mmio.W32(base+regStatus, 0)
	st := uint32(statusAck | statusDriver)
	mmio.W32(base+regStatus, st)

	// Negotiate VIRTIO_F_VERSION_1 (required for v2 devices, §6.1) and, if the
	// device exposes a volatile write cache, VIRTIO_BLK_F_FLUSH so writes can be
	// committed durably (Flush). Mask against what the device offers.
	mmio.W32(base+regDeviceFeatSel, 0)
	lo := mmio.R32(base + regDeviceFeat)
	mmio.W32(base+regDeviceFeatSel, 1)
	hi := mmio.R32(base + regDeviceFeat)
	b.flushable = lo&blkFeatFlush != 0
	mmio.W32(base+regDriverFeatSel, 0)
	mmio.W32(base+regDriverFeat, lo&blkFeatFlush)
	mmio.W32(base+regDriverFeatSel, 1)
	mmio.W32(base+regDriverFeat, hi&featVersion1)
	st |= statusFeaturesOK
	mmio.W32(base+regStatus, st)
	if mmio.R32(base+regStatus)&statusFeaturesOK == 0 {
		return nil, errors.New("virtio: device rejected FEATURES_OK")
	}

	// Set up queue 0.
	mmio.W32(base+regQueueSel, 0)
	if mmio.R32(base+regQueueNumMax) < queueSize {
		return nil, errors.New("virtio: queue too small")
	}
	mmio.W32(base+regQueueNum, queueSize)
	mmio.W32(base+regQueueDescLo, uint32(b.descPA))
	mmio.W32(base+regQueueDescHi, uint32(b.descPA>>32))
	mmio.W32(base+regQueueDrvLo, uint32(b.availPA))
	mmio.W32(base+regQueueDrvHi, uint32(b.availPA>>32))
	mmio.W32(base+regQueueDevLo, uint32(b.usedPA))
	mmio.W32(base+regQueueDevHi, uint32(b.usedPA>>32))
	mmio.W32(base+regQueueReady, 1)

	mmio.W32(base+regStatus, st|statusDriverOK)

	b.capacity = uint64(mmio.R32(base+regConfig)) | uint64(mmio.R32(base+regConfig+4))<<32
	return b, nil
}

// Size returns the device capacity in bytes.
func (b *Block) Size() int64 { return int64(b.capacity) * SectorSize }

// readSector reads one 512-byte sector into b.data via a 3-descriptor chain:
// header (device reads) -> data (device writes) -> status (device writes).
func (b *Block) readSector(sector uint64) error {
	b.hdr.Type = blkTypeIn
	b.hdr.Reserved = 0
	b.hdr.Sector = sector
	*b.status = 0xff // device sets 0 on success

	b.desc[0] = virtqDesc{Addr: b.hdrPA, Len: 16, Flags: descNext, Next: 1}
	b.desc[1] = virtqDesc{Addr: b.dataPA, Len: SectorSize, Flags: descNext | descWrite, Next: 2}
	b.desc[2] = virtqDesc{Addr: b.statusPA, Len: 1, Flags: descWrite, Next: 0}

	b.avail.Ring[b.avail.Idx%queueSize] = 0 // chain head = descriptor 0
	mmio.Fence()                            // ring entry before idx bump
	b.avail.Idx++
	mmio.Fence() // idx bump before the doorbell
	mmio.W32(b.base+regQueueNotify, 0)

	for mmio.R16(b.usedIdxPA) == b.lastUsed { // volatile poll of used.Idx
	}
	b.lastUsed++
	mmio.Fence() // completion observed before reading the buffers

	if mmio.R8(uintptr(b.statusPA)) != 0 {
		return errors.New("virtio: block read failed")
	}
	return nil
}

// ReadAt implements io.ReaderAt over the device, reading whole sectors and
// copying out the requested window.
func (b *Block) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("virtio: negative offset")
	}
	n := 0
	for n < len(p) {
		pos := off + int64(n)
		if pos >= b.Size() {
			return n, errors.New("EOF")
		}
		if err := b.readSector(uint64(pos / SectorSize)); err != nil {
			return n, err
		}
		n += copy(p[n:], b.data[pos%SectorSize:])
	}
	return n, nil
}

// writeSector writes b.data to one 512-byte sector via header (device reads) ->
// data (device reads) -> status (device writes).
func (b *Block) writeSector(sector uint64) error {
	b.hdr.Type = blkTypeOut
	b.hdr.Reserved = 0
	b.hdr.Sector = sector
	*b.status = 0xff // device sets 0 on success

	b.desc[0] = virtqDesc{Addr: b.hdrPA, Len: 16, Flags: descNext, Next: 1}
	b.desc[1] = virtqDesc{Addr: b.dataPA, Len: SectorSize, Flags: descNext, Next: 2} // device reads
	b.desc[2] = virtqDesc{Addr: b.statusPA, Len: 1, Flags: descWrite, Next: 0}

	b.avail.Ring[b.avail.Idx%queueSize] = 0
	mmio.Fence()
	b.avail.Idx++
	mmio.Fence()
	mmio.W32(b.base+regQueueNotify, 0)

	for mmio.R16(b.usedIdxPA) == b.lastUsed {
	}
	b.lastUsed++
	mmio.Fence()

	if mmio.R8(uintptr(b.statusPA)) != 0 {
		return errors.New("virtio: block write failed")
	}
	return nil
}

// WriteAt implements io.WriterAt over the device. Sub-sector writes do a
// read-modify-write so the rest of the touched sector is preserved.
func (b *Block) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("virtio: negative offset")
	}
	n := 0
	for n < len(p) {
		pos := off + int64(n)
		if pos >= b.Size() {
			return n, errors.New("EOF")
		}
		sector := uint64(pos / SectorSize)
		within := int(pos % SectorSize)
		if within != 0 || len(p)-n < SectorSize { // partial sector: preserve the rest
			if err := b.readSector(sector); err != nil {
				return n, err
			}
		}
		m := copy(b.data[within:], p[n:])
		if err := b.writeSector(sector); err != nil {
			return n, err
		}
		n += m
	}
	return n, nil
}

// Flush issues a VIRTIO_BLK_T_FLUSH so the device commits completed writes to
// non-volatile storage. It is a no-op when the device has no volatile write
// cache (VIRTIO_BLK_F_FLUSH not negotiated). A flush request is a 2-descriptor
// chain: header (device reads) -> status (device writes).
func (b *Block) Flush() error {
	if !b.flushable {
		return nil
	}
	b.hdr.Type = blkTypeFlush
	b.hdr.Reserved = 0
	b.hdr.Sector = 0 // MUST be 0 for a flush request
	*b.status = 0xff

	b.desc[0] = virtqDesc{Addr: b.hdrPA, Len: 16, Flags: descNext, Next: 1}
	b.desc[1] = virtqDesc{Addr: b.statusPA, Len: 1, Flags: descWrite, Next: 0}

	b.avail.Ring[b.avail.Idx%queueSize] = 0
	mmio.Fence()
	b.avail.Idx++
	mmio.Fence()
	mmio.W32(b.base+regQueueNotify, 0)

	for mmio.R16(b.usedIdxPA) == b.lastUsed {
	}
	b.lastUsed++
	mmio.Fence()

	if mmio.R8(uintptr(b.statusPA)) != 0 {
		return errors.New("virtio: block flush failed")
	}
	return nil
}

// allocDMA reserves page-aligned, GC-pinned DMA memory for the virtqueue and the
// request buffers; under honk's identity map each region's virtual address is
// also its physical address.
func (b *Block) allocDMA() {
	q, qpa := dmaPage()
	b.desc = (*[queueSize]virtqDesc)(unsafe.Pointer(&q[0]))
	b.avail = (*virtqAvail)(unsafe.Pointer(&q[256]))
	b.used = (*virtqUsed)(unsafe.Pointer(&q[512]))
	b.descPA, b.availPA, b.usedPA = qpa, qpa+256, qpa+512
	b.usedIdxPA = uintptr(qpa) + 512 + 2 // &used.Idx

	r, rpa := dmaPage()
	b.hdr = (*blkReqHeader)(unsafe.Pointer(&r[0]))
	b.status = (*byte)(unsafe.Pointer(&r[16]))
	b.data = (*[SectorSize]byte)(unsafe.Pointer(&r[512]))
	b.hdrPA, b.statusPA, b.dataPA = rpa, rpa+16, rpa+512
}
