// honk - QEMU virt board: virtio-blk driver (a block.Device).
//
// A focused split-virtqueue driver over the shared virtio-mmio v2 transport
// (virtio.go). Requests are a 3-descriptor chain (header, data, status)
// published to the request queue and completed by polling the used ring -
// synchronous block I/O behind the block.Device interface. honk runs
// flat/identity-mapped (satp=0), so any pinned Go []byte is DMA-addressable at
// its own address.

//go:build tamago && riscv64

package virt

import (
	"encoding/binary"
	"runtime"
	"sync"

	"honk/block"
)

const virtioDevBlock = 2 // virtio subsystem device ID for block

// virtio-blk request types and the optional flush feature.
const (
	blkTypeIn    = 0 // read (device -> memory)
	blkTypeOut   = 1 // write (memory -> device)
	blkTypeFlush = 4 // flush the device write cache

	virtioBlkFFlush = 1 << 9 // VIRTIO_BLK_F_FLUSH

	virtioBlockSize = 512 // virtio-blk sector size
	blkQueueLen     = 8   // request queue depth (requests are serialized)
)

// virtioBlk is a single virtio-blk device on a virtio-mmio slot.
type virtioBlk struct {
	dev vioDev
	q   vioQueue

	mu       sync.Mutex
	blocks   int64
	canFlush bool // device negotiated VIRTIO_BLK_F_FLUSH

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
		dev, ok := vioProbe(i, virtioDevBlock)
		if !ok {
			continue
		}
		d := &virtioBlk{dev: dev}
		if d.init() {
			println("honk: storage = virtio-blk,", d.blocks, "blocks x", virtioBlockSize, "bytes")
			return d
		}
	}
	return nil
}

func (d *virtioBlk) init() bool {
	// negotiate VIRTIO_F_VERSION_1 (required) plus FLUSH if offered.
	neg, ok := d.dev.negotiate(virtioFVersion1 | virtioBlkFFlush)
	if !ok {
		return false
	}
	d.canFlush = neg&virtioBlkFFlush != 0

	if !d.q.setup(d.dev, 0, blkQueueLen) {
		return false
	}
	d.hdr = dmaAlloc(16, 1)
	d.status = dmaAlloc(1, 1)

	d.dev.ready()

	// capacity (config offset 0): u64 count of 512-byte sectors.
	lo := uint64(d.dev.config32(0))
	hi := uint64(d.dev.config32(4))
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
	d.q.setDesc(0, ptr(d.hdr), 16, descNext, 1)
	d.q.setDesc(1, ptr(p), uint32(len(p)), dataFlags, 2)
	d.q.setDesc(2, ptr(d.status), 1, descWrite, 0)
	return d.doRequest()
}

// Flush issues a VIRTIO_BLK_T_FLUSH so prior writes reach the media. It is a
// no-op if the device did not negotiate the flush feature (then writes have no
// volatile cache to flush).
func (d *virtioBlk) Flush() error {
	if !d.canFlush {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	binary.LittleEndian.PutUint32(d.hdr[0:], blkTypeFlush)
	binary.LittleEndian.PutUint32(d.hdr[4:], 0)
	binary.LittleEndian.PutUint64(d.hdr[8:], 0)
	d.status[0] = 0xff

	// flush has no data buffer: a 2-descriptor chain (header, status).
	d.q.setDesc(0, ptr(d.hdr), 16, descNext, 1)
	d.q.setDesc(1, ptr(d.status), 1, descWrite, 0)
	return d.doRequest()
}

// doRequest publishes descriptor chain head 0 to the available ring, notifies
// the device, polls the used ring for completion, and returns the request
// status. The caller holds d.mu and has populated the descriptor chain.
func (d *virtioBlk) doRequest() error {
	d.q.offer(0)
	fence()
	d.dev.notify(0)

	for spins := 0; ; spins++ {
		if _, _, ok := d.q.take(); ok {
			break
		}
		if spins > 1<<24 {
			return block.ErrIO
		}
		runtime.Gosched()
	}
	d.dev.ackIRQ()

	if d.status[0] != 0 {
		return block.ErrIO
	}
	return nil
}
