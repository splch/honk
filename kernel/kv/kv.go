// Package kv is honk's persistent key/value store: a crash-safe,
// log-structured store over a block.Device (HONK.md §2 storage).
//
// Design, mapped onto Go primitives (HONK.md §1):
//
//   - A single appender goroutine owns all writes; callers send requests on a
//     channel and the appender drains them in a batch - the drain *is* the
//     group commit (one device write per batch, serialized, no write locks).
//   - Readers are lock-free: the index is an immutable map published through an
//     atomic pointer; the appender copies-on-write and swaps it.
//   - Durability is the log: records are checksummed and replayed on Open,
//     stopping at the first torn/!valid record (a crash leaves at most an
//     unacknowledged tail, which is discarded and overwritten).
//   - Compaction is an atomic checkpoint: the live set is rewritten into the
//     other of two log regions, then a double-buffered superblock is switched
//     with a single (atomic) block write. A crash mid-compaction leaves the old
//     superblock - and old region - intact.
//
// Values are held in memory (the log is the durable copy), which suits honk's
// configuration/state workload.
package kv

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"

	"honk/block"
)

var (
	ErrClosed   = errors.New("kv: store closed")
	ErrNotFound = errors.New("kv: key not found")
	ErrFull     = errors.New("kv: device full")
	ErrCorrupt  = errors.New("kv: corrupt store")
)

const (
	sbMagic  = 0x484b5342 // "HKSB"
	recMagic = 0x484b5631 // "HKV1"

	sbSlots   = 2  // double-buffered superblock at blocks 0 and 1
	recHeader = 28 // magic,crc,seq,op,pad,keyLen,valLen

	opPut = 1
	opDel = 2
)

type index = map[string][]byte

// writeReq is one pending mutation handed to the appender.
type writeReq struct {
	op  byte
	key string
	val []byte
	err chan error
}

// Store is an open key/value store.
type Store struct {
	dev block.Device
	bs  int

	// reads (lock-free): the current immutable index snapshot.
	cur indexPtr

	// writes (appender goroutine only): the channel, lifecycle, and log state.
	reqs   chan *writeReq
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	regionStart [2]int64 // first block of each log region
	regionLen   int64    // blocks per region
	active      int      // current region (0 or 1)
	head        int64    // next free block within the active region's window
	gen         uint64   // superblock generation
	seq         uint64   // record sequence
}

// Open opens (replaying) or formats a store on dev and starts its appender.
func Open(dev block.Device) (*Store, error) {
	s := &Store{
		dev:  dev,
		bs:   dev.BlockSize(),
		reqs: make(chan *writeReq),
		done: make(chan struct{}),
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())

	blocks := dev.Blocks()
	if blocks < sbSlots+2 {
		return nil, ErrFull
	}
	s.regionLen = (blocks - sbSlots) / 2
	s.regionStart = [2]int64{sbSlots, sbSlots + s.regionLen}

	idx, err := s.recover()
	if err != nil {
		return nil, err
	}
	s.cur.store(idx)

	go s.appender()
	return s, nil
}

// Get returns the value for key (lock-free), reporting whether it exists. The
// returned slice must not be modified.
func (s *Store) Get(key string) ([]byte, bool) {
	v, ok := s.cur.load()[key]
	return v, ok
}

