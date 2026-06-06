// honk - QEMU virt board: virtio-net driver.
//
// A focused split-virtqueue driver over the shared virtio-mmio v2 transport
// (virtio.go). It exposes the two methods go-net's NetworkDevice needs -
// Receive and Transmit of raw Ethernet frames - plus the device MAC; the
// gVisor TCP/IP stack is wired on top of it in kernel/net.go.
//
// The receive queue is pre-filled with device-writable buffers that the device
// fills as frames arrive; Receive polls the used ring and recycles each buffer.
// Transmit is synchronous (serialized, poll-to-completion), mirroring honk's
// polled block I/O - the IRQ-driven path is a later optimization (HONK.md
// async-I/O model). honk is identity-mapped, so pinned buffers are
// DMA-addressable at their own address.

//go:build tamago && riscv64

package virt

import (
	"runtime"
	"sync"
)

const (
	virtioDevNet = 1      // virtio subsystem device ID for network
	virtioNetMAC = 1 << 5 // VIRTIO_NET_F_MAC: config carries a valid MAC

	// netHdrLen is the virtio_net_hdr length. With VIRTIO_F_VERSION_1 the v1
	// header (including num_buffers) is 12 bytes, prepended on every frame in
	// both directions.
	netHdrLen = 12

	netRxRing  = 64   // receive buffers kept posted to the device
	netBufSize = 2048 // per-buffer bytes: netHdrLen + max Ethernet frame + slack

	netRxQ = 0 // receiveq1
	netTxQ = 1 // transmitq1
)

// virtioNet is a single virtio-net device on a virtio-mmio slot. It satisfies
// the go-net NetworkDevice interface (Receive/Transmit).
type virtioNet struct {
	dev vioDev
	rxq vioQueue
	txq vioQueue

	mac    []byte // device MAC (nil if the MAC feature was not negotiated)
	rxBufs [][]byte
	txBuf  []byte     // single in-flight transmit buffer
	txMu   sync.Mutex // serializes Transmit across goroutines
}

// netDev is the system network device, discovered by InitNet (kernel/net.go).
var netDev *virtioNet

// NetDevice returns the system virtio-net device, or nil if none was found.
// The returned value satisfies go-net's NetworkDevice (Receive/Transmit).
func NetDevice() *virtioNet { return netDev }

// ProbeNet scans the virtio-mmio slots for a network device and returns an
// initialized driver, or nil if none is present. It records the result for
// NetDevice().
func ProbeNet() *virtioNet {
	for i := 0; i < virtioSlots; i++ {
		dev, ok := vioProbe(i, virtioDevNet)
		if !ok {
			continue
		}
		d := &virtioNet{dev: dev}
		if d.init() {
			netDev = d
			return d
		}
	}
	return nil
}

func (d *virtioNet) init() bool {
	// negotiate VIRTIO_F_VERSION_1 (required) plus MAC if offered.
	neg, ok := d.dev.negotiate(virtioFVersion1 | virtioNetMAC)
	if !ok {
		return false
	}

	if !d.rxq.setup(d.dev, netRxQ, netRxRing) || !d.txq.setup(d.dev, netTxQ, 1) {
		return false
	}

	if neg&virtioNetMAC != 0 {
		d.mac = make([]byte, 6)
		for i := range d.mac {
			d.mac[i] = d.dev.config8(uintptr(i))
		}
	}

	d.txBuf = dmaAlloc(netBufSize, 1)
	d.rxBufs = make([][]byte, d.rxq.qn)
	for i := range d.rxBufs {
		d.rxBufs[i] = dmaAlloc(netBufSize, 1)
		d.rxq.setDesc(i, ptr(d.rxBufs[i]), netBufSize, descWrite, 0)
	}

	d.dev.ready()

	// Post every receive buffer to the device, then kick the receive queue.
	for i := 0; i < int(d.rxq.qn); i++ {
		d.rxq.offer(uint16(i))
	}
	fence()
	d.dev.notify(netRxQ)
	return true
}

// MAC returns the device hardware address, or nil if the device did not
// advertise one (the caller then assigns a random locally-administered MAC).
func (d *virtioNet) MAC() []byte { return d.mac }

// Receive copies one received Ethernet frame (excluding the virtio-net header)
// into buf and recycles its buffer back to the device. It is non-blocking: n=0
// with a nil error means no frame is currently available (the caller polls).
// Only the single go-net receive pump calls Receive, so it needs no lock.
func (d *virtioNet) Receive(buf []byte) (n int, err error) {
	id, length, ok := d.rxq.take()
	if !ok {
		return 0, nil
	}
	if int(length) > netHdrLen {
		n = copy(buf, d.rxBufs[id][netHdrLen:length])
	}
	// Recycle the buffer: its descriptor still points at rxBufs[id], so just
	// re-offer it and let the device fill it again.
	d.rxq.offer(uint16(id))
	fence()
	d.dev.notify(netRxQ)
	return n, nil
}

// Transmit sends one Ethernet frame (without the virtio-net header, which is
// prepended here). It is synchronous: it publishes the frame and polls the used
// ring until the device consumes it. Serialized so concurrent socket writes do
// not interleave on the single transmit buffer/queue.
func (d *virtioNet) Transmit(frame []byte) error {
	if len(frame) > netBufSize-netHdrLen {
		return nil // oversize frame: drop (cannot happen within the MTU)
	}
	d.txMu.Lock()
	defer d.txMu.Unlock()

	for i := 0; i < netHdrLen; i++ {
		d.txBuf[i] = 0
	}
	copy(d.txBuf[netHdrLen:], frame)

	d.txq.setDesc(0, ptr(d.txBuf), uint32(netHdrLen+len(frame)), 0, 0)
	d.txq.offer(0)
	fence()
	d.dev.notify(netTxQ)

	for spins := 0; ; spins++ {
		if _, _, ok := d.txq.take(); ok {
			return nil
		}
		if spins > 1<<24 {
			return nil // device stuck: drop rather than hang the stack
		}
		runtime.Gosched()
	}
}
