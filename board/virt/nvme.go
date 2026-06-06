// honk - QEMU virt board: NVMe-over-PCIe driver (a block.Device).
//
// A focused NVMe 1.x driver: find the controller on PCIe, bring it up, create
// one admin and one I/O queue pair, identify namespace 1, and issue Read/Write
// commands with PRP data pointers. Completions are polled (synchronous I/O
// behind the block.Device interface). honk is identity-mapped (satp=0) with a
// non-moving GC, so pinned []byte buffers are DMA-addressable at their address.

//go:build tamago && riscv64

package virt

import (
	"encoding/binary"
	"runtime"
	"sync"

	"honk/block"
)

const (
	nvmeClass    = 0x010802 // PCI class: mass storage / NVM / NVMe
	nvmeBAR0     = pcieMMIO // where we map the controller registers
	nvmeQueueLen = 16       // admin and I/O queue depth
	nvmePageSize = 4096

	// controller registers (offsets in BAR0)
	regCAP  = 0x00 // capabilities (64-bit)
	regCC   = 0x14 // controller configuration
	regCSTS = 0x1c // controller status
	regAQA  = 0x24 // admin queue attributes
	regASQ  = 0x28 // admin submission queue base (64-bit)
	regACQ  = 0x30 // admin completion queue base (64-bit)
	regDB   = 0x1000

	ccEnable = 1 << 0 // CC.EN
	cstsRdy  = 1 << 0 // CSTS.RDY

	// opcodes
	opCreateIOSQ = 0x01
	opCreateIOCQ = 0x05
	opIdentify   = 0x06
	opWrite      = 0x01
	opRead       = 0x02
)

// nvmeQueue is one submission/completion queue pair (physically contiguous,
// pinned DMA buffers).
type nvmeQueue struct {
	sq   []byte // submission entries (64 bytes each)
	cq   []byte // completion entries (16 bytes each)
	sqDB uintptr
	cqDB uintptr

	sqTail uint16
	cqHead uint16
	phase  uint16 // expected CQ phase tag (starts at 1)
}

type nvmeDevice struct {
	bar uintptr

	mu    sync.Mutex
	admin nvmeQueue
	io    nvmeQueue

	cid    uint16
	nsid   uint32
	bs     int
	blocks int64
}

// probeNVMe finds and initializes an NVMe controller, or returns nil.
func probeNVMe() block.Device {
	dev, ok := pciFindByClass(nvmeClass)
	if !ok {
		return nil
	}
	pciSetupBAR0(dev, nvmeBAR0)

	d := &nvmeDevice{bar: uintptr(nvmeBAR0), nsid: 1}
	if !d.init() {
		return nil
	}
	println("honk: storage = NVMe,", d.blocks, "blocks x", d.bs, "bytes")
	return d
}

func (d *nvmeDevice) init() bool {
	stride := uintptr(4) << ((d.cap() >> 32) & 0xf) // CAP.DSTRD

	// disable the controller and wait until it is not ready.
	mmioWrite32(d.bar+regCC, mmioRead32(d.bar+regCC)&^uint32(ccEnable))
	if !d.waitReady(false) {
		return false
	}

	// admin queue pair (queue 0).
	d.admin = newQueue(d.bar, 0, stride)
	mmioWrite32(d.bar+regAQA, uint32(nvmeQueueLen-1)<<16|uint32(nvmeQueueLen-1))
	writeAddr(d.bar+regASQ, ptr(d.admin.sq))
	writeAddr(d.bar+regACQ, ptr(d.admin.cq))

	// enable: IOCQES=4 (16B), IOSQES=6 (64B), MPS=0 (4KiB), EN=1.
	mmioWrite32(d.bar+regCC, 4<<20|6<<16|ccEnable)
	if !d.waitReady(true) {
		return false
	}

	// I/O queue pair (queue 1): create the completion queue, then the
	// submission queue that targets it.
	d.io = newQueue(d.bar, 1, stride)
	if d.adminCmd(func(c []byte) {
		c[0] = opCreateIOCQ
		binary.LittleEndian.PutUint64(c[24:], ptr(d.io.cq))                 // PRP1
		binary.LittleEndian.PutUint32(c[40:], uint32(nvmeQueueLen-1)<<16|1) // CDW10: size, qid
		binary.LittleEndian.PutUint32(c[44:], 1)                            // CDW11: PC=1
	}) != 0 {
		return false
	}
	if d.adminCmd(func(c []byte) {
		c[0] = opCreateIOSQ
		binary.LittleEndian.PutUint64(c[24:], ptr(d.io.sq))                 // PRP1
		binary.LittleEndian.PutUint32(c[40:], uint32(nvmeQueueLen-1)<<16|1) // CDW10: size, qid
		binary.LittleEndian.PutUint32(c[44:], 1<<16|1)                      // CDW11: CQID=1, PC=1
	}) != 0 {
		return false
	}

	// identify namespace 1 to learn capacity and LBA size.
	id := dmaAlloc(nvmePageSize, nvmePageSize)
	if d.adminCmd(func(c []byte) {
		c[0] = opIdentify
		binary.LittleEndian.PutUint32(c[4:], d.nsid) // NSID
		binary.LittleEndian.PutUint64(c[24:], ptr(id))
		binary.LittleEndian.PutUint32(c[40:], 0) // CDW10: CNS=0 (namespace)
	}) != 0 {
		return false
	}
	fence()
	d.blocks = int64(binary.LittleEndian.Uint64(id[0:])) // NSZE
	flbas := id[26] & 0xf
	lbads := id[128+int(flbas)*4+2] // LBADS byte of the active LBA format
	d.bs = 1 << lbads
	return d.bs >= 512 && d.blocks > 0
}

