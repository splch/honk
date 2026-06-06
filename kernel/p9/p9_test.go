package p9

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

// An in-process 9P2000.L server over a loopback Transport, serving a fixed tree.
// It exists so the real client protocol code (encode on the way out, decode on
// the way back) is exercised host-side, including fstest.TestFS - the same bar
// the kv/vfs filesystems are held to.

type snode struct {
	isDir    bool
	data     []byte
	children map[string]*snode
	qpath    uint64
}

type server struct {
	root  *snode
	fids  map[uint32]*snode
	msize uint32
	qnext uint64
}

func newServer(files map[string]string) *server {
	s := &server{fids: map[uint32]*snode{}, msize: 8192}
	s.root = s.node(true)
	for name, data := range files {
		s.add(name, []byte(data))
	}
	return s
}

func (s *server) node(dir bool) *snode {
	s.qnext++
	return &snode{isDir: dir, children: map[string]*snode{}, qpath: s.qnext}
}

func (s *server) add(name string, data []byte) {
	cur := s.root
	parts := strings.Split(name, "/")
	for i, p := range parts {
		last := i == len(parts)-1
		ch := cur.children[p]
		if ch == nil {
			ch = s.node(!last)
			cur.children[p] = ch
		}
		if last {
			ch.isDir = false
			ch.data = data
		}
		cur = ch
	}
}

func (s *server) MaxMessageSize() int                   { return int(s.msize) }
func (s *server) RoundTrip(tmsg []byte) ([]byte, error) { return s.handle(tmsg), nil }

func (s *server) handle(tmsg []byte) []byte {
	d := dec{b: tmsg}
	d.skip(4) // size
	typ := d.u8()
	tag := d.u16()

	switch typ {
	case tVersion:
		ms := d.u32()
		ver := d.str()
		s.msize = min(s.msize, ms)
		if ver != "9P2000.L" {
			ver = "unknown"
		}
		return reply(rVersion, tag, func(e *enc) { e.u32(s.msize); e.str(ver) })

	case tAttach:
		s.fids[d.u32()] = s.root
		return reply(rAttach, tag, func(e *enc) { putQid(e, s.root) })

	case tWalk:
		fid, newfid, nw := d.u32(), d.u32(), int(d.u16())
		cur := s.fids[fid]
		if cur == nil {
			return rerror(tag, 9) // EBADF
		}
		var qids []*snode
		for i := 0; i < nw; i++ {
			ch := cur.children[d.str()]
			if ch == nil {
				return rerror(tag, 2) // ENOENT
			}
			cur = ch
			qids = append(qids, ch)
		}
		s.fids[newfid] = cur
		return reply(rWalk, tag, func(e *enc) {
			e.u16(uint16(len(qids)))
			for _, n := range qids {
				putQid(e, n)
			}
		})

	case tLopen:
		n := s.fids[d.u32()]
		if n == nil {
			return rerror(tag, 9)
		}
		return reply(rLopen, tag, func(e *enc) { putQid(e, n); e.u32(0) })

	case tGetattr:
		n := s.fids[d.u32()]
		if n == nil {
			return rerror(tag, 9)
		}
		return reply(rGetattr, tag, func(e *enc) { putAttr(e, n) })

	case tRead:
		fid, off := d.u32(), d.u64()
		count := d.u32()
		n := s.fids[fid]
		if n == nil || n.isDir {
			return rerror(tag, 9)
		}
		var chunk []byte
		if off < uint64(len(n.data)) {
			end := min(off+uint64(count), uint64(len(n.data)))
			chunk = n.data[off:end]
		}
		return reply(rRead, tag, func(e *enc) { e.u32(uint32(len(chunk))); e.b = append(e.b, chunk...) })

	case tReaddir:
		fid, off := d.u32(), d.u64()
		_ = d.u32() // count (the test tree fits in one response)
		n := s.fids[fid]
		if n == nil || !n.isDir {
			return rerror(tag, 9)
		}
		return reply(rReaddir, tag, func(e *enc) { putDirents(e, n, off) })

	case tClunk:
		delete(s.fids, d.u32())
		return reply(rClunk, tag, func(*enc) {})

	default:
		return rerror(tag, 38) // ENOSYS
	}
}

func reply(typ byte, tag uint16, body func(*enc)) []byte {
	var e enc
	e.header(typ, tag)
	body(&e)
	return e.finish()
}

func rerror(tag uint16, ecode uint32) []byte {
	return reply(rLerror, tag, func(e *enc) { e.u32(ecode) })
}

