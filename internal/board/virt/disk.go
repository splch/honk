//go:build tamago && riscv64

package virt

import (
	"archive/tar"
	"io"

	"github.com/splch/honk/internal/virtio"
)

// QEMU virt exposes 8 virtio-mmio transport slots at 0x10001000, 0x1000 apart
// (RV64.md Appendix A). honk scans them for a block device.
const virtioBase = 0x10001000

// Disk is the mounted virtio block device, or nil if none is attached.
var Disk *virtio.Block

// initDisk finds and initializes a virtio-blk device, then lists the read-only
// tar image on it. Called from hwinit1, after paging maps the virtio MMIO.
func initDisk() {
	for i := uintptr(0); i < 8; i++ {
		base := uintptr(virtioBase) + i*0x1000
		if !virtio.IsBlock(base) {
			continue
		}
		d, err := virtio.New(base)
		if err != nil {
			puts("honk/virt: virtio-blk init failed: ")
			puts(err.Error())
			puts("\n")
			return
		}
		Disk = d
		break
	}
	if Disk == nil {
		return // no disk attached
	}
	listDisk()
}

// listDisk reads the disk as a tar archive (Go stdlib, on bare metal) and prints
// its entries — demonstrating a read-only filesystem over the block driver.
func listDisk() {
	tr := tar.NewReader(io.NewSectionReader(Disk, 0, Disk.Size()))
	puts("honk/virt: disk (tar):")
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		puts(" ")
		puts(h.Name)
	}
	puts("\n")
}

// ReadFile returns the contents of name from the disk's tar image.
func ReadFile(name string) ([]byte, error) {
	tr := tar.NewReader(io.NewSectionReader(Disk, 0, Disk.Size()))
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, err
		}
		if h.Name == name {
			return io.ReadAll(tr)
		}
	}
}
