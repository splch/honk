package p9

import (
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// filesystem presents an attached 9P session as a read-only io/fs.FS. It is the
// only type Mount exposes (as an fs.FS); files, directories, and the protocol
// client behind it are hidden.
type filesystem struct{ c *client }

func (f *filesystem) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	fid, err := f.c.walk(rootFid, components(name))
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	a, err := f.c.getattr(fid)
	if err == nil {
		err = f.c.lopen(fid)
	}
	if err != nil {
		f.c.clunk(fid)
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	fi := fileInfo{name: path.Base(name), size: int64(a.size), mode: fileMode(a)}
	if a.qidType&qtDir != 0 {
		return &dir{fs: f, fid: fid, path: name, info: fi}, nil
	}
	return &file{c: f.c, fid: fid, info: fi}, nil
}

func (f *filesystem) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	fid, err := f.c.walk(rootFid, components(name))
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	defer f.c.clunk(fid)
	a, err := f.c.getattr(fid)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	return fileInfo{name: path.Base(name), size: int64(a.size), mode: fileMode(a)}, nil
}

func (f *filesystem) ReadDir(name string) ([]fs.DirEntry, error) {
	file, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	d, ok := file.(*dir)
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: errNotDir}
	}
	return d.ReadDir(-1)
}

// file is an open regular file; reads stream sequentially via Tread.
type file struct {
	c    *client
	fid  uint32
	info fileInfo
	off  uint64
	done bool
}

func (f *file) Stat() (fs.FileInfo, error) { return f.info, nil }

func (f *file) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, err := f.c.read(f.fid, f.off, p)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, io.EOF
	}
	f.off += uint64(n)
	return n, nil
}

func (f *file) Close() error {
	if !f.done {
		f.done = true
		f.c.clunk(f.fid)
	}
	return nil
}

// dir is an open directory; its entries are decoded from Treaddir lazily on the
// first ReadDir and then paged out per the fs.ReadDirFile contract.
type dir struct {
	fs      *filesystem
	fid     uint32
	path    string
	info    fileInfo
	entries []fs.DirEntry
	pos     int
	loaded  bool
	done    bool
}

func (d *dir) Stat() (fs.FileInfo, error) { return d.info, nil }

func (d *dir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.path, Err: errIsDir}
}

func (d *dir) Close() error {
	if !d.done {
		d.done = true
		d.fs.c.clunk(d.fid)
	}
	return nil
}

func (d *dir) ReadDir(n int) ([]fs.DirEntry, error) {
	if !d.loaded {
		es, err := d.readAll()
		if err != nil {
			return nil, err
		}
		d.entries, d.loaded = es, true
	}
	if n <= 0 {
		es := d.entries[d.pos:]
		d.pos = len(d.entries)
		return es, nil
	}
	if d.pos >= len(d.entries) {
		return nil, io.EOF
	}
	es := d.entries[d.pos:min(d.pos+n, len(d.entries))]
	d.pos += len(es)
	return es, nil
}

// readAll drains Treaddir until the server reports no more entries, dropping the
// "." and ".." aliases and sorting by name (the io/fs convention).
func (d *dir) readAll() ([]fs.DirEntry, error) {
	var out []fs.DirEntry
	var offset uint64
	for {
		des, err := d.fs.c.readdir(d.fid, offset)
		if err != nil {
			return nil, err
		}
		if len(des) == 0 {
			break
		}
		for _, de := range des {
			offset = de.next
			if de.name == "." || de.name == ".." {
				continue
			}
			out = append(out, &dirEntry{fs: d.fs, path: join(d.path, de.name), name: de.name, isDir: de.isDir})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// dirEntry resolves its full FileInfo lazily: Treaddir yields only name and
// type, so listing stays one round trip and only callers that need size/mode
// pay a Stat.
type dirEntry struct {
	fs    *filesystem
	path  string
	name  string
	isDir bool
}

func (e *dirEntry) Name() string { return e.name }
func (e *dirEntry) IsDir() bool  { return e.isDir }
func (e *dirEntry) Type() fs.FileMode {
	if e.isDir {
		return fs.ModeDir
	}
	return 0
}
func (e *dirEntry) Info() (fs.FileInfo, error) { return e.fs.Stat(e.path) }

// fileInfo is a read-only fs.FileInfo. The host share is presented timeless,
// like honk's other synthesized filesystems.
type fileInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (i fileInfo) Name() string       { return i.name }
func (i fileInfo) Size() int64        { return i.size }
func (i fileInfo) Mode() fs.FileMode  { return i.mode }
func (i fileInfo) ModTime() time.Time { return time.Time{} }
func (i fileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fileInfo) Sys() any           { return nil }

// fileMode renders a 9P attr as an fs.FileMode: the Unix permission bits plus
// the directory bit from the qid type.
func fileMode(a attr) fs.FileMode {
	m := fs.FileMode(a.mode & 0o777)
	if a.qidType&qtDir != 0 {
		m |= fs.ModeDir
	}
	return m
}

func components(name string) []string {
	if name == "." {
		return nil
	}
	return strings.Split(name, "/")
}

func join(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}
