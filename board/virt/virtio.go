// honk - QEMU virt board: virtio-mmio v2 transport (shared by virtio-blk and
// virtio-net).
//
// This is the single owner of the virtio-mmio register layout and the device
// lifecycle handshake - reset, feature negotiation, split-virtqueue setup, and
// notify (RV64.md §7.4). Device drivers (virtioblk.go, virtionet.go) layer
// their request/packet handling on top and never touch the registers directly,
// so the wire format lives in exactly one place.
//
// honk runs flat/identity-mapped (satp=0) with a non-moving GC, so any pinned
// Go []byte is DMA-addressable at its own address (see dmaAlloc).

//go:build tamago && riscv64

package virt

import "encoding/binary"

// virtio-mmio transport: 8 device slots on QEMU virt, 0x1000 apart.
const (
	virtioBase   = 0x10001000
	virtioStride = 0x1000
	virtioSlots  = 8

	virtioMagicValue = 0x74726976 // "virt"
	virtioVersion    = 2          // modern (v2) transport
)

// virtio-mmio register offsets (RV64.md §7.4).
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

// device status bits (OR'd into the status register).
const (
	statusAck        = 1
	statusDriver     = 2
	statusDriverOK   = 4
	statusFeaturesOK = 8
)

// virtio descriptor flags.
const (
	descNext  = 1 // chain continues at .next
	descWrite = 2 // device writes this buffer (vs. reads it)
)

// VIRTIO_F_VERSION_1 (feature bit 32): required to drive a v2 device.
const virtioFVersion1 = 1 << 32

// vioDev is a virtio-mmio v2 device at a fixed MMIO base. It owns the register
// access and the device-lifecycle handshake; it carries no per-driver state, so
// it is passed by value.
type vioDev struct{ base uintptr }

// vioProbe returns the transport at virtio-mmio slot i if it is a v2 device of
// the given subsystem device id (e.g. 1=net, 2=block), else ok=false.
func vioProbe(slot int, deviceID uint32) (dev vioDev, ok bool) {
	base := uintptr(virtioBase + slot*virtioStride)
	if mmioRead32(base+regMagic) != virtioMagicValue ||
		mmioRead32(base+regVersion) != virtioVersion ||
		mmioRead32(base+regDeviceID) != deviceID {
		return vioDev{}, false
	}
	return vioDev{base}, true
}

func (d vioDev) addStatus(bit uint32) {
	mmioWrite32(d.base+regStatus, mmioRead32(d.base+regStatus)|bit)
}

func (d vioDev) status() uint32 { return mmioRead32(d.base + regStatus) }

func (d vioDev) deviceFeatures() uint64 {
	mmioWrite32(d.base+regDeviceFeatSel, 0)
	lo := uint64(mmioRead32(d.base + regDeviceFeatures))
	mmioWrite32(d.base+regDeviceFeatSel, 1)
	hi := uint64(mmioRead32(d.base + regDeviceFeatures))
	return hi<<32 | lo
}

func (d vioDev) setDriverFeatures(f uint64) {
	mmioWrite32(d.base+regDriverFeatSel, 0)
	mmioWrite32(d.base+regDriverFeatures, uint32(f))
	mmioWrite32(d.base+regDriverFeatSel, 1)
	mmioWrite32(d.base+regDriverFeatures, uint32(f>>32))
}

// negotiate runs reset -> ACKNOWLEDGE -> DRIVER, offers the intersection of the
// device features and want, and confirms FEATURES_OK. It returns the negotiated
// feature set; ok is false if the device rejected the features (VERSION_1 must
// be in want for a v2 device).
func (d vioDev) negotiate(want uint64) (negotiated uint64, ok bool) {
	mmioWrite32(d.base+regStatus, 0) // reset
	d.addStatus(statusAck)
	d.addStatus(statusDriver)
	negotiated = d.deviceFeatures() & want
	d.setDriverFeatures(negotiated)
	d.addStatus(statusFeaturesOK)
	return negotiated, d.status()&statusFeaturesOK != 0
}

