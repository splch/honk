package kv

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"honk/block"
)

func open(t *testing.T, dev block.Device) *Store {
	t.Helper()
	s, err := Open(dev)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func mustGet(t *testing.T, s *Store, key, want string) {
	t.Helper()
	v, ok := s.Get(key)
	if !ok {
		t.Fatalf("Get(%q): missing", key)
	}
	if string(v) != want {
		t.Fatalf("Get(%q) = %q, want %q", key, v, want)
	}
}

func TestPutGetDelete(t *testing.T) {
	s := open(t, block.NewMemory(64, 512))
	defer s.Close()

	if err := s.Put("hostname", []byte("honk")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("ip", []byte("10.0.0.1")); err != nil {
		t.Fatal(err)
	}
	mustGet(t, s, "hostname", "honk")
	mustGet(t, s, "ip", "10.0.0.1")

	if err := s.Delete("ip"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("ip"); ok {
		t.Fatal("ip present after Delete")
	}
	if s.Len() != 1 {
		t.Fatalf("Len = %d, want 1", s.Len())
	}
}

func TestReplayAcrossReopen(t *testing.T) {
	dev := block.NewMemory(64, 512)

	s := open(t, dev)
	for i := 0; i < 20; i++ {
		if err := s.Put(fmt.Sprintf("k%d", i), []byte(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	s.Put("k5", []byte("updated")) // overwrite
	s.Delete("k7")
	s.Close()

	s = open(t, dev) // replay
	defer s.Close()
	mustGet(t, s, "k0", "v0")
	mustGet(t, s, "k5", "updated")
	mustGet(t, s, "k19", "v19")
	if _, ok := s.Get("k7"); ok {
		t.Fatal("k7 present after delete + replay")
	}
	if s.Len() != 19 {
		t.Fatalf("Len = %d, want 19", s.Len())
	}
}

func TestTornTailDiscarded(t *testing.T) {
	dev := block.NewMemory(64, 512)
	s := open(t, dev)
	// Small values => one block per record; region A starts at block sbSlots.
	s.Put("a", []byte("1"))
	s.Put("b", []byte("2"))
	s.Put("c", []byte("3"))
	s.Close()

	// Corrupt the third record's block: replay must stop there, keeping a,b.
	garbage := make([]byte, 512)
	for i := range garbage {
		garbage[i] = 0xee
	}
	if err := dev.WriteBlocks(sbSlots+2, garbage); err != nil {
		t.Fatal(err)
	}

	s = open(t, dev)
	defer s.Close()
	mustGet(t, s, "a", "1")
	mustGet(t, s, "b", "2")
	if _, ok := s.Get("c"); ok {
		t.Fatal("torn record c survived replay")
	}
}

func TestCompaction(t *testing.T) {
	// Tiny device so the active region fills quickly and forces a checkpoint.
	dev := block.NewMemory(16, 512)
	s := open(t, dev)

	s.Put("a", []byte("A"))
	s.Put("b", []byte("B"))
	// Many overwrites of one key: the log fills with stale records, so
	// compaction must reclaim space while preserving the live set.
	for i := 0; i < 200; i++ {
		if err := s.Put("counter", []byte(fmt.Sprintf("%d", i))); err != nil {
			t.Fatalf("Put #%d: %v", i, err)
		}
	}
	mustGet(t, s, "counter", "199")
	mustGet(t, s, "a", "A")
	mustGet(t, s, "b", "B")
	if s.gen == 0 {
		t.Fatal("expected at least one compaction (gen still 0)")
	}
	s.Close()

	s = open(t, dev) // replay a compacted store
	defer s.Close()
	mustGet(t, s, "counter", "199")
	mustGet(t, s, "a", "A")
	mustGet(t, s, "b", "B")
}

func TestConcurrent(t *testing.T) {
	s := open(t, block.NewMemory(256, 512))
	defer s.Close()

	const writers = 8
	const each = 100
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				key := fmt.Sprintf("w%d-k%d", w, i%8)
				if err := s.Put(key, []byte(fmt.Sprintf("%d", i))); err != nil {
					t.Errorf("Put: %v", err)
					return
				}
				s.Get(key)
				s.Keys()
			}
		}(w)
	}
	wg.Wait()

	// Each writer's last write to each of its 8 keys should be the highest i
	// with i%8 == k, i.e. for each (w,k) the value is the largest i<each.
	for w := 0; w < writers; w++ {
		for k := 0; k < 8; k++ {
			var last int
			for i := 0; i < each; i++ {
				if i%8 == k {
					last = i
				}
			}
			v, ok := s.Get(fmt.Sprintf("w%d-k%d", w, k))
			if !ok || !bytes.Equal(v, []byte(fmt.Sprintf("%d", last))) {
				t.Fatalf("w%d-k%d = %q (ok=%v), want %d", w, k, v, ok, last)
			}
		}
	}
}
