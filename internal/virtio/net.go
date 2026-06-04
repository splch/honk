//go:build tamago && riscv64

package virtio

import (
	"errors"
	"unsafe"

	"github.com/splch/honk/internal/mmio"
)

const (
	deviceNet  = 1
	netFeatMAC = 1 << 5 // VIRTIO_NET_F_MAC

	// virtio-net header prepended to every frame. honk negotiates
	// VIRTIO_F_VERSION_1 (modern mode), in which struct virtio_net_hdr always
	// includes the num_buffers field and is therefore 12 bytes, even though
	// VIRTIO_NET_F_MRG_RXBUF is not negotiated (VIRTIO spec §5.1.6.1; in the older
	// transitional mode without that feature the header was 2 bytes shorter).
	netHdrLen    = 12
	frameBufSize = 2048 // RX buffer: net header + a full Ethernet frame
)

// vq is one split virtqueue (descriptors + avail/used rings) in DMA memory.
type vq struct {
	desc                    *[queueSize]virtqDesc
	avail                   *virtqAvail
	used                    *virtqUsed
	descPA, availPA, usedPA uint64
	usedIdxPA               uintptr
	lastUsed                uint16
}

func newVQ() *vq {
	q, pa := dmaPage()
	return &vq{
		desc:      (*[queueSize]virtqDesc)(unsafe.Pointer(&q[0])),
		avail:     (*virtqAvail)(unsafe.Pointer(&q[256])),
		used:      (*virtqUsed)(unsafe.Pointer(&q[512])),
		descPA:    pa,
		availPA:   pa + 256,
		usedPA:    pa + 512,
		usedIdxPA: uintptr(pa) + 512 + 2, // &used.Idx
	}
}

// Net is a virtio-mmio network device: two queues (RX = queue 0,
// TX = queue 1), each frame carrying a 12-byte virtio-net header. Polled,
// single-threaded; methods are not safe for concurrent use.
type Net struct {
	base   uintptr
	rx, tx *vq

	rxBuf   [queueSize][]byte // RX buffers, one per descriptor
	rxBufPA [queueSize]uint64
	txBuf   []byte
	txBufPA uint64

	mac [6]byte
}

// IsNet reports whether the mmio slot at base is a virtio network device.
func IsNet(base uintptr) bool {
	return MagicValue(base) == magicValue && DeviceID(base) == deviceNet
}

// NewNet performs the virtio-mmio v2 handshake for the network device at base,
// sets up the RX/TX queues, pre-posts the RX buffers, and reads the MAC.
func NewNet(base uintptr) (*Net, error) {
	if MagicValue(base) != magicValue || mmio.R32(base+regVersion) != 2 || DeviceID(base) != deviceNet {
		return nil, errors.New("virtio: not a v2 network device")
	}

	n := &Net{base: base, rx: newVQ(), tx: newVQ()}

	mmio.W32(base+regStatus, 0)
	st := uint32(statusAck | statusDriver)
	mmio.W32(base+regStatus, st)

	// Negotiate VIRTIO_NET_F_MAC (word 0, read the address from config space) and
	// VIRTIO_F_VERSION_1 (word 1, required for v2 devices, §6.1).
	mmio.W32(base+regDeviceFeatSel, 0)
	dev := mmio.R32(base + regDeviceFeat)
	mmio.W32(base+regDeviceFeatSel, 1)
	devHi := mmio.R32(base + regDeviceFeat)
	mmio.W32(base+regDriverFeatSel, 0)
	mmio.W32(base+regDriverFeat, dev&netFeatMAC)
	mmio.W32(base+regDriverFeatSel, 1)
	mmio.W32(base+regDriverFeat, devHi&featVersion1)
	st |= statusFeaturesOK
	mmio.W32(base+regStatus, st)
	if mmio.R32(base+regStatus)&statusFeaturesOK == 0 {
		return nil, errors.New("virtio-net: FEATURES_OK rejected")
	}

	n.setupQueue(0, n.rx)
	n.setupQueue(1, n.tx)

	// RX: give every descriptor a device-writable buffer and publish them all.
	for i := 0; i < queueSize; i++ {
		buf, pa := dmaPage()
		n.rxBuf[i], n.rxBufPA[i] = buf[:frameBufSize], pa
		n.rx.desc[i] = virtqDesc{Addr: pa, Len: frameBufSize, Flags: descWrite}
		n.rx.avail.Ring[i] = uint16(i)
	}
	mmio.Fence()
	n.rx.avail.Idx = queueSize
	mmio.Fence()

	tb, tpa := dmaPage()
	n.txBuf, n.txBufPA = tb, tpa

	mmio.W32(base+regStatus, st|statusDriverOK)
	mmio.W32(base+regQueueNotify, 0) // make the posted RX buffers available

	for i := 0; i < 6; i++ {
		n.mac[i] = mmio.R8(base + regConfig + uintptr(i))
	}
	return n, nil
}

// MAC returns the device's 6-byte hardware address; callers wrap it in
// net.HardwareAddr to format or hand to the stack.
func (n *Net) MAC() [6]byte { return n.mac }

func (n *Net) setupQueue(idx uint32, q *vq) {
	b := n.base
	mmio.W32(b+regQueueSel, idx)
	mmio.W32(b+regQueueNum, queueSize)
	mmio.W32(b+regQueueDescLo, uint32(q.descPA))
	mmio.W32(b+regQueueDescHi, uint32(q.descPA>>32))
	mmio.W32(b+regQueueDrvLo, uint32(q.availPA))
	mmio.W32(b+regQueueDrvHi, uint32(q.availPA>>32))
	mmio.W32(b+regQueueDevLo, uint32(q.usedPA))
	mmio.W32(b+regQueueDevHi, uint32(q.usedPA>>32))
	mmio.W32(b+regQueueReady, 1)
}

// Send transmits one Ethernet frame (a 12-byte zero virtio-net header is
// prepended). It blocks until the device consumes the descriptor.
func (n *Net) Send(frame []byte) {
	for i := 0; i < netHdrLen; i++ {
		n.txBuf[i] = 0
	}
	copy(n.txBuf[netHdrLen:], frame)
	n.tx.desc[0] = virtqDesc{Addr: n.txBufPA, Len: uint32(netHdrLen + len(frame))}
	n.tx.avail.Ring[n.tx.avail.Idx%queueSize] = 0
	mmio.Fence()
	n.tx.avail.Idx++
	mmio.Fence()
	mmio.W32(n.base+regQueueNotify, 1)
	for mmio.R16(n.tx.usedIdxPA) == n.tx.lastUsed {
	}
	n.tx.lastUsed++
}

// Recv returns the next received Ethernet frame (virtio-net header stripped), or
// ok=false if none is pending. The underlying buffer is re-posted to the device.
func (n *Net) Recv() (frame []byte, ok bool) {
	if mmio.R16(n.rx.usedIdxPA) == n.rx.lastUsed {
		return nil, false
	}
	mmio.Fence()
	e := n.rx.used.Ring[n.rx.lastUsed%queueSize]
	n.rx.lastUsed++
	id := e.ID
	if e.Len > netHdrLen {
		frame = append([]byte(nil), n.rxBuf[id][netHdrLen:e.Len]...)
		ok = true
	}
	// Re-post the buffer.
	n.rx.avail.Ring[n.rx.avail.Idx%queueSize] = uint16(id)
	mmio.Fence()
	n.rx.avail.Idx++
	mmio.Fence()
	mmio.W32(n.base+regQueueNotify, 0)
	return frame, ok
}
