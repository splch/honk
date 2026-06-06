// Package p9 is honk's read-only 9P2000.L client (HONK.md M8). It mounts a file
// server reachable over a message Transport - in honk, the virtio-9p device
// QEMU exports for a host directory - and presents it as a standard io/fs.FS,
// so host files compose with the kv store and the embedded core through the
// same overlay (HONK.md §1: filesystems are io/fs.FS composition, not a
// bespoke API).
//
// The package owns the 9P wire format and the fid lifecycle behind a tiny
// surface: a Transport (one exchange: send a T-message, get the R-message) and
// Mount, which returns an fs.FS. Versioning, fids, walking, and directory
// decoding are all hidden.
//
// It is pure Go with no bare-metal dependency, so the protocol is exercised
// host-side (go test, including fstest.TestFS) against an in-process 9P server.
package p9

import (
	"errors"
	"fmt"
	"io/fs"
	"sync"
)

var (
	errShort  = errors.New("p9: truncated message")
	errNotDir = errors.New("p9: not a directory")
	errIsDir  = errors.New("p9: is a directory")
)

// Transport carries a single 9P message exchange. honk's virtio-9p driver
// implements it; the host test uses an in-process loopback.
type Transport interface {
	// MaxMessageSize is the largest message the transport can carry; it bounds
	// the negotiated msize.
	MaxMessageSize() int
	// RoundTrip sends one complete T-message and returns the R-message reply.
	// The reply is valid only until the next call (the client copies it out
	// while holding its lock).
	RoundTrip(tmsg []byte) (rmsg []byte, err error)
}

// Mount negotiates 9P2000.L, attaches to the exported tree as honk, and returns
// it as a read-only io/fs.FS.
func Mount(t Transport) (fs.FS, error) {
	c := &client{t: t, nextFid: 1}
	if err := c.version(); err != nil {
		return nil, err
	}
	if err := c.attach(); err != nil {
		return nil, err
	}
	return &filesystem{c: c}, nil
}

// rootFid is the attach fid: it is never opened for I/O or clunked, so every
// walk clones from it into a fresh fid the caller owns.
const rootFid uint32 = 0

// client is one attached 9P session. A single mutex serializes the transport
// (so only one exchange is ever on the wire - which is why every message can
// use the same tag) and the fid allocator. No method holds the mutex across an
// RPC, so concurrent fs operations interleave, one exchange at a time.
type client struct {
	t Transport

	mu       sync.Mutex
	msize    uint32
	nextFid  uint32
	freeFids []uint32
}

// rpc sends tmsg, copies the reply out so it survives the next exchange, and
// returns the message body (past the header). An Rlerror becomes the matching
// fs error; an unexpected reply type is an error.
func (c *client) rpc(want byte, tmsg []byte) ([]byte, error) {
	c.mu.Lock()
	reply, err := c.t.RoundTrip(tmsg)
	var out []byte
	if err == nil {
		out = make([]byte, len(reply))
		copy(out, reply)
	}
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if len(out) < headerLen {
		return nil, errShort
	}
	switch out[4] {
	case want:
		return out[headerLen:], nil
	case rLerror:
		d := dec{b: out[headerLen:]}
		return nil, errnoToErr(d.u32())
	default:
		return nil, fmt.Errorf("p9: reply type %d, want %d", out[4], want)
	}
}

func (c *client) allocFid() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := len(c.freeFids); n > 0 {
		f := c.freeFids[n-1]
		c.freeFids = c.freeFids[:n-1]
		return f
	}
	f := c.nextFid
	c.nextFid++
	return f
}

func (c *client) freeFid(f uint32) {
	c.mu.Lock()
	c.freeFids = append(c.freeFids, f)
	c.mu.Unlock()
}

func (c *client) version() error {
	want := uint32(c.t.MaxMessageSize())
	var e enc
	e.header(tVersion, noTag)
	e.u32(want)
	e.str("9P2000.L")
	body, err := c.rpc(rVersion, e.finish())
	if err != nil {
		return err
	}
	d := dec{b: body}
	got := d.u32()
	ver := d.str()
	if d.err != nil {
		return d.err
	}
	if ver != "9P2000.L" {
		return fmt.Errorf("p9: server speaks %q, not 9P2000.L", ver)
	}
	c.msize = min(want, got)
	if c.msize <= readOverhead {
		return fmt.Errorf("p9: msize %d too small", c.msize)
	}
	return nil
}

func (c *client) attach() error {
	var e enc
	e.header(tAttach, 0)
	e.u32(rootFid)
	e.u32(noFid)
	e.str("honk") // uname
	e.str("")     // aname: the server's export root
	e.u32(0)      // n_uname: numeric uid (0) for 9P2000.L
	_, err := c.rpc(rAttach, e.finish())
	return err
}

