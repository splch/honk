package ring

import (
	"sync"
	"testing"
)

func TestPushPopOrder(t *testing.T) {
	r := New(4)
	for i := byte(0); i < 4; i++ {
		if !r.Push(i) {
			t.Fatalf("Push(%d) failed early", i)
		}
	}
	if r.Push(99) { // full (capacity 4)
		t.Fatal("Push succeeded on a full ring")
	}
	for i := byte(0); i < 4; i++ {
		b, ok := r.Pop()
		if !ok || b != i {
			t.Fatalf("Pop() = %d, %v; want %d, true", b, ok, i)
		}
	}
	if _, ok := r.Pop(); ok {
		t.Fatal("Pop succeeded on an empty ring")
	}
}

func TestPanicsOnBadSize(t *testing.T) {
	for _, n := range []int{0, 3, 6, -2} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("New(%d) did not panic", n)
				}
			}()
			New(n)
		}()
	}
}

// TestSPSC exercises the single-producer/single-consumer contract under -race:
// the consumer must observe every byte exactly once, in order.
func TestSPSC(t *testing.T) {
	const n = 100_000
	r := New(64)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			for !r.Push(byte(i)) {
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			var b byte
			var ok bool
			for !ok {
				b, ok = r.Pop()
			}
			if b != byte(i) {
				t.Errorf("at %d: got %d, want %d", i, b, byte(i))
				return
			}
		}
	}()
	wg.Wait()
}
