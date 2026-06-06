package kv

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"honk/block"
)

// crashDevice models power-loss semantics: writes are volatile until Flush,
// which commits them to the durable image. snapshot() returns the durable image
// (what survives a power loss) as an independent device. It is the harness for
// honk's central storage invariant - acked data is durable, and recovery is
// always to a consistent state.
type crashDevice struct {
	bs      int
	durable []byte // survives a crash (last Flush)
	live    []byte // durable + writes since the last Flush
}

func newCrashDevice(blocks int64, bs int) *crashDevice {
	n := blocks * int64(bs)
	return &crashDevice{bs: bs, durable: make([]byte, n), live: make([]byte, n)}
}

func (d *crashDevice) BlockSize() int { return d.bs }
func (d *crashDevice) Blocks() int64  { return int64(len(d.live)) / int64(d.bs) }

func (d *crashDevice) check(start int64, p []byte) error {
	if len(p) == 0 || len(p)%d.bs != 0 {
		return block.ErrAlign
	}
	if start < 0 || start+int64(len(p)/d.bs) > d.Blocks() {
		return block.ErrRange
	}
	return nil
}

func (d *crashDevice) ReadBlocks(start int64, p []byte) error {
	if err := d.check(start, p); err != nil {
		return err
	}
	copy(p, d.live[start*int64(d.bs):])
	return nil
}

func (d *crashDevice) WriteBlocks(start int64, p []byte) error {
	if err := d.check(start, p); err != nil {
		return err
	}
	copy(d.live[start*int64(d.bs):], p)
	return nil
}

// Flush commits volatile writes to the durable image.
func (d *crashDevice) Flush() error {
	copy(d.durable, d.live)
	return nil
}

// snapshot returns the durable image as a fresh device - the state a power loss
// at this instant would leave behind.
func (d *crashDevice) snapshot() *crashDevice {
	nd := newCrashDevice(d.Blocks(), d.bs)
	copy(nd.durable, d.durable)
	copy(nd.live, d.durable)
	return nd
}

// TestCrashConsistency drives a long randomized sequence of Put/Delete/Reset
// (with both inline and disk-resident values, on a device small enough to force
// many compactions) and, after EVERY acknowledged operation, simulates a power
// loss and reopens the store from the durable image. The recovered store must
// match the model exactly: all acked data present and correct, nothing stale
// resurrected, nothing lost, no corruption. This is the invariant the rest of
// honk is built on.
func TestCrashConsistency(t *testing.T) {
	const blocks, bs = 48, 512 // regionLen = (48-2)/2 = 23 blocks; forces compaction
	dev := newCrashDevice(blocks, bs)
	s := open(t, dev)
	defer s.Close()

	model := map[string][]byte{}
	keys := []string{"a", "b", "c", "host", "ip", "big1", "big2", "deep/x", "deep/y"}
	rng := rand.New(rand.NewSource(20260606))

	for i := 0; i < 600; i++ {
		k := keys[rng.Intn(len(keys))]
		switch rng.Intn(12) {
		case 0, 1: // delete
			if err := s.Delete(k); err != nil {
				t.Fatalf("op %d Delete(%q): %v", i, k, err)
			}
			delete(model, k)
		case 2: // stateless reset
			if err := s.Reset(); err != nil {
				t.Fatalf("op %d Reset: %v", i, err)
			}
			model = map[string][]byte{}
		default: // put (big keys spill to disk; small ones stay inline)
			var v []byte
			if strings.HasPrefix(k, "big") {
				v = bytes.Repeat([]byte{byte(i), byte(i >> 8)}, 800) // 1600 B > inlineMax
			} else {
				v = []byte(fmt.Sprintf("%s=%d", k, i))
			}
			if err := s.Put(k, v); err != nil {
				t.Fatalf("op %d Put(%q): %v", i, k, err)
			}
			model[k] = v
		}
		// The operation is acked, hence durable: a power loss now must recover
		// exactly to the model.
		recoverAndCheck(t, i, dev.snapshot(), model)
	}
}

func recoverAndCheck(t *testing.T, op int, dev block.Device, model map[string][]byte) {
	t.Helper()
	s, err := Open(dev)
	if err != nil {
		t.Fatalf("op %d: reopen after crash: %v", op, err)
	}
	defer s.Close()

	if s.Len() != len(model) {
		t.Fatalf("op %d: recovered %d keys, want %d", op, s.Len(), len(model))
	}
	for k, want := range model {
		got, err := s.Get(k)
		if err != nil {
			t.Fatalf("op %d: recovered Get(%q): %v", op, k, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("op %d: recovered %q = %q, want %q", op, k, got, want)
		}
	}
	for _, k := range s.Keys() { // no stale key resurrected
		if _, ok := model[k]; !ok {
			t.Fatalf("op %d: recovered stale key %q not in model", op, k)
		}
	}
}
