// honk - filesystem mount: the immutable embedded core overlaid by the kv store.

//go:build tamago && riscv64

package main

import (
	"embed"
	"io/fs"
	"path"
	"strings"

	"honk/board/virt"
	"honk/kernel/kv"
	"honk/kernel/vfs"
)

// coreFiles is the immutable core image baked into the kernel (read-only).
//
//go:embed core
var coreFiles embed.FS

var (
	store *kv.Store // persistent writable layer (nil if no block device)
	root  fs.FS     // the mounted filesystem (overlay, or core-only)
)

// mountFS builds the root filesystem: the kv store (over the block device) as a
// writable layer over the read-only embedded core. With no block device, only
// the core is mounted (read-only).
func mountFS() {
	core, _ := fs.Sub(coreFiles, "core")
	if dev := virt.Block(); dev != nil {
		if s, err := kv.Open(dev); err == nil {
			store = s
			root = vfs.Overlay(vfs.KVFS(s), core)
			return
		}
	}
	root = core
}

// fsPath normalizes a shell path argument to an io/fs path (relative, cleaned,
// "." for root). It returns ok=false for paths that escape the root.
func fsPath(arg string) (string, bool) {
	p := path.Clean("/" + arg) // anchor, collapse .. that would escape
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		p = "."
	}
	return p, fs.ValidPath(p)
}
