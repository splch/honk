// honk - QEMU virt board: virtio-blk driver (a block.Device).
//
// A focused split-virtqueue driver over virtio-mmio v2 (RV64.md §7.4). Requests
// are a 3-descriptor chain (header, data, status) submitted to the request
// queue and completed by polling the used ring - synchronous block I/O behind
// the block.Device interface. honk runs flat/identity-mapped (satp=0), so any
// pinned Go []byte is DMA-addressable at its own address.

//go:build tamago && riscv64

package virt

import (
	"encoding/binary"
	"runtime"
	"sync"
	"unsafe"

	"honk/block"
)

// virtio-mmio transport: 8 device slots on QEMU virt.
const (
	virtioBase   = 0x10001000
	virtioStride = 0x1000
	virtioSlots  = 8

	virtioMagicValue = 0x74726976 // "virt"
	virtioVersion    = 2
	virtioDevBlock   = 2 // VirtIO subsystem device ID for block
)

// virtio-mmio register offsets.
const (
	regMagic          = 0x000
	regVersion        = 0x004
	regDeviceID       = 0x008
	regDeviceFeatures = 0x010
	regDeviceFeatSel  = 0x014
	regDriverFeatures = 0x020
	regDriverFeatSel  = 0x024
	regQueueSel       = 0x030
	regQueueNumMax    = 0x034
	regQueueNum       = 0x038
	regQueueReady     = 0x044
	regQueueNotify    = 0x050
	regInterruptStat  = 0x060
	regInterruptACK   = 0x064
	regStatus         = 0x070
	regQueueDescLow   = 0x080
	regQueueDriverLow = 0x090
	regQueueDeviceLow = 0x0a0
	regConfig         = 0x100
)

// device status bits (values, OR'd into the status register).
const (
	statusAck        = 1
	statusDriver     = 2
	statusDriverOK   = 4
	statusFeaturesOK = 8
)

const virtioFVersion1 = 1 << 32 // VIRTIO_F_VERSION_1 (required for v2)

// descriptor flags.
const (
	descNext  = 1
	descWrite = 2
)

// virtio-blk request types and status.
const (
	blkTypeIn  = 0 // read (device -> memory)
	blkTypeOut = 1 // write (memory -> device)
)

const virtioBlockSize = 512 // virtio-blk sector size

// virtioBlk is a single virtio-blk device on a virtio-mmio slot.
type virtioBlk struct {
	base uintptr

	mu     sync.Mutex
	blocks int64

	// queue size and the three pinned, DMA-addressable virtqueue areas.
	qn    uint16
	desc  []byte // descriptor table
	avail []byte // available ring
	used  []byte // used ring

	availIdx uint16 // next available ring index (free-running)
	usedSeen uint16 // last used ring index we have consumed

	// pinned per-request buffers (one in-flight request, serialized by mu).
	hdr    []byte // 16-byte request header
	status []byte // 1-byte completion status
}

// blockDev is the system block device, discovered by InitStorage.
var blockDev block.Device

// InitStorage probes for a block device and records it for Block().
func InitStorage() { blockDev = ProbeBlock() }

// Block returns the system block device, or nil if none was found.
func Block() block.Device { return blockDev }

// ProbeBlock returns the system block device: NVMe-over-PCIe if present
// (roadmap primary), otherwise virtio-blk (fallback). Both implement the same
// block.Device, so nothing above storage depends on which is used.
func ProbeBlock() block.Device {
	if d := probeNVMe(); d != nil {
		return d
	}
	return probeVirtioBlk()
}

// probeVirtioBlk scans the virtio-mmio slots for a block device and returns an
// initialized driver, or nil if none is present.
func probeVirtioBlk() block.Device {
	for i := 0; i < virtioSlots; i++ {
		base := uintptr(virtioBase + i*virtioStride)
		if mmioRead32(base+regMagic) != virtioMagicValue ||
			mmioRead32(base+regVersion) != virtioVersion ||
			mmioRead32(base+regDeviceID) != virtioDevBlock {
			continue
		}
		d := &virtioBlk{base: base}
		if d.init() {
			println("honk: storage = virtio-blk,", d.blocks, "blocks x", virtioBlockSize, "bytes")
			return d
		}
	}
	return nil
}

func (d *virtioBlk) init() bool {
	// reset, then acknowledge.
	mmioWrite32(d.base+regStatus, 0)
	d.setStatus(statusAck)
	d.setStatus(statusDriver)

	// negotiate features: accept only VIRTIO_F_VERSION_1.
	if d.deviceFeatures()&virtioFVersion1 == 0 {
		return false
	}
	d.setDriverFeatures(virtioFVersion1)
	d.setStatus(statusFeaturesOK)
	if mmioRead32(d.base+regStatus)&statusFeaturesOK == 0 {
		return false
	}

	// set up request queue 0.
	mmioWrite32(d.base+regQueueSel, 0)
	max := mmioRead32(d.base + regQueueNumMax)
	if max == 0 {
		return false
	}
	n := uint16(8)
	if uint32(n) > max {
		n = uint16(max)
	}
	d.qn = n

	d.desc = dmaAlloc(int(n)*16, 16)
	d.avail = dmaAlloc(6+int(n)*2, 2)
	d.used = dmaAlloc(6+int(n)*8, 4)
	d.hdr = dmaAlloc(16, 1)
	d.status = dmaAlloc(1, 1)

	mmioWrite32(d.base+regQueueNum, uint32(n))
	writeAddr(d.base+regQueueDescLow, ptr(d.desc))
	writeAddr(d.base+regQueueDriverLow, ptr(d.avail))
	writeAddr(d.base+regQueueDeviceLow, ptr(d.used))
	mmioWrite32(d.base+regQueueReady, 1)

	d.setStatus(statusDriverOK)

	// capacity (config offset 0): u64 count of 512-byte sectors.
	lo := uint64(mmioRead32(d.base + regConfig))
	hi := uint64(mmioRead32(d.base + regConfig + 4))
	d.blocks = int64(hi<<32 | lo)
	return d.blocks > 0
}

