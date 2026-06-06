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

	sbSlots    = 2  // double-buffered superblock at blocks 0 and 1
	sbStartSeq = 24 // superblock: startSeq (uint64), the replay floor for the region
	sbCRCEnd   = 32 // superblock: CRC (at offset 4) covers buf[8:sbCRCEnd]
	recHeader  = 28 // magic,crc,seq,op,pad,keyLen,valLen

	opPut = 1
	opDel = 2

	// inlineMax is the largest value kept in memory; larger values are
	// disk-resident (the index holds only a pointer to the log record).
	inlineMax = 256
)

// entry is an index slot: an inline value held in memory, or a pointer to a
// disk-resident value's log record (val == nil). The on-disk record always
// holds the full key+value regardless; entry only decides what is cached.
type entry struct {
	val   []byte // inline value (small); nil if the value is disk-resident
	block int64  // record start block of a disk-resident value
	vlen  int32  // length of a disk-resident value (for Size without I/O)
}

type index = map[string]entry

// entryFor builds an index entry for val whose record starts at block: inline
// if small (reusing val, which the caller has already privately copied), else a
// pointer to the disk-resident record.
func entryFor(val []byte, block int64) entry {
	if len(val) <= inlineMax {
		return entry{val: val}
	}
	return entry{block: block, vlen: int32(len(val))}
}

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

	// writes (appender goroutine only): the channels, lifecycle, and log state.
	reqs     chan *writeReq
	resetReq chan chan error
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}

	regionStart [2]int64 // first block of each log region
	regionLen   int64    // blocks per region
	active      int      // current region (0 or 1)
	head        int64    // next free block within the active region's window
	gen         uint64   // superblock generation
	seq         uint64   // record sequence (strictly increasing across the store's life)
	startSeq    uint64   // seq floor for the active region: records below it are
	//          leftovers from a prior life of the region, so replay stops there
}