func putQid(e *enc, n *snode) {
	var t byte
	if n.isDir {
		t = qtDir
	}
	e.u8(t)
	e.u32(0)       // version
	e.u64(n.qpath) // path
}

func putAttr(e *enc, n *snode) {
	e.u64(getattrBasic) // valid
	putQid(e, n)
	mode := uint32(0o644 | 0x8000) // S_IFREG
	if n.isDir {
		mode = 0o755 | 0x4000 // S_IFDIR
	}
	e.u32(mode)
	e.u32(0)                   // uid
	e.u32(0)                   // gid
	e.u64(1)                   // nlink
	e.u64(0)                   // rdev
	e.u64(uint64(len(n.data))) // size
	for i := 0; i < 12; i++ {  // blksize..data_version
		e.u64(0)
	}
}

func putDirents(e *enc, n *snode, off uint64) {
	names := []string{".", ".."}
	for name := range n.children {
		names = append(names, name)
	}
	var blob enc
	for i, name := range names {
		cookie := uint64(i + 1)
		if cookie <= off {
			continue
		}
		child := n
		if name != "." && name != ".." {
			child = n.children[name]
		}
		putQid(&blob, child)
		blob.u64(cookie)
		dt := byte(8) // DT_REG
		if child.isDir {
			dt = 4 // DT_DIR
		}
		blob.u8(dt)
		blob.str(name)
	}
	e.u32(uint32(len(blob.b)))
	e.b = append(e.b, blob.b...)
}

// --- tests -------------------------------------------------------------------

func mount(t *testing.T, files map[string]string) fs.FS {
	t.Helper()
	fsys, err := Mount(newServer(files))
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return fsys
}

func TestReadFile(t *testing.T) {
	fsys := mount(t, map[string]string{
		"motd":           "honk from the host\n",
		"etc/hostname":   "hostbox",
		"docs/readme.md": "a deeper file",
	})
	for name, want := range map[string]string{
		"motd":           "honk from the host\n",
		"etc/hostname":   "hostbox",
		"docs/readme.md": "a deeper file",
	} {
		b, err := fs.ReadFile(fsys, name)
		if err != nil || string(b) != want {
			t.Fatalf("ReadFile(%q) = %q, %v; want %q", name, b, err, want)
		}
	}
}

func TestLargeFileSpansReads(t *testing.T) {
	// A value larger than one Tread (msize - overhead) must reassemble across
	// multiple round trips. Shrink msize via the server so the test is cheap.
	srv := newServer(nil)
	srv.msize = 64 // tiny: forces many Tread chunks
	big := strings.Repeat("honk-", 200)
	srv.add("big", []byte(big))
	fsys, err := Mount(srv)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	b, err := fs.ReadFile(fsys, "big")
	if err != nil || string(b) != big {
		t.Fatalf("ReadFile(big) = %d bytes, %v; want %d", len(b), err, len(big))
	}
}

func TestStatAndReadDir(t *testing.T) {
	fsys := mount(t, map[string]string{
		"a/x": "1", "a/y": "22", "a/b/z": "333", "top": "T",
	})
	fi, err := fs.Stat(fsys, "a/y")
	if err != nil || fi.IsDir() || fi.Size() != 2 {
		t.Fatalf("Stat(a/y): isdir=%v size=%d err=%v", fi.IsDir(), fi.Size(), err)
	}
	if di, err := fs.Stat(fsys, "a"); err != nil || !di.IsDir() {
		t.Fatalf("Stat(a): isdir=%v err=%v", di.IsDir(), err)
	}
	es, err := fs.ReadDir(fsys, "a")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	if got := strings.Join(names, ","); got != "b,x,y" {
		t.Fatalf("ReadDir(a) = %q, want b,x,y", got)
	}
}

func TestNotExist(t *testing.T) {
	fsys := mount(t, map[string]string{"present": "x"})
	if _, err := fsys.Open("absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open(absent) = %v, want ErrNotExist", err)
	}
	if _, err := fs.Stat(fsys, "no/such/deep"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat(no/such/deep) = %v, want ErrNotExist", err)
	}
	if _, err := fsys.Open("../escape"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("Open(invalid) = %v, want ErrInvalid", err)
	}
}

func TestFSCompliance(t *testing.T) {
	fsys := mount(t, map[string]string{
		"motd":         "hi",
		"etc/hostname": "hostbox",
		"etc/os":       "honk",
		"a/b/c":        "deep",
		"a/b/d":        "deep too",
		"top":          "T",
	})
	if err := fstest.TestFS(fsys, "motd", "etc/hostname", "etc/os", "a/b/c", "a/b/d", "top"); err != nil {
		t.Fatal(err)
	}
}
