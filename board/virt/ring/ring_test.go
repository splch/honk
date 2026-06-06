package ring

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestNewRejectsBadCapacity(t *testing.T) {
	for _, bad := range []int{0, -1, -8, 3, 5, 6, 7, 100, 1000} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("New(%d) did not panic (capacity must be a power of two)", bad)
				}
			}()
			New(bad)
		}()
	}
	for _, ok := range []int{1, 2, 4, 8, 1024} {
		r := New(ok)
		if r.Cap() != ok {
			t.Fatalf("New(%d).Cap() = %d", ok, r.Cap())
		}
	}
}

func TestFIFOOrder(t *testing.T) {
	r := New(8)
	if _, ok := r.Pop(); ok {
		t.Fatal("Pop on empty ring returned ok=true")
	}
	for i := byte(0); i < 6; i++ {
		if !r.Push(i) {
			t.Fatalf("Push(%d) returned false on a non-full ring", i)
		}
	}
	if r.Len() != 6 {
		t.Fatalf("Len = %d, want 6", r.Len())
	}
	for want := byte(0); want < 6; want++ {
		got, ok := r.Pop()
		if !ok || got != want {
			t.Fatalf("Pop = %d, %v; want %d, true", got, ok, want)
		}
	}
	if _, ok := r.Pop(); ok {
		t.Fatal("Pop after draining returned ok=true")
	}
	if r.Len() != 0 {
		t.Fatalf("Len after drain = %d, want 0", r.Len())
	}
}

func TestDropOnFull(t *testing.T) {
	r := New(4)
	for i := byte(0); i < 4; i++ {
		if !r.Push(i) {
			t.Fatalf("Push(%d) on a ring with room returned false", i)
		}
	}
	if r.Len() != 4 || r.Len() != r.Cap() {
		t.Fatalf("Len = %d, want 4 (== Cap)", r.Len())
	}
	// Full: further pushes are dropped (the input byte is lost, not overwriting
	// unread data) and report false.
	if r.Push(99) {
		t.Fatal("Push on a full ring returned true (must drop and return false)")
	}
	// Make room, then a push succeeds again; the dropped byte (99) is NOT
	// resurrected - the oldest live byte is still 0.
	got, ok := r.Pop()
	if !ok || got != 0 {
		t.Fatalf("Pop = %d, %v; want 0, true", got, ok)
	}
	if !r.Push(100) {
		t.Fatal("Push after making room returned false")
	}
	want := []byte{1, 2, 3, 100}
	for _, w := range want {
		got, ok := r.Pop()
		if !ok || got != w {
			t.Fatalf("Pop = %d, %v; want %d, true", got, ok, w)
		}
	}
}

// TestWraparound exercises the index&mask slot computation and the free-running
// counters: repeatedly filling, partially draining, and refilling drives the
// head/tail indices far past the capacity, so any off-by-one in the masking or
// the full/empty test surfaces as a wrong byte or a wrong length.
func TestWraparound(t *testing.T) {
	r := New(4)
	next := byte(0) // next value to push
	exp := byte(0)  // next value expected from Pop
	live := 0
	for round := 0; round < 1000; round++ {
		// push up to 3
		for i := 0; i < 3; i++ {
			if r.Push(next) {
				if live >= r.Cap() {
					t.Fatalf("round %d: Push succeeded past capacity (live=%d)", round, live)
				}
				next++
				live++
			} else if live != r.Cap() {
				t.Fatalf("round %d: Push dropped while not full (live=%d)", round, live)
			}
		}
		// pop up to 2
		for i := 0; i < 2; i++ {
			got, ok := r.Pop()
			if ok {
				if got != exp {
					t.Fatalf("round %d: Pop = %d, want %d (FIFO violated across wrap)", round, got, exp)
				}
				exp++
				live--
			} else if live != 0 {
				t.Fatalf("round %d: Pop empty while live=%d", round, live)
			}
		}
		if r.Len() != live {
			t.Fatalf("round %d: Len = %d, want %d", round, r.Len(), live)
		}
	}
}

// TestSPSCConcurrent is the test that matters for the trap path: one producer
// goroutine (the stand-in for the IRQ handler) pushes a long byte sequence,
// retrying on full; one consumer goroutine (the reader goroutine) pops it. The
// consumer must observe every value exactly once and in order, with no data
// race. Run under -race, this validates the producer/consumer happens-before
// that the lock-free indices rely on - the property honk's console correctness
// depends on. The small ring guarantees the full condition (backpressure) and
// many wraparounds are hit.
func TestSPSCConcurrent(t *testing.T) {
	const n = 200000
	r := New(8) // tiny: forces frequent full/empty transitions and wraps

	var wg sync.WaitGroup
	wg.Add(2)

	go func() { // single producer
		defer wg.Done()
		for i := 0; i < n; i++ {
			for !r.Push(byte(i)) {
				runtime.Gosched() // ring full: yield to the consumer
			}
		}
	}()

	got := 0
	go func() { // single consumer
		defer wg.Done()
		for got < n {
			b, ok := r.Pop()
			if !ok {
				runtime.Gosched() // ring empty: yield to the producer
				continue
			}
			if b != byte(got) {
				t.Errorf("at %d: Pop = %d, want %d (order or loss)", got, b, byte(got))
				return
			}
			got++
		}
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("SPSC test deadlocked at %d/%d (backpressure/wakeup bug?)", got, n)
	}
	if got != n {
		t.Fatalf("consumed %d, want %d", got, n)
	}
}