func (d *nvmeDevice) BlockSize() int { return d.bs }
func (d *nvmeDevice) Blocks() int64  { return d.blocks }

func (d *nvmeDevice) ReadBlocks(start int64, p []byte) error {
	return d.rw(opRead, start, p)
}

func (d *nvmeDevice) WriteBlocks(start int64, p []byte) error {
	return d.rw(opWrite, start, p)
}

func (d *nvmeDevice) rw(op uint8, start int64, p []byte) error {
	if len(p) == 0 || len(p)%d.bs != 0 {
		return block.ErrAlign
	}
	n := int64(len(p) / d.bs)
	if start < 0 || start+n > d.blocks {
		return block.ErrRange
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// Split into chunks of at most one page so the data fits in PRP1[+PRP2]
	// (two pages worst case), avoiding PRP lists. The buffer is physically
	// contiguous, so PRP2 is simply the next page boundary.
	perChunk := nvmePageSize / d.bs // blocks per page
	for off := 0; off < len(p); {
		blocksLeft := (len(p) - off) / d.bs
		nb := blocksLeft
		if nb > perChunk {
			nb = perChunk
		}
		buf := p[off : off+nb*d.bs]
		prp1 := ptr(buf)
		var prp2 uint64
		if (prp1&(nvmePageSize-1))+uint64(len(buf)) > nvmePageSize {
			prp2 = (prp1 &^ (nvmePageSize - 1)) + nvmePageSize
		}
		lba := uint64(start) + uint64(off/d.bs)
		if d.ioCmd(op, lba, uint16(nb-1), prp1, prp2) != 0 {
			return block.ErrIO
		}
		off += nb * d.bs
	}
	return nil
}

// ioCmd issues a Read/Write on the I/O queue and returns the NVMe status.
func (d *nvmeDevice) ioCmd(op uint8, lba uint64, nlb uint16, prp1, prp2 uint64) uint32 {
	return d.submit(&d.io, func(c []byte) {
		c[0] = op
		binary.LittleEndian.PutUint32(c[4:], d.nsid)
		binary.LittleEndian.PutUint64(c[24:], prp1)
		binary.LittleEndian.PutUint64(c[32:], prp2)
		binary.LittleEndian.PutUint64(c[40:], lba)         // CDW10/11: SLBA
		binary.LittleEndian.PutUint32(c[48:], uint32(nlb)) // CDW12: NLB (0-based)
	})
}

func (d *nvmeDevice) adminCmd(build func(c []byte)) uint32 {
	return d.submit(&d.admin, build)
}

// submit writes a command into q's submission queue, rings the doorbell, polls
// the completion queue, and returns the NVMe status field (0 = success).
func (d *nvmeDevice) submit(q *nvmeQueue, build func(c []byte)) uint32 {
	var cmd [64]byte
	build(cmd[:])
	d.cid++
	binary.LittleEndian.PutUint16(cmd[2:], d.cid)

	copy(q.sq[int(q.sqTail)*64:], cmd[:])
	q.sqTail = (q.sqTail + 1) % nvmeQueueLen
	fence()
	mmioWrite32(q.sqDB, uint32(q.sqTail))

	// poll the completion queue entry's phase tag.
	off := int(q.cqHead) * 16
	for spins := 0; ; spins++ {
		dw3 := binary.LittleEndian.Uint32(q.cq[off+12:])
		if uint16((dw3>>16)&1) == q.phase {
			fence()
			status := dw3 >> 17
			q.cqHead++
			if q.cqHead == nvmeQueueLen {
				q.cqHead = 0
				q.phase ^= 1
			}
			mmioWrite32(q.cqDB, uint32(q.cqHead))
			return status
		}
		if spins > 1<<24 {
			return 1 // timeout
		}
		runtime.Gosched()
	}
}

func newQueue(bar uintptr, qid int, stride uintptr) nvmeQueue {
	return nvmeQueue{
		sq:    dmaAlloc(nvmeQueueLen*64, nvmePageSize),
		cq:    dmaAlloc(nvmeQueueLen*16, nvmePageSize),
		sqDB:  bar + regDB + uintptr(2*qid)*stride,
		cqDB:  bar + regDB + uintptr(2*qid+1)*stride,
		phase: 1,
	}
}

func (d *nvmeDevice) cap() uint64 {
	lo := uint64(mmioRead32(d.bar + regCAP))
	hi := uint64(mmioRead32(d.bar + regCAP + 4))
	return hi<<32 | lo
}

func (d *nvmeDevice) waitReady(want bool) bool {
	for spins := 0; spins < 1<<24; spins++ {
		if (mmioRead32(d.bar+regCSTS)&cstsRdy != 0) == want {
			return true
		}
		runtime.Gosched()
	}
	return false
}
