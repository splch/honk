package kv

import (
	"bytes"
	"encoding/binary"
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
	v, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	if string(v) != want {
		t.Fatalf("Get(%q) = %q, want %q", key, v, want)
	}
}

func mustMissing(t *testing.T, s *Store, key string) {
	t.Helper()
	if _, err := s.Get(key); err != ErrNotFound {
		t.Fatalf("Get(%q) err = %v, want ErrNotFound", key, err)
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
	mustMissing(t, s, "ip")
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
	mustMissing(t, s, "k7")
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
	mustMissing(t, s, "c")
}

func TestDurableWrites(t *testing.T) {
	dev := block.NewMemory(64, 512)
	s := open(t, dev)
	defer s.Close()

	// Open formats a fresh device and must flush the superblock durably.
	if dev.Flushes() == 0 {
		t.Fatal("Open did not flush the formatted superblock")
	}
	before := dev.Flushes()
	if err := s.Put("a", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("b", []byte("2")); err != nil {
		t.Fatal(err)
	}
	// Each acknowledged Put is durable: its batch flushed before returning.
	if dev.Flushes() < before+2 {
		t.Fatalf("flushes %d -> %d, want >= +2 (one per Put batch)", before, dev.Flushes())
	}
}

func TestDiskResidentValues(t *testing.T) {
	dev := block.NewMemory(256, 512)
	s := open(t, dev)

	small := []byte("tiny")
	big := bytes.Repeat([]byte("X"), 4096) // > inlineMax => disk-resident
	if err := s.Put("small", small); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("big", big); err != nil {
		t.Fatal(err)
	}

	// small is cached inline; big is a pointer to the log (val == nil).
	m := s.cur.load()
	if m["small"].val == nil {
		t.Fatal("small value should be inline")
	}
	if m["big"].val != nil {
		t.Fatal("big value should be disk-resident (val == nil)")
	}
	if m["big"].block == 0 || int(m["big"].vlen) != len(big) {
		t.Fatalf("big entry = %+v, want a disk pointer with vlen %d", m["big"], len(big))
	}

	// Get round-trips both (reading big from disk); Size needs no read.
	mustGet(t, s, "small", "tiny")
	if v, err := s.Get("big"); err != nil || !bytes.Equal(v, big) {
		t.Fatalf("Get(big): len %d, %v", len(v), err)
	}
	if sz, ok := s.Size("big"); !ok || sz != int64(len(big)) {
		t.Fatalf("Size(big) = %d, %v", sz, ok)
	}
	s.Close()

	// After replay the value stays disk-resident (the pointer is restored, not
	// the value), and still reads back.
	s = open(t, dev)
	defer s.Close()
	if s.cur.load()["big"].val != nil {
		t.Fatal("big should still be disk-resident after replay")
	}
	if v, err := s.Get("big"); err != nil || !bytes.Equal(v, big) {
		t.Fatalf("Get(big) after replay: %v", err)
	}
}

func TestCompactionMovesDiskValues(t *testing.T) {
	// Small device so overwriting a large value forces compaction, which must
	// read the disk-resident value from the old region and rewrite it.
	dev := block.NewMemory(64, 512)
	s := open(t, dev)
	defer s.Close()

	big := bytes.Repeat([]byte("Z"), 2048)
	for i := 0; i < 100; i++ {
		v := append([]byte(fmt.Sprintf("%03d:", i)), big...)
		if err := s.Put("blob", v); err != nil {
			t.Fatalf("put #%d: %v", i, err)
		}
	}
	if s.gen == 0 {
		t.Fatal("expected compaction")
	}
	v, err := s.Get("blob")
	if err != nil || !bytes.HasPrefix(v, []byte("099:")) || len(v) != 4+len(big) {
		t.Fatalf("blob = %d bytes prefix %q, %v", len(v), v[:min(4, len(v))], err)
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

// TestDiskResidentCorruptLength ensures a disk-resident value whose on-disk
// length field is corrupted to an implausible size is treated as stale (the
// location is no longer a valid record for the key) rather than driving a huge
// allocation or an I/O error. The value becomes unreadable, but the store does
// not OOM or panic - the graceful degradation a storage layer owes a bad disk.
func TestDiskResidentCorruptLength(t *testing.T) {
	dev := block.NewMemory(256, 512)
	s := open(t, dev)
	defer s.Close()

	if err := s.Put("big", bytes.Repeat([]byte("Q"), 4096)); err != nil { // disk-resident
		t.Fatal(err)
	}
	e := s.cur.load()["big"]
	if e.val != nil {
		t.Fatal("expected a disk-resident value")
	}

	blk := make([]byte, 512)
	if err := dev.ReadBlocks(e.block, blk); err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(blk[24:], 0xffffffff) // valLen -> ~4 GiB
	if err := dev.WriteBlocks(e.block, blk); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("big"); err != ErrNotFound {
		t.Fatalf("Get with corrupt length: err = %v, want ErrNotFound", err)
	}
}

func TestReset(t *testing.T) {
	dev := block.NewMemory(64, 512)
	s := open(t, dev)
	s.Put("hostname", []byte("honk"))
	s.Put("etc/ip", []byte("10.0.0.1"))
	big := bytes.Repeat([]byte("Z"), 2048) // disk-resident value too
	s.Put("blob", big)

	if err := s.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if s.Len() != 0 {
		t.Fatalf("Len after reset = %d, want 0", s.Len())
	}
	mustMissing(t, s, "hostname")
	mustMissing(t, s, "blob")

	// The store is usable after reset, and the empty state persists across a
	// reopen (replay must not resurrect the pre-reset records).
	if err := s.Put("fresh", []byte("start")); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s = open(t, dev)
	defer s.Close()
	mustGet(t, s, "fresh", "start")
	mustMissing(t, s, "hostname")
	mustMissing(t, s, "etc/ip")
	mustMissing(t, s, "blob")
	if s.Len() != 1 {
		t.Fatalf("Len after reset+reopen = %d, want 1", s.Len())
	}
}

// TestCompactionLeftoverNotReplayed guards the crash-consistency invariant that
// replay must not read stale records left in a region's tail from a previous
// life. Overwriting one key forces alternating compactions; ending on a SHORT
// final region leaves valid older records beyond the true log end. A correct
// store recovers only the live value.
func TestCompactionLeftoverNotReplayed(t *testing.T) {
	dev := block.NewMemory(16, 512) // regionLen = (16-2)/2 = 7
	s := open(t, dev)
	for i := 0; i <= 14; i++ { // enough overwrites to reuse both regions twice
		if err := s.Put("k", []byte(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	s = open(t, dev) // replay
	defer s.Close()
	mustGet(t, s, "k", "v14")
	if s.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (stale leftover records resurrected on replay)", s.Len())
	}
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
			v, err := s.Get(fmt.Sprintf("w%d-k%d", w, k))
			if err != nil || !bytes.Equal(v, []byte(fmt.Sprintf("%d", last))) {
				t.Fatalf("w%d-k%d = %q (err=%v), want %d", w, k, v, err, last)
			}
		}
	}
}
