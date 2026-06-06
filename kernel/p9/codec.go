package p9

import "encoding/binary"

// 9P2000.L message types - the subset honk's read-only client uses. The full
// set is defined by the Linux 9P protocol (net/9p); honk only reads files and
// directories, so it needs version/attach/walk/open/read/readdir/getattr/clunk.
const (
	rLerror  = 7
	tLopen   = 12
	rLopen   = 13
	tGetattr = 24
	rGetattr = 25
	tReaddir = 40
	rReaddir = 41
	tVersion = 100
	rVersion = 101
	tAttach  = 104
	rAttach  = 105
	tWalk    = 110
	rWalk    = 111
	tRead    = 116
	rRead    = 117
	tClunk   = 120
	rClunk   = 121
)

const (
	noFid uint32 = ^uint32(0) // NOFID: "no auth fid" for Tattach.afid
	noTag uint16 = ^uint16(0) // NOTAG: the required tag for Tversion

	qtDir = 0x80 // qid.type bit marking a directory (QTDIR)

	getattrBasic = 0x000007ff // P9_GETATTR_BASIC: mode, ids, size, link, times

	headerLen = 7  // size[4] + type[1] + tag[2]
	qidLen    = 13 // type[1] + version[4] + path[8]

	// readOverhead is the Rread/Rreaddir header (size+type+tag+count) that the
	// payload shares the negotiated msize with.
	readOverhead = headerLen + 4
)

// enc builds a little-endian 9P message. header reserves the 4-byte size
// prefix; finish back-patches it. The wire encoding lives only here.
type enc struct{ b []byte }

func (e *enc) header(typ byte, tag uint16) {
	e.b = append(e.b, 0, 0, 0, 0, typ, byte(tag), byte(tag>>8))
}
func (e *enc) u8(v byte)    { e.b = append(e.b, v) }
func (e *enc) u16(v uint16) { e.b = append(e.b, byte(v), byte(v>>8)) }
func (e *enc) u32(v uint32) { e.b = append(e.b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24)) }
func (e *enc) u64(v uint64) {
	e.b = append(e.b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24),
		byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56))
}
func (e *enc) str(s string) { e.u16(uint16(len(s))); e.b = append(e.b, s...) }

// finish back-patches the size prefix and returns the complete message.
func (e *enc) finish() []byte {
	binary.LittleEndian.PutUint32(e.b, uint32(len(e.b)))
	return e.b
}

// dec reads a little-endian 9P body, latching the first out-of-bounds read so a
// caller can decode a whole message and check err once at the end.
type dec struct {
	b   []byte
	off int
	err error
}

func (d *dec) need(n int) bool {
	if d.err != nil {
		return false
	}
	if n < 0 || d.off+n > len(d.b) {
		d.err = errShort
		return false
	}
	return true
}

func (d *dec) u8() byte {
	if !d.need(1) {
		return 0
	}
	v := d.b[d.off]
	d.off++
	return v
}

func (d *dec) u16() uint16 {
	if !d.need(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(d.b[d.off:])
	d.off += 2
	return v
}

func (d *dec) u32() uint32 {
	if !d.need(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(d.b[d.off:])
	d.off += 4
	return v
}

func (d *dec) u64() uint64 {
	if !d.need(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(d.b[d.off:])
	d.off += 8
	return v
}

func (d *dec) skip(n int) {
	if d.need(n) {
		d.off += n
	}
}

func (d *dec) str() string {
	n := int(d.u16())
	if !d.need(n) {
		return ""
	}
	s := string(d.b[d.off : d.off+n])
	d.off += n
	return s
}

// bytes returns the next n bytes as a sub-slice (no copy).
func (d *dec) bytes(n int) []byte {
	if !d.need(n) {
		return nil
	}
	b := d.b[d.off : d.off+n]
	d.off += n
	return b
}