func (d *virtioBlk) BlockSize() int { return virtioBlockSize }
func (d *virtioBlk) Blocks() int64  { return d.blocks }

func (d *virtioBlk) ReadBlocks(start int64, p []byte) error {
	return d.transfer(blkTypeIn, start, p)
}

func (d *virtioBlk) WriteBlocks(start int64, p []byte) error {
	return d.transfer(blkTypeOut, start, p)
}

func (d *virtioBlk) transfer(typ uint32, start int64, p []byte) error {
	if len(p)%virtioBlockSize != 0 || len(p) == 0 {
		return block.ErrAlign
	}
	n := int64(len(p) / virtioBlockSize)
	if start < 0 || start+n > d.blocks {
		return block.ErrRange
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// header: type, reserved, sector.
	binary.LittleEndian.PutUint32(d.hdr[0:], typ)
	binary.LittleEndian.PutUint32(d.hdr[4:], 0)
	binary.LittleEndian.PutUint64(d.hdr[8:], uint64(start))
	d.status[0] = 0xff

	// chain: 0=header (device reads), 1=data, 2=status (device writes).
	dataFlags := uint16(descNext)
	if typ == blkTypeIn {
		dataFlags |= descWrite // device writes data into p on a read
	}
	d.setDesc(0, ptr(d.hdr), 16, descNext, 1)
	d.setDesc(1, ptr(p), uint32(len(p)), dataFlags, 2)
	d.setDesc(2, ptr(d.status), 1, descWrite, 0)

	// publish head descriptor 0 and notify the device.
	binary.LittleEndian.PutUint16(d.avail[4+2*(d.availIdx%d.qn):], 0)
	d.availIdx++
	binary.LittleEndian.PutUint16(d.avail[2:], d.availIdx) // avail.idx
	fence()
	mmioWrite32(d.base+regQueueNotify, 0)

	// poll the used ring for completion.
	for spins := 0; binary.LittleEndian.Uint16(d.used[2:]) == d.usedSeen; spins++ {
		if spins > 1<<24 {
			return block.ErrIO
		}
		runtime.Gosched()
	}
	fence()
	d.usedSeen++

	// acknowledge the (polled) device interrupt so it can re-arm.
	if is := mmioRead32(d.base + regInterruptStat); is != 0 {
		mmioWrite32(d.base+regInterruptACK, is)
	}

	if d.status[0] != 0 {
		return block.ErrIO
	}
	return nil
}

func (d *virtioBlk) setDesc(i int, addr uint64, length uint32, flags, next uint16) {
	o := i * 16
	binary.LittleEndian.PutUint64(d.desc[o:], addr)
	binary.LittleEndian.PutUint32(d.desc[o+8:], length)
	binary.LittleEndian.PutUint16(d.desc[o+12:], flags)
	binary.LittleEndian.PutUint16(d.desc[o+14:], next)
}

func (d *virtioBlk) setStatus(bit uint32) {
	mmioWrite32(d.base+regStatus, mmioRead32(d.base+regStatus)|bit)
}

func (d *virtioBlk) deviceFeatures() uint64 {
	mmioWrite32(d.base+regDeviceFeatSel, 0)
	lo := uint64(mmioRead32(d.base + regDeviceFeatures))
	mmioWrite32(d.base+regDeviceFeatSel, 1)
	hi := uint64(mmioRead32(d.base + regDeviceFeatures))
	return hi<<32 | lo
}

func (d *virtioBlk) setDriverFeatures(f uint64) {
	mmioWrite32(d.base+regDriverFeatSel, 0)
	mmioWrite32(d.base+regDriverFeatures, uint32(f))
	mmioWrite32(d.base+regDriverFeatSel, 1)
	mmioWrite32(d.base+regDriverFeatures, uint32(f>>32))
}

// writeAddr writes a 64-bit physical address to a Low/High register pair.
func writeAddr(low uintptr, addr uint64) {
	mmioWrite32(low, uint32(addr))
	mmioWrite32(low+4, uint32(addr>>32))
}

// ptr returns the physical (== virtual) address of a pinned buffer.
func ptr(b []byte) uint64 { return uint64(uintptr(unsafe.Pointer(&b[0]))) }

// dmaAlloc returns a zeroed, align-aligned []byte that stays put: honk's GC is
// non-moving and the returned slice keeps its backing array (and thus the
// over-allocation) alive, so the device may DMA to its address.
func dmaAlloc(size, align int) []byte {
	raw := make([]byte, size+align)
	off := (align - int(uintptr(unsafe.Pointer(&raw[0])))%align) % align
	return raw[off : off+size]
}
