//go:build tamago && riscv64

package virtio

import (
	"errors"
	"sync"

	"github.com/splch/honk/internal/mmio"
)

const deviceEntropy = 4 // virtio entropy device (virtio-rng)

// RNG is a virtio-mmio entropy device: a single virtqueue onto which the driver
// posts a device-writable buffer that the host fills with random bytes. Read is
// serialized, so it is safe to use as the system entropy source.
type RNG struct {
	base  uintptr
	q     *vq
	buf   []byte
	bufPA uint64
	mu    sync.Mutex
}

// IsRNG reports whether the mmio slot at base is a virtio entropy device.
func IsRNG(base uintptr) bool {
	return MagicValue(base) == magicValue && DeviceID(base) == deviceEntropy
}

// NewRNG performs the virtio-mmio v2 handshake for the entropy device at base.
func NewRNG(base uintptr) (*RNG, error) {
	if MagicValue(base) != magicValue || mmio.R32(base+regVersion) != 2 || DeviceID(base) != deviceEntropy {
		return nil, errors.New("virtio: not a v2 entropy device")
	}

	r := &RNG{base: base, q: newVQ()}

	mmio.W32(base+regStatus, 0)
	st := uint32(statusAck | statusDriver)
	mmio.W32(base+regStatus, st)
	// Negotiate VIRTIO_F_VERSION_1 (word 1, required for v2 devices, §6.1); the
	// entropy device defines no other feature bits we need.
	mmio.W32(base+regDeviceFeatSel, 1)
	hi := mmio.R32(base + regDeviceFeat)
	mmio.W32(base+regDriverFeatSel, 0)
	mmio.W32(base+regDriverFeat, 0)
	mmio.W32(base+regDriverFeatSel, 1)
	mmio.W32(base+regDriverFeat, hi&featVersion1)
	st |= statusFeaturesOK
	mmio.W32(base+regStatus, st)
	if mmio.R32(base+regStatus)&statusFeaturesOK == 0 {
		return nil, errors.New("virtio-rng: device rejected FEATURES_OK")
	}

	mmio.W32(base+regQueueSel, 0)
	mmio.W32(base+regQueueNum, queueSize)
	mmio.W32(base+regQueueDescLo, uint32(r.q.descPA))
	mmio.W32(base+regQueueDescHi, uint32(r.q.descPA>>32))
	mmio.W32(base+regQueueDrvLo, uint32(r.q.availPA))
	mmio.W32(base+regQueueDrvHi, uint32(r.q.availPA>>32))
	mmio.W32(base+regQueueDevLo, uint32(r.q.usedPA))
	mmio.W32(base+regQueueDevHi, uint32(r.q.usedPA>>32))
	mmio.W32(base+regQueueReady, 1)
	mmio.W32(base+regStatus, st|statusDriverOK)

	buf, pa := dmaPage()
	r.buf, r.bufPA = buf, pa
	return r, nil
}

// Read fills b entirely with entropy from the device. It satisfies io.Reader
// (always returns len(b), nil) and is safe for concurrent callers.
func (r *RNG) Read(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	total := len(b)
	for len(b) > 0 {
		n := len(b)
		if n > pageSize {
			n = pageSize
		}
		r.q.desc[0] = virtqDesc{Addr: r.bufPA, Len: uint32(n), Flags: descWrite}
		r.q.avail.Ring[r.q.avail.Idx%queueSize] = 0
		mmio.Fence()
		r.q.avail.Idx++
		mmio.Fence()
		mmio.W32(r.base+regQueueNotify, 0)

		for mmio.R16(r.q.usedIdxPA) == r.q.lastUsed {
		}
		mmio.Fence()
		got := int(r.q.used.Ring[r.q.lastUsed%queueSize].Len) // bytes the device wrote
		r.q.lastUsed++
		if got <= 0 || got > n {
			got = n
		}
		copy(b, r.buf[:got])
		b = b[got:]
	}
	return total, nil
}