// Open opens (replaying) or formats a store on dev and starts its appender.
func Open(dev block.Device) (*Store, error) {
	s := &Store{
		dev:      dev,
		bs:       dev.BlockSize(),
		reqs:     make(chan *writeReq),
		resetReq: make(chan chan error),
		done:     make(chan struct{}),
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

// Get returns the value for key, or ErrNotFound. For an inline value this is a
// lock-free index read; for a disk-resident value it reads the log record
// (verifying it, so a location recycled by compaction is detected and retried).
// The returned slice must not be modified.
func (s *Store) Get(key string) ([]byte, error) {
	for retry := 0; retry < 8; retry++ {
		e, ok := s.cur.load()[key]
		if !ok {
			return nil, ErrNotFound
		}
		if e.val != nil {
			return e.val, nil
		}
		v, stale, err := s.readRecordValue(e.block, key)
		if err != nil {
			return nil, err
		}
		if !stale {
			return v, nil
		}
		// The record moved (compaction recycled the location); reload.
	}
	return nil, ErrNotFound
}

// Has reports whether key exists (lock-free, no I/O).
func (s *Store) Has(key string) bool {
	_, ok := s.cur.load()[key]
	return ok
}

// Size returns the value length for key (lock-free, no I/O).
func (s *Store) Size(key string) (int64, bool) {
	e, ok := s.cur.load()[key]
	if !ok {
		return 0, false
	}
	if e.val != nil {
		return int64(len(e.val)), true
	}
	return int64(e.vlen), true
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

// readRecordValue reads a disk-resident value from the log record at block,
// returning stale=true if the record there is no longer key's (a magic/CRC
// mismatch, or a different key - i.e. the location was reused by compaction).
func (s *Store) readRecordValue(block int64, key string) (val []byte, stale bool, err error) {
	hdr := make([]byte, s.bs)
	if err := s.dev.ReadBlocks(block, hdr); err != nil {
		return nil, false, err
	}
	if binary.LittleEndian.Uint32(hdr[0:]) != recMagic {
		return nil, true, nil
	}
	keyLen := int(binary.LittleEndian.Uint32(hdr[20:]))
	valLen := int(binary.LittleEndian.Uint32(hdr[24:]))
	n := recHeader + keyLen + valLen
	// Defend against trusting on-disk lengths: a block with the record magic but
	// a key length that does not match, or a record larger than a region, is not
	// a valid record for key (a torn write or a recycled location). Treat it as
	// stale rather than sizing an allocation or a read from a corrupt length.
	if keyLen != len(key) || int64((n+s.bs-1)/s.bs) > s.regionLen {
		return nil, true, nil
	}
	rec := hdr
	if blocks := int64((n + s.bs - 1) / s.bs); blocks > 1 {
		rec = make([]byte, blocks*int64(s.bs))
		if err := s.dev.ReadBlocks(block, rec); err != nil {
			return nil, false, err
		}
	}
	if binary.LittleEndian.Uint32(rec[4:]) != crc32.ChecksumIEEE(rec[8:n]) {
		return nil, true, nil
	}
	if string(rec[recHeader:recHeader+keyLen]) != key {
		return nil, true, nil
	}
	v := make([]byte, valLen)
	copy(v, rec[recHeader+keyLen:n])
	return v, false, nil
}

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

// Reset clears the store, returning it to empty - honk's stateless reset (the
// immutable core then shows through unshadowed). It is serialized with writes
// through the appender and crash-safe: a fresh empty region is published, then
// the double-buffered superblock is switched with a single atomic write, so a
// crash mid-reset leaves the old superblock and data intact.
func (s *Store) Reset() error {
	errc := make(chan error, 1)
	select {
	case <-s.ctx.Done():
		return ErrClosed
	case s.resetReq <- errc:
		return <-errc
	}
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
// applies them, and acknowledges each. Reset requests are handled on the same
// goroutine, so they never interleave with a batch.
func (s *Store) appender() {
	defer close(s.done)
	for {
		var batch []*writeReq
		select {
		case <-s.ctx.Done():
			return
		case errc := <-s.resetReq:
			errc <- s.doReset()
			continue
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

// doReset publishes a fresh empty region and atomically switches to it. The new
// region keeps whatever (valid) records its prior life left, but the superblock
// records a startSeq above all of them, so replay treats the region as empty.
// The superblock switch is the same single atomic write compaction uses.
func (s *Store) doReset() error {
	other := 1 - s.active
	startSeq := s.seq + 1
	if err := s.writeSuper(other, s.gen+1, startSeq); err != nil {
		return err
	}
	s.active, s.head, s.gen, s.startSeq = other, 0, s.gen+1, startSeq
	s.cur.store(index{})
	return nil
}

// commit applies a batch: serialize the records, write them to the active
// region (checkpointing first if they don't fit), and publish the new index
// (copy-on-write). Disk-resident values are not cached - their entry points at
// the record just written.
func (s *Store) commit(batch []*writeReq) error {
	base := s.regionStart[s.active] + s.head

	type pend struct {
		key string
		e   entry
		del bool
	}
	var pending []pend
	var recs []byte
	var off int64
	for _, req := range batch {
		s.seq++
		r := s.record(req.op, req.key, req.val)
		if req.op == opPut {
			pending = append(pending, pend{key: req.key, e: entryFor(req.val, base+off)})
		} else {
			pending = append(pending, pend{key: req.key, del: true})
		}
		recs = append(recs, r...)
		off += int64(len(r) / s.bs)
	}

	if s.head+off > s.regionLen {
		// Not enough room: checkpoint the live set into the other region.
		return s.compact(batch)
	}
	if err := s.dev.WriteBlocks(base, recs); err != nil {
		return err
	}
	if err := s.dev.Flush(); err != nil { // durable before we acknowledge the batch
		return err
	}
	s.head += off

	next := s.copyIndex()
	for _, p := range pending {
		if p.del {
			delete(next, p.key)
		} else {
			next[p.key] = p.e
		}
	}
	s.cur.store(next)
	return nil
}

func (s *Store) copyIndex() index {
	cur := s.cur.load()
	next := make(index, len(cur))
	for k, v := range cur {
		next[k] = v
	}
	return next
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

// compact rewrites the live set (current index with batch applied) as fresh
// records into the inactive region, then atomically switches the superblock to
// it. Crash-safe: the old superblock and region stay valid until the single
// (atomic) superblock block write lands. Values are streamed one at a time -
// disk-resident ones are read from the old region as they are rewritten - so
// compaction stays memory-bounded regardless of total value size.
func (s *Store) compact(batch []*writeReq) error {
	// Resolve the batch to a final value (or deletion) per key.
	bput := make(map[string][]byte)
	bdel := make(map[string]bool)
	for _, req := range batch {
		if req.op == opPut {
			bput[req.key] = req.val
			delete(bdel, req.key)
		} else {
			bdel[req.key] = true
			delete(bput, req.key)
		}
	}

	cur := s.cur.load()
	live := make(map[string]bool, len(cur)+len(bput))
	for k := range cur {
		if !bdel[k] {
			live[k] = true
		}
	}
	for k := range bput {
		live[k] = true
	}

	other := 1 - s.active
	base := s.regionStart[other]
	next := make(index, len(live))
	// Records this compaction writes begin at startSeq; everything already in
	// the destination region (a prior life of it) is below startSeq, so replay
	// will stop at the true end of the new log rather than reading leftovers.
	startSeq := s.seq + 1
	var off int64
	for k := range live {
		val, ok := bput[k]
		if !ok {
			e := cur[k]
			if e.val != nil {
				val = e.val
			} else if v, stale, err := s.readRecordValue(e.block, k); err != nil {
				return err
			} else if stale {
				return ErrCorrupt
			} else {
				val = v
			}
		}
		s.seq++
		r := s.record(opPut, k, val)
		nb := int64(len(r) / s.bs)
		if off+nb > s.regionLen {
			return ErrFull
		}
		if err := s.dev.WriteBlocks(base+off, r); err != nil {
			return err
		}
		next[k] = entryFor(val, base+off)
		off += nb
	}

	// The new region must be durable before the superblock points at it, or a
	// host crash could leave the pointer referencing unflushed data.
	if err := s.dev.Flush(); err != nil {
		return err
	}
	if err := s.writeSuper(other, s.gen+1, startSeq); err != nil {
		return err
	}
	s.active, s.head, s.gen, s.startSeq = other, off, s.gen+1, startSeq
	s.cur.store(next)
	return nil
}

// writeSuper durably writes the superblock to its (generation-selected) slot.
// It records the active region, the generation, and the region's startSeq (the
// replay floor); the CRC covers all three.
func (s *Store) writeSuper(active int, gen, startSeq uint64) error {
	buf := make([]byte, s.bs)
	binary.LittleEndian.PutUint32(buf[0:], sbMagic)
	binary.LittleEndian.PutUint64(buf[8:], gen)
	buf[16] = byte(active)
	binary.LittleEndian.PutUint64(buf[sbStartSeq:], startSeq)
	binary.LittleEndian.PutUint32(buf[4:], crc32.ChecksumIEEE(buf[8:sbCRCEnd]))
	if err := s.dev.WriteBlocks(int64(gen%sbSlots), buf); err != nil {
		return err
	}
	return s.dev.Flush()
}
