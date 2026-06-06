// honk - QEMU virt board: virtio-9p transport driver.
//
// A focused split-virtqueue driver over the shared virtio-mmio v2 transport
// (virtio.go). It carries whole 9P messages between honk's p9 client
// (kernel/p9) and the host file server QEMU exports for a shared directory
// (-fsdev local + -device virtio-9p-device,mount_tag=...). The driver owns no
// 9P protocol knowledge: each exchange is a 2-descriptor chain - the request
// (device-readable) then the reply buffer (device-writable) - published and
// polled to completion, mirroring honk's synchronous virtio-blk path. honk is
// identity-mapped, so the pinned buffers are DMA-addressable at their address.

//go:build tamago && riscv64

package virt

import (
	"errors"
	"runtime"
	"sync"
)

const (
	virtioDev9P      = 9      // virtio subsystem device ID for the 9P transport
	virtio9PMountTag = 1 << 0 // VIRTIO_9P_F_MOUNT_TAG: config carries a mount tag
	p9QueueLen       = 8      // request queue depth (requests are serialized)

	// p9MaxMsg sizes the per-message DMA buffers and so bounds the negotiated 9P
	// msize. Above 8 KiB to avoid QEMU's small-msize degraded-performance note.
	p9MaxMsg = 16384
)

var (
	errP9Oversize = errors.New("virtio9p: message exceeds buffer")
	errP9Timeout  = errors.New("virtio9p: device timeout")
)

// virtio9p is a virtio-9p transport on a virtio-mmio slot. It satisfies the 9P
// client's Transport (MaxMessageSize/RoundTrip) plus a Tag accessor.
type virtio9p struct {
	dev vioDev
	q   vioQueue
	tag string

	mu sync.Mutex
	tx []byte // device-readable request buffer
	rx []byte // device-writable reply buffer
}

// ProbeP9 scans the virtio-mmio slots for a 9P device (a host-shared directory)
// and returns an initialized transport, or nil if none is present.
func ProbeP9() *virtio9p {
	for i := 0; i < virtioSlots; i++ {
		dev, ok := vioProbe(i, virtioDev9P)
		if !ok {
			continue
		}
		d := &virtio9p{dev: dev}
		if d.init() {
			return d
		}
	}
	return nil
}

func (d *virtio9p) init() bool {
	neg, ok := d.dev.negotiate(virtioFVersion1 | virtio9PMountTag)
	if !ok {
		return false
	}
	if !d.q.setup(d.dev, 0, p9QueueLen) {
		return false
	}
	d.tx = dmaAlloc(p9MaxMsg, 1)
	d.rx = dmaAlloc(p9MaxMsg, 1)
	d.dev.ready()

	// The mount tag (config: tag_len u16 at offset 0, then the tag bytes).
	if neg&virtio9PMountTag != 0 {
		n := int(d.dev.config32(0) & 0xffff)
		tag := make([]byte, n)
		for i := range tag {
			tag[i] = d.dev.config8(uintptr(2 + i))
		}
		d.tag = string(tag)
	}
	return true
}

// Tag returns the device's mount tag (the name the host export was given).
func (d *virtio9p) Tag() string { return d.tag }

// MaxMessageSize is the largest 9P message the transport can carry.
func (d *virtio9p) MaxMessageSize() int { return p9MaxMsg }

// RoundTrip sends one 9P T-message and returns the R-message reply (valid only
// until the next call; the p9 client copies it out under its own lock).
func (d *virtio9p) RoundTrip(tmsg []byte) ([]byte, error) {
	if len(tmsg) > p9MaxMsg {
		return nil, errP9Oversize
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	copy(d.tx, tmsg)
	d.q.setDesc(0, ptr(d.tx), uint32(len(tmsg)), descNext, 1)
	d.q.setDesc(1, ptr(d.rx), p9MaxMsg, descWrite, 0)
	d.q.offer(0)
	fence()
	d.dev.notify(0)

	for spins := 0; ; spins++ {
		if _, length, ok := d.q.take(); ok {
			d.dev.ackIRQ()
			if length > p9MaxMsg {
				length = p9MaxMsg
			}
			return d.rx[:length], nil
		}
		if spins > 1<<24 {
			return nil, errP9Timeout
		}
		runtime.Gosched()
	}
}
