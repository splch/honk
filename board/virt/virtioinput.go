// honk - QEMU virt board: virtio-input driver (M10).
//
// A focused driver over the shared virtio-mmio v2 transport (virtio.go). It
// surfaces a stream of raw evdev events (type/code/value) from one or more
// virtio-input devices - QEMU's virtio-keyboard and virtio-tablet on the virt
// board - which the kernel translates into honk's UI events.
//
// Unlike the UART (a small FIFO that must be drained on interrupt or it
// overruns), a virtio-input device buffers events in its eventq until the
// driver collects them, so polling is lossless. honk therefore polls it, like
// its other virtio devices (blk/net/9p), and keeps off the nosplit trap path;
// true IRQ-driven wakeup is the same deferred async-I/O item as the rest of
// honk's drivers (it needs the runtime's semawakeup-over-IPI fork). honk is
// identity-mapped, so each event buffer is DMA-addressable at its own address.

//go:build tamago && riscv64

package virt

import "encoding/binary"

const (
	virtioDevInput = 18 // virtio subsystem device ID for input
	inputEventQ    = 0  // eventq (device -> driver); statusq (1) is unused
	inputRing      = 64 // event buffers posted to the device
	inputEventLen  = 8  // sizeof(struct virtio_input_event): type,code,value

	// evdev event types honk forwards (see linux/input-event-codes.h).
	EvSyn = 0x00
	EvKey = 0x01
	EvRel = 0x02
	EvAbs = 0x03
)

// InputEvent is one raw evdev event: an (type, code, value) triple. honk keeps
// the device layer dumb - the kernel maps codes to runes and pointer actions.
type InputEvent struct {
	Type  uint16
	Code  uint16
	Value int32
}

// inputDev is a single virtio-input device's eventq and its posted buffers.
type inputDev struct {
	dev  vioDev
	q    vioQueue
	bufs [][]byte
}

// Input aggregates every virtio-input device found (e.g. a keyboard and a
// tablet) behind one Read loop.
type Input struct {
	devs []*inputDev
}

// ProbeInput scans the virtio-mmio slots for input devices and returns them
// aggregated, or nil if none are present.
func ProbeInput() *Input {
	in := &Input{}
	for i := 0; i < virtioSlots; i++ {
		dev, ok := vioProbe(i, virtioDevInput)
		if !ok {
			continue
		}
		d := &inputDev{dev: dev}
		if d.init() {
			in.devs = append(in.devs, d)
		}
	}
	if len(in.devs) == 0 {
		return nil
	}
	println("honk: input = virtio-input,", len(in.devs), "device(s)")
	return in
}

func (d *inputDev) init() bool {
	if _, ok := d.dev.negotiate(virtioFVersion1); !ok {
		return false
	}
	if !d.q.setup(d.dev, inputEventQ, inputRing) {
		return false
	}
	d.bufs = make([][]byte, d.q.qn)
	for i := range d.bufs {
		d.bufs[i] = dmaAlloc(inputEventLen, 1)
		d.q.setDesc(i, ptr(d.bufs[i]), inputEventLen, descWrite, 0)
	}
	d.dev.ready()

	// Post every event buffer to the device, then kick the eventq.
	for i := 0; i < int(d.q.qn); i++ {
		d.q.offer(uint16(i))
	}
	fence()
	d.dev.notify(inputEventQ)
	return true
}

// Devices reports how many virtio-input devices were found.
func (in *Input) Devices() int { return len(in.devs) }

// Read returns the next pending input event from any device and recycles its
// buffer, or ok=false if none is currently available (the caller polls).
func (in *Input) Read() (ev InputEvent, ok bool) {
	for _, d := range in.devs {
		id, _, got := d.q.take()
		if !got {
			continue
		}
		b := d.bufs[id]
		ev = InputEvent{
			Type:  binary.LittleEndian.Uint16(b[0:]),
			Code:  binary.LittleEndian.Uint16(b[2:]),
			Value: int32(binary.LittleEndian.Uint32(b[4:])),
		}
		d.q.offer(uint16(id))
		fence()
		d.dev.notify(inputEventQ)
		return ev, true
	}
	return InputEvent{}, false
}