// ready signals DRIVER_OK: setup is complete and the device may run.
func (d vioDev) ready() { d.addStatus(statusDriverOK) }

// notify tells the device that queue has new available buffers.
func (d vioDev) notify(queue uint32) { mmioWrite32(d.base+regQueueNotify, queue) }

// ackIRQ acknowledges any asserted device interrupt so it can re-arm. The
// polled paths call it after draining; the IRQ-driven path is a later
// optimization (HONK.md async-I/O model).
func (d vioDev) ackIRQ() {
	if is := mmioRead32(d.base + regInterruptStat); is != 0 {
		mmioWrite32(d.base+regInterruptACK, is)
	}
}

func (d vioDev) config8(off uintptr) uint8   { return mmioRead8(d.base + regConfig + off) }
func (d vioDev) config32(off uintptr) uint32 { return mmioRead32(d.base + regConfig + off) }

// vioQueue is one split virtqueue: the three DMA-resident areas (descriptor
// table, available ring, used ring) plus the driver-side free-running indices.
// It hides the ring layout and index arithmetic; a driver fills descriptors,
// offers chains, and consumes completions.
type vioQueue struct {
	desc, avail, used []byte
	qn                uint16 // queue size (number of descriptors)
	availIdx          uint16 // next available-ring slot to publish (free-running)
	usedSeen          uint16 // last used-ring index consumed (free-running)
}

// setup selects queue sel on dev, negotiates its size (min of want and the
// device maximum), allocates the DMA areas, registers their addresses, and
// marks the queue ready. It returns false if the device has no such queue.
func (q *vioQueue) setup(d vioDev, sel int, want uint16) bool {
	mmioWrite32(d.base+regQueueSel, uint32(sel))
	max := mmioRead32(d.base + regQueueNumMax)
	if max == 0 {
		return false
	}
	q.qn = want
	if uint32(want) > max {
		q.qn = uint16(max)
	}
	q.desc = dmaAlloc(int(q.qn)*16, 16)
	q.avail = dmaAlloc(6+int(q.qn)*2, 2)
	q.used = dmaAlloc(6+int(q.qn)*8, 4)
	mmioWrite32(d.base+regQueueNum, uint32(q.qn))
	writeAddr(d.base+regQueueDescLow, ptr(q.desc))
	writeAddr(d.base+regQueueDriverLow, ptr(q.avail))
	writeAddr(d.base+regQueueDeviceLow, ptr(q.used))
	mmioWrite32(d.base+regQueueReady, 1)
	return true
}

// setDesc writes descriptor i (addr, length, flags, and the next index for a
// chain).
func (q *vioQueue) setDesc(i int, addr uint64, length uint32, flags, next uint16) {
	o := i * 16
	binary.LittleEndian.PutUint64(q.desc[o:], addr)
	binary.LittleEndian.PutUint32(q.desc[o+8:], length)
	binary.LittleEndian.PutUint16(q.desc[o+12:], flags)
	binary.LittleEndian.PutUint16(q.desc[o+14:], next)
}

// offer publishes descriptor-chain head into the available ring and advances
// the available index. The caller fences and notifies.
func (q *vioQueue) offer(head uint16) {
	binary.LittleEndian.PutUint16(q.avail[4+2*(q.availIdx%q.qn):], head)
	q.availIdx++
	binary.LittleEndian.PutUint16(q.avail[2:], q.availIdx)
}

// take consumes the next completed used-ring entry, returning the head
// descriptor id and the device-written byte count. ok is false if none are
// pending.
func (q *vioQueue) take() (id, length uint32, ok bool) {
	if binary.LittleEndian.Uint16(q.used[2:]) == q.usedSeen {
		return 0, 0, false
	}
	fence() // order the used-ring read after the device's index advance
	o := 4 + 8*int(q.usedSeen%q.qn)
	id = binary.LittleEndian.Uint32(q.used[o:])
	length = binary.LittleEndian.Uint32(q.used[o+4:])
	q.usedSeen++
	return id, length, true
}