// Keys returns a snapshot of all keys, unordered.
func (s *Store) Keys() []string {
	m := s.cur.load()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// Len returns the number of keys.
func (s *Store) Len() int { return len(s.cur.load()) }

// Put stores val under key, returning once it is committed to the device.
func (s *Store) Put(key string, val []byte) error {
	cp := make([]byte, len(val))
	copy(cp, val)
	return s.submit(&writeReq{op: opPut, key: key, val: cp})
}

// Delete removes key, returning once committed.
func (s *Store) Delete(key string) error {
	return s.submit(&writeReq{op: opDel, key: key})
}

// Close stops the appender. The store must not be used afterwards.
func (s *Store) Close() error {
	s.cancel()
	<-s.done
	return nil
}

func (s *Store) submit(req *writeReq) error {
	req.err = make(chan error, 1)
	select {
	case <-s.ctx.Done():
		return ErrClosed
	case s.reqs <- req:
		return <-req.err
	}
}

// appender is the single writer: it batches pending requests (group commit),
// applies them, and acknowledges each.
func (s *Store) appender() {
	defer close(s.done)
	for {
		var batch []*writeReq
		select {
		case <-s.ctx.Done():
			return
		case req := <-s.reqs:
			batch = append(batch, req)
		}
		// drain whatever else is queued - this is the group commit.
		for draining := true; draining; {
			select {
			case req := <-s.reqs:
				batch = append(batch, req)
			default:
				draining = false
			}
		}

		err := s.commit(batch)
		for _, req := range batch {
			req.err <- err
		}
	}
}

// commit applies a batch: build the next index (copy-on-write), serialize the
// records, write them to the active region (compacting first if needed), and
// publish the new index.
func (s *Store) commit(batch []*writeReq) error {
	next := make(index, len(s.cur.load())+len(batch))
	for k, v := range s.cur.load() {
		next[k] = v
	}

	var recs []byte
	for _, req := range batch {
		s.seq++
		recs = append(recs, s.record(req.op, req.key, req.val)...)
		if req.op == opPut {
			next[req.key] = req.val
		} else {
			delete(next, req.key)
		}
	}

	nblk := int64(len(recs) / s.bs)
	if s.head+nblk > s.regionLen {
		// Not enough room: checkpoint the new live set into the other region.
		if err := s.compact(next); err != nil {
			return err
		}
	} else {
		if err := s.dev.WriteBlocks(s.regionStart[s.active]+s.head, recs); err != nil {
			return err
		}
		s.head += nblk
	}

	s.cur.store(next)
	return nil
}

// record serializes one log record padded to a whole number of blocks.
func (s *Store) record(op byte, key string, val []byte) []byte {
	n := recHeader + len(key) + len(val)
	blocks := (n + s.bs - 1) / s.bs
	buf := make([]byte, blocks*s.bs)

	binary.LittleEndian.PutUint32(buf[0:], recMagic)
	binary.LittleEndian.PutUint64(buf[8:], s.seq)
	buf[16] = op
	binary.LittleEndian.PutUint32(buf[20:], uint32(len(key)))
	binary.LittleEndian.PutUint32(buf[24:], uint32(len(val)))
	copy(buf[recHeader:], key)
	copy(buf[recHeader+len(key):], val)
	binary.LittleEndian.PutUint32(buf[4:], crc32.ChecksumIEEE(buf[8:n]))
	return buf
}

// compact rewrites idx as fresh put records into the inactive region, then
// atomically switches the superblock to it. Crash-safe: the old superblock and
// region remain valid until the single-block superblock write lands.
func (s *Store) compact(idx index) error {
	other := 1 - s.active

	var recs []byte
	var head int64
	for k, v := range idx {
		s.seq++
		r := s.record(opPut, k, v)
		recs = append(recs, r...)
		head += int64(len(r) / s.bs)
	}
	if head > s.regionLen {
		return ErrFull
	}
	if len(recs) > 0 {
		if err := s.dev.WriteBlocks(s.regionStart[other], recs); err != nil {
			return err
		}
	}
	if err := s.writeSuper(other, s.gen+1); err != nil {
		return err
	}

	s.active = other
	s.head = head
	s.gen++
	return nil
}

func (s *Store) writeSuper(active int, gen uint64) error {
	buf := make([]byte, s.bs)
	binary.LittleEndian.PutUint32(buf[0:], sbMagic)
	binary.LittleEndian.PutUint64(buf[8:], gen)
	buf[16] = byte(active)
	binary.LittleEndian.PutUint32(buf[4:], crc32.ChecksumIEEE(buf[8:17]))
	return s.dev.WriteBlocks(int64(gen%sbSlots), buf)
}
