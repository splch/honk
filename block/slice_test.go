package block

import (
	"bytes"
	"testing"
)

func TestSliceIsolation(t *testing.T) {
	parent := NewMemory(100, 512)
	// A slice of 10 blocks starting at block 20.
	s := Slice(parent, 20, 10)

	if s.BlockSize() != 512 {
		t.Fatalf("BlockSize = %d", s.BlockSize())
	}
	if s.Blocks() != 10 {
		t.Fatalf("Blocks = %d, want 10", s.Blocks())
	}

	// Write through the slice; it must land at the rebased parent offset.
	w := bytes.Repeat([]byte{0xab}, 512)
	if err := s.WriteBlocks(0, w); err != nil {
		t.Fatal(err)
	}
	r := make([]byte, 512)
	if err := parent.ReadBlocks(20, r); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r, w) {
		t.Fatal("slice block 0 did not map to parent block 20")
	}

	// The slice cannot reach beyond its window.
	if err := s.WriteBlocks(10, make([]byte, 512)); err != ErrRange {
		t.Fatalf("out-of-range write err = %v, want ErrRange", err)
	}
	if err := s.ReadBlocks(9, make([]byte, 1024)); err != ErrRange {
		t.Fatalf("straddling read err = %v, want ErrRange", err)
	}

	// Block before the slice is untouched (isolation).
	if err := parent.ReadBlocks(19, r); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r, make([]byte, 512)) {
		t.Fatal("parent block before slice was modified")
	}
}

func TestSliceFlushDelegates(t *testing.T) {
	parent := NewMemory(64, 512)
	s := Slice(parent, 8, 8)
	before := parent.Flushes()
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if parent.Flushes() != before+1 {
		t.Fatal("slice Flush did not delegate to parent")
	}
}
