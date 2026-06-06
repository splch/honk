// honk - filesystem mount: the immutable, integrity-verified core (M5) overlaid
// by the writable kv store.
//
// The core is a signed, Merkle-tree'd image (kernel/image). honk holds two
// on-disk update slots plus the embedded factory image and boots the valid one
// with the highest security version, falling back across the rest (the A/B
// model). The block device is partitioned: slot A, slot B, then the kv region.

//go:build tamago && riscv64

package main

import (
	_ "embed"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"honk/block"
	"honk/board/virt"
	"honk/kernel/image"
	"honk/kernel/kv"
	"honk/kernel/p9"
	"honk/kernel/vfs"
)

// coreImage is the immutable core, built by tools/mkimage and baked into the
// kernel as the factory image - the guaranteed-good A/B fallback.
//
//go:embed core.img
var coreImage []byte

var (
	store    *kv.Store // persistent writable layer (nil if no block device)
	root     fs.FS     // the mounted filesystem (overlay, or core-only)
	bootCore string    // which image booted, for the banner (e.g. "slot A v2")
	hostTag  string    // mount tag of the 9p host share, "" if none
)

// minKVBlocks is the smallest kv region worth carving out behind the image
// slots; below this the device hosts only kv and the slots are skipped.
const minKVBlocks = 16

// mountFS verifies the core image and builds the root filesystem: the writable
// kv store (on the device tail) over the read-only verified core. With no block
// device, only the verified core is mounted (read-only). A device too small for
// the image slots hosts the kv store alone and uses the embedded core.
func mountFS() {
	anchor := image.SoftwareAnchor{Key: image.DevPublicKey} // Floor 0: see note below
	var slotA, slotB []byte

	if dev := virt.Block(); dev != nil {
		sb := image.SlotBlocks(dev.BlockSize())
		if dev.Blocks() >= 2*sb+minKVBlocks {
			slotA, _ = image.ReadSlot(dev, 0)
			slotB, _ = image.ReadSlot(dev, sb)
			openKV(block.Slice(dev, 2*sb, dev.Blocks()-2*sb))
		} else {
			openKV(dev) // too small to partition: kv gets the whole device
		}
	}

	// Verify-then-switch with fallback: the factory image is listed last as the
	// guaranteed-good candidate. Anti-rollback is enforced by the image layer
	// (Anchor.MinVersion); the QEMU software anchor pins the floor at 0, and a
	// real board backs it with a monotonic OTP counter instead.
	img, idx, err := image.Select(anchor, slotA, slotB, coreImage)
	if err != nil {
		fmt.Printf("honk: FATAL no verifiable core image: %v\n", err)
		virt.Shutdown() // honk has no core to serve; do not run unverified.
	}
	bootCore = fmt.Sprintf("%s v%d", coreSource(idx), img.SecVersion)
	fmt.Printf("honk: core verified - %s\n", bootCore)

	// Compose the root bottom-up (HONK.md §1: io/fs.FS composition): the
	// verified core, the host share (9p) layered over it if present, and the
	// writable kv store on top. Each layer shadows the ones below it.
	root = vfs.FilesFS(img.Files())
	if dev := virt.ProbeP9(); dev != nil {
		if hostFS, err := p9.Mount(dev); err != nil {
			fmt.Printf("honk: host share unavailable: %v\n", err)
		} else {
			root = vfs.Overlay(hostFS, root)
			hostTag = dev.Tag()
			fmt.Printf("honk: host share mounted (9p, tag %q)\n", hostTag)
		}
	}
	if store != nil {
		root = vfs.Overlay(vfs.KVFS(store), root)
	}
}

func openKV(dev block.Device) {
	if s, err := kv.Open(dev); err == nil {
		store = s
	}
}

// coreSource names the A/B candidate that booted.
func coreSource(idx int) string {
	switch idx {
	case 0:
		return "slot A"
	case 1:
		return "slot B"
	default:
		return "embedded factory"
	}
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
