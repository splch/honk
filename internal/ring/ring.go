// Package ring is a fixed-capacity, lock-free single-producer/single-consumer
// byte queue. honk uses it to hand UART input from the interrupt-drain path
// (producer, in the idle hook) to the console goroutine (consumer). It is
// hardware-independent and unit-tested on the host (GO.md §16).
package ring

import "sync/atomic"

// Ring is an SPSC byte queue. The zero value is unusable; call New.
type Ring struct {
	buf  []byte
	mask uint32
	head atomic.Uint32 // total bytes pushed (producer)
	tail atomic.Uint32 // total bytes popped (consumer)
}

// New returns a ring holding up to size bytes. size must be a power of two.
func New(size int) *Ring {
	if size <= 0 || size&(size-1) != 0 {
		panic("ring: size must be a power of two")
	}
	return &Ring{buf: make([]byte, size), mask: uint32(size - 1)}
}

// Push appends b, returning false if the ring is full. Safe for a single
// producer to call concurrently with a single consumer's Pop.
func (r *Ring) Push(b byte) bool {
	h := r.head.Load()
	if h-r.tail.Load() > r.mask { // capacity == mask+1
		return false
	}
	r.buf[h&r.mask] = b
	r.head.Store(h + 1) // publish only after the write
	return true
}

// Pop removes and returns the oldest byte, or ok=false if empty.
func (r *Ring) Pop() (b byte, ok bool) {
	t := r.tail.Load()
	if t == r.head.Load() {
		return 0, false
	}
	b = r.buf[t&r.mask]
	r.tail.Store(t + 1)
	return b, true
}
