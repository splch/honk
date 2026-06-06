// Package vfs composes honk's filesystem from io/fs.FS pieces (HONK.md §1):
// a writable view of the kv store and a read-only overlay that unions it over
// the immutable embedded core image.
//
// io/fs.FS is read-only by design; writes go through the kv store directly
// (e.g. the shell's cp), and the overlay's upper layer (the kv FS) shadows the
// lower (the core) so edits hide the originals.
package vfs

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"honk/kernel/kv"
)

// KVFS returns a read-only io/fs.FS view of a kv store. Keys are slash-separated
// paths; directories are synthesized from the key set.
func KVFS(s *kv.Store) fs.FS { return &kvFS{s} }

type kvFS struct{ s *kv.Store }

func (f *kvFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." || f.isDir(name) {
		entries, _ := f.ReadDir(name)
		return &dirFile{info: info{name: path.Base(name), dir: true}, entries: entries}, nil
	}
	if v, ok := f.s.Get(name); ok {
		return &dataFile{info: info{name: path.Base(name), size: int64(len(v))}, r: bytes.NewReader(v)}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func (f *kvFS) Stat(name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." || f.isDir(name) {
		return info{name: path.Base(name), dir: true}, nil
	}
	if v, ok := f.s.Get(name); ok {
		return info{name: path.Base(name), size: int64(len(v))}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

func (f *kvFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	prefix := ""
	if name != "." {
		prefix = name + "/"
	}
	// child base name -> isDir (a name with descendants is a directory).
	kind := map[string]bool{}
	for _, k := range f.s.Keys() {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := k[len(prefix):]
		if rest == "" {
			continue
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			kind[rest[:i]] = true
		} else if !kind[rest] {
			kind[rest] = false
		}
	}
	names := make([]string, 0, len(kind))
	for n := range kind {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]fs.DirEntry, 0, len(names))
	for _, n := range names {
		i := info{name: n, dir: kind[n]}
		if !i.dir {
			if v, ok := f.s.Get(prefix + n); ok {
				i.size = int64(len(v))
			}
		}
		out = append(out, i)
	}
	return out, nil
}

func (f *kvFS) isDir(name string) bool {
	prefix := name + "/"
	for _, k := range f.s.Keys() {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// Overlay unions upper over lower: reads prefer upper, and directory listings
// merge both with upper shadowing lower by name.
func Overlay(upper, lower fs.FS) fs.FS { return &overlay{upper, lower} }

type overlay struct{ upper, lower fs.FS }

func (o *overlay) Open(name string) (fs.File, error) {
	if f, err := o.upper.Open(name); err == nil {
		return f, nil
	}
	return o.lower.Open(name)
}

func (o *overlay) Stat(name string) (fs.FileInfo, error) {
	if fi, err := fs.Stat(o.upper, name); err == nil {
		return fi, nil
	}
	return fs.Stat(o.lower, name)
}

func (o *overlay) ReadDir(name string) ([]fs.DirEntry, error) {
	seen := map[string]fs.DirEntry{}
	up, _ := fs.ReadDir(o.upper, name)
	for _, e := range up {
		seen[e.Name()] = e
	}
	lo, errLo := fs.ReadDir(o.lower, name)
	for _, e := range lo {
		if _, ok := seen[e.Name()]; !ok {
			seen[e.Name()] = e
		}
	}
	if len(seen) == 0 && len(up) == 0 && errLo != nil {
		return nil, errLo
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]fs.DirEntry, 0, len(names))
	for _, n := range names {
		out = append(out, seen[n])
	}
	return out, nil
}

// info implements fs.FileInfo and fs.DirEntry. name is the base name.
type info struct {
	name string
	size int64
	dir  bool
}

func (i info) Name() string { return i.name }
func (i info) Size() int64  { return i.size }
func (i info) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (i info) ModTime() time.Time         { return time.Time{} }
func (i info) IsDir() bool                { return i.dir }
func (i info) Sys() any                   { return nil }
func (i info) Type() fs.FileMode          { return i.Mode().Type() }
func (i info) Info() (fs.FileInfo, error) { return i, nil }

type dataFile struct {
	info info
	r    *bytes.Reader
}

func (f *dataFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *dataFile) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *dataFile) Close() error               { return nil }

type dirFile struct {
	info    info
	entries []fs.DirEntry
	off     int
}

func (d *dirFile) Stat() (fs.FileInfo, error) { return d.info, nil }
func (d *dirFile) Close() error               { return nil }
func (d *dirFile) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.name, Err: errors.New("is a directory")}
}

func (d *dirFile) ReadDir(n int) ([]fs.DirEntry, error) {
	if n <= 0 {
		e := d.entries[d.off:]
		d.off = len(d.entries)
		return e, nil
	}
	if d.off >= len(d.entries) {
		return nil, io.EOF
	}
	end := min(d.off+n, len(d.entries))
	e := d.entries[d.off:end]
	d.off = end
	return e, nil
}
