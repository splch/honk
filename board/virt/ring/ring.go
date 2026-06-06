// Package ring is a fixed-capacity, single-producer/single-consumer (SPSC)
// lock-free byte ring.
//
// honk uses it for the M1 console input path (HONK.md §1: "a goroutine blocks
// on a channel the IRQ handler signals"): the UART receive interrupt handler is
// the single producer and pushes bytes from trap context, while a reader
// goroutine is the single consumer and pops them onto the console channel. The
// same shape serves every IRQ->channel device path on the roadmap (NVMe,
// virtio-input/net/gpu), so the primitive is shared and tested once here.
//
// It is pure Go with no bare-metal dependency, so its correctness - FIFO order,
// index wraparound, drop-on-full backpressure, and the producer/consumer
// happens-before relationship - is exercised host-side under `go test -race`,
// which the bare-metal trap path could never reach reliably.
//
// Concurrency contract: at most one goroutine may call Push and at most one
// (other) goroutine may call Pop, concurrently. Any other sharing - two
// producers, two consumers - is a data race and unsupported. Push and Pop use
// only free-function atomic loads/stores on the head/tail indices (no locks),
// which is why Push is safe to call from an interrupt handler.
package ring

import "sync/atomic"

// Byte is the SPSC byte ring. The zero value is not usable; obtain one from New.
//
// Indices are free-running uint32 counters (never reduced modulo the capacity);
// the live count is tail-head and the storage slot is index&mask. Free-running
// counters distinguish a full ring (count == capacity) from an empty one
// (head == tail) without wasting a slot, and the uint32 subtraction stays
// correct across wrap because the count never exceeds the capacity.
type Byte struct {
	buf  []byte
	mask uint32

	// head and tail are accessed only with sync/atomic. head is advanced by the
	// consumer (Pop), tail by the producer (Push); each side reads the other's
	// index atomically to decide empty/full.
	head uint32 // next index to read
	tail uint32 // next index to write
}

// New returns an empty ring that holds up to capacity bytes. capacity must be a
// power of two (so the index&mask slot computation is exact); New panics
// otherwise. The console uses 1024; tests use small powers of two to exercise
// wraparound and the full condition cheaply.
func New(capacity int) *Byte {
	if capacity <= 0 || capacity&(capacity-1) != 0 {
		panic("ring: capacity must be a power of two")
	}
	return &Byte{buf: make([]byte, capacity), mask: uint32(capacity - 1)}
}

// Push appends b and returns true, or returns false (dropping b) if the ring is
// full. It is the producer side: safe to call from a single producer, including
// an interrupt handler. It is //go:nosplit and free of allocation and floating
// point so it is safe on honk's trap stack (board/virt trap.go).
//
//go:nosplit
func (r *Byte) Push(b byte) bool {
	t := atomic.LoadUint32(&r.tail)
	if t-atomic.LoadUint32(&r.head) >= uint32(len(r.buf)) {
		return false // full: drop the byte rather than overwrite unread input
	}
	r.buf[t&r.mask] = b
	atomic.StoreUint32(&r.tail, t+1) // release: publish the byte before the index
	return true
}

// Pop removes and returns the oldest byte; ok is false if the ring is empty. It
// is the consumer side: safe to call from a single consumer concurrently with
// Push.
func (r *Byte) Pop() (b byte, ok bool) {
	h := atomic.LoadUint32(&r.head)
	if h == atomic.LoadUint32(&r.tail) {
		return 0, false
	}
	b = r.buf[h&r.mask]
	atomic.StoreUint32(&r.head, h+1)
	return b, true
}

// Len returns the number of buffered bytes at the moment of the call. It is a
// racy snapshot under concurrent Push/Pop (useful for tests and diagnostics).
func (r *Byte) Len() int {
	return int(atomic.LoadUint32(&r.tail) - atomic.LoadUint32(&r.head))
}

// Cap returns the capacity in bytes.
func (r *Byte) Cap() int { return len(r.buf) }
