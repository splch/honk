package kv

import (
	"encoding/binary"
	"hash/crc32"
	"sync/atomic"
)

// indexPtr publishes the immutable index for lock-free readers.
type indexPtr struct{ p atomic.Pointer[index] }

func (ip *indexPtr) load() index {
	if m := ip.p.Load(); m != nil {
		return *m
	}
	return index{}
}

func (ip *indexPtr) store(m index) { ip.p.Store(&m) }

// recover reads the superblocks, selects the active region (highest valid
// generation), and replays it - or formats a fresh device.
func (s *Store) recover() (index, error) {
	sb := make([]byte, s.bs)
	active, gen, found := 0, uint64(0), false

	for slot := int64(0); slot < sbSlots; slot++ {
		if err := s.dev.ReadBlocks(slot, sb); err != nil {
			return nil, err
		}
		if binary.LittleEndian.Uint32(sb[0:]) != sbMagic {
			continue
		}
		if binary.LittleEndian.Uint32(sb[4:]) != crc32.ChecksumIEEE(sb[8:17]) {
			continue
		}
		if g := binary.LittleEndian.Uint64(sb[8:]); !found || g > gen {
			active, gen, found = int(sb[16]), g, true
		}
	}

	if !found {
		// Fresh (or unrecognized) device: format region A as empty.
		s.active, s.gen, s.head, s.seq = 0, 0, 0, 0
		if err := s.writeSuper(0, 0); err != nil {
			return nil, err
		}
		return index{}, nil
	}

	s.active, s.gen = active, gen
	return s.replay()
}

// replay walks the active region's log, applying valid records and stopping at
// the first torn or absent one - which is where the next append will go.
func (s *Store) replay() (index, error) {
	idx := index{}
	start := s.regionStart[s.active]
	hdr := make([]byte, s.bs)

	var off int64
	for off < s.regionLen {
		if err := s.dev.ReadBlocks(start+off, hdr); err != nil {
			return nil, err
		}
		if binary.LittleEndian.Uint32(hdr[0:]) != recMagic {
			break // end of log
		}
		keyLen := int(binary.LittleEndian.Uint32(hdr[20:]))
		valLen := int(binary.LittleEndian.Uint32(hdr[24:]))
		n := recHeader + keyLen + valLen
		if keyLen < 0 || valLen < 0 || n < recHeader {
			break // bogus lengths
		}
		blocks := int64((n + s.bs - 1) / s.bs)
		if blocks > s.regionLen-off {
			break // would run past the region
		}

		rec := hdr
		if blocks > 1 {
			rec = make([]byte, blocks*int64(s.bs))
			if err := s.dev.ReadBlocks(start+off, rec); err != nil {
				return nil, err
			}
		}
		if binary.LittleEndian.Uint32(rec[4:]) != crc32.ChecksumIEEE(rec[8:n]) {
			break // torn tail
		}

		if seq := binary.LittleEndian.Uint64(rec[8:]); seq > s.seq {
			s.seq = seq
		}
		key := string(rec[recHeader : recHeader+keyLen])
		if rec[16] == opPut {
			if valLen <= inlineMax {
				v := make([]byte, valLen)
				copy(v, rec[recHeader+keyLen:n])
				idx[key] = entry{val: v}
			} else {
				// Disk-resident: record the location, don't cache the value.
				idx[key] = entry{block: start + off, vlen: int32(valLen)}
			}
		} else {
			delete(idx, key)
		}
		off += blocks
	}

	s.head = off
	return idx, nil
}