// walk clones from through the named path components into a fresh fid the
// caller owns (and must clunk). It batches into the 16-element Twalk limit so
// arbitrarily deep paths work; an empty path clones the directory in place. A
// component that does not exist is fs.ErrNotExist.
func (c *client) walk(from uint32, names []string) (uint32, error) {
	cur, own := from, false
	for i := 0; i < len(names); i += 16 {
		chunk := names[i:min(i+16, len(names))]
		next := c.allocFid()
		body, err := c.twalk(cur, next, chunk)
		if own {
			c.clunk(cur)
		}
		if err != nil {
			c.freeFid(next)
			return 0, err
		}
		d := dec{b: body}
		got := int(d.u16())
		if d.err != nil || got < len(chunk) {
			c.clunk(next)
			if d.err != nil {
				return 0, d.err
			}
			return 0, fs.ErrNotExist
		}
		cur, own = next, true
	}
	if !own { // empty path: clone from so Close can clunk without touching from
		next := c.allocFid()
		if _, err := c.twalk(from, next, nil); err != nil {
			c.freeFid(next)
			return 0, err
		}
		cur = next
	}
	return cur, nil
}

func (c *client) twalk(fid, newfid uint32, names []string) ([]byte, error) {
	var e enc
	e.header(tWalk, 0)
	e.u32(fid)
	e.u32(newfid)
	e.u16(uint16(len(names)))
	for _, n := range names {
		e.str(n)
	}
	return c.rpc(rWalk, e.finish())
}

// attr is the slice of Tgetattr honk reads: whether it is a directory, the
// permission/type mode bits, and the size.
type attr struct {
	qidType byte
	mode    uint32
	size    uint64
}

func (c *client) getattr(fid uint32) (attr, error) {
	var e enc
	e.header(tGetattr, 0)
	e.u32(fid)
	e.u64(getattrBasic)
	body, err := c.rpc(rGetattr, e.finish())
	if err != nil {
		return attr{}, err
	}
	d := dec{b: body}
	d.skip(8)             // valid mask
	qt := d.u8()          // qid.type
	d.skip(qidLen - 1)    // rest of qid
	mode := d.u32()       // st_mode
	d.skip(4 + 4 + 8 + 8) // uid, gid, nlink, rdev
	size := d.u64()       // st_size
	if d.err != nil {
		return attr{}, d.err
	}
	return attr{qidType: qt, mode: mode, size: size}, nil
}

func (c *client) lopen(fid uint32) error {
	var e enc
	e.header(tLopen, 0)
	e.u32(fid)
	e.u32(0) // flags: O_RDONLY
	_, err := c.rpc(rLopen, e.finish())
	return err
}

func (c *client) read(fid uint32, offset uint64, p []byte) (int, error) {
	count := uint32(len(p))
	if limit := c.msize - readOverhead; count > limit {
		count = limit
	}
	var e enc
	e.header(tRead, 0)
	e.u32(fid)
	e.u64(offset)
	e.u32(count)
	body, err := c.rpc(rRead, e.finish())
	if err != nil {
		return 0, err
	}
	d := dec{b: body}
	n := int(d.u32())
	if d.err != nil {
		return 0, d.err
	}
	data := body[4:]
	if n > len(data) {
		return 0, errShort
	}
	return copy(p, data[:n]), nil
}

// dirent is one decoded Treaddir entry.
type dirent struct {
	name  string
	isDir bool
	next  uint64 // cookie to pass as the offset of the following Treaddir
}

func (c *client) readdir(fid uint32, offset uint64) ([]dirent, error) {
	var e enc
	e.header(tReaddir, 0)
	e.u32(fid)
	e.u64(offset)
	e.u32(c.msize - readOverhead)
	body, err := c.rpc(rReaddir, e.finish())
	if err != nil {
		return nil, err
	}
	d := dec{b: body}
	blob := dec{b: d.bytes(int(d.u32()))}
	if d.err != nil {
		return nil, d.err
	}
	var out []dirent
	for blob.off < len(blob.b) {
		qt := blob.u8()
		blob.skip(qidLen - 1)
		next := blob.u64()
		blob.skip(1) // d_type (redundant with qid.type)
		name := blob.str()
		if blob.err != nil {
			return nil, blob.err
		}
		out = append(out, dirent{name: name, isDir: qt&qtDir != 0, next: next})
	}
	return out, nil
}

func (c *client) clunk(fid uint32) {
	var e enc
	e.header(tClunk, 0)
	e.u32(fid)
	_, _ = c.rpc(rClunk, e.finish()) // best effort: the server drops fid regardless
	c.freeFid(fid)
}

// errnoToErr maps the Linux errno carried by an Rlerror to an fs sentinel, so
// the overlay falls through on a missing path; others surface verbatim.
func errnoToErr(ec uint32) error {
	switch ec {
	case 2, 20: // ENOENT; ENOTDIR (a path component is not a directory)
		return fs.ErrNotExist
	case 1, 13: // EPERM, EACCES
		return fs.ErrPermission
	default:
		return fmt.Errorf("p9: server error %d", ec)
	}
}
