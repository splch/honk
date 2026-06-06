package block

// Slice returns a Device that views count blocks of parent starting at block
// start, with its own block addressing rebased to 0. It is honk's on-disk
// partitioning primitive: the immutable image A/B slots and the writable kv
// region are all Slices of one physical device, so neither layer needs to know
// the others' geometry (HONK.md §2 storage).
//
// A Slice shares the parent's block size and forwards Flush to it (a flush is a
// whole-device cache barrier, not a per-range one). Out-of-range requests fail
// with ErrRange exactly as the parent would.
func Slice(parent Device, start, count int64) Device {
	return &slice{parent: parent, start: start, count: count}
}

type slice struct {
	parent Device
	start  int64
	count  int64
}

func (s *slice) BlockSize() int { return s.parent.BlockSize() }
func (s *slice) Blocks() int64  { return s.count }

func (s *slice) ReadBlocks(start int64, p []byte) error {
	if err := s.check(start, p); err != nil {
		return err
	}
	return s.parent.ReadBlocks(s.start+start, p)
}

func (s *slice) WriteBlocks(start int64, p []byte) error {
	if err := s.check(start, p); err != nil {
		return err
	}
	return s.parent.WriteBlocks(s.start+start, p)
}

func (s *slice) Flush() error { return s.parent.Flush() }

func (s *slice) check(start int64, p []byte) error {
	bs := s.parent.BlockSize()
	if len(p) == 0 || len(p)%bs != 0 {
		return ErrAlign
	}
	if start < 0 || start+int64(len(p)/bs) > s.count {
		return ErrRange
	}
	return nil
}
