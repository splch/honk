//go:build tamago && riscv64

package virt

import (
	"io"
	"os"
	"sync"

	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/filesystem/fat32"
	"github.com/splch/honk/internal/virtio"
)

// QEMU virt exposes 8 virtio-mmio transport slots at 0x10001000, 0x1000 apart
// (RV64.md Appendix A). honk scans them for a block device.
const virtioBase = 0x10001000

var (
	disk *virtio.Block
	// FS is the mounted writable FAT32 filesystem, or nil if no disk is attached.
	FS filesystem.FileSystem
	// fsMu serializes filesystem access: the FAT32 driver and the single virtio
	// DMA buffer are not safe for the concurrent use that multiple SSH sessions
	// would otherwise cause.
	fsMu sync.Mutex
)

// init mounts the disk from a package init() rather than hwinit1: the go-diskfs
// FAT32 driver uses defer, which faults on the system stack where hwinit1 runs
// (DESIGN.md §15.3). The virtio MMIO is already mapped by paging in hwinit1.
func init() { initDisk() }

// initDisk finds a virtio-blk device and mounts its FAT32 filesystem, formatting
// a fresh one (and seeding a motd) if the image is blank. Called from hwinit1,
// after paging maps the virtio MMIO. The mounted contents persist across reboots
// because the device writes through to the backing image (DESIGN.md §15, step 7).
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
		disk = d
		break
	}
	if disk == nil {
		return // no disk attached
	}

	store := &blkStorage{disk}
	size := disk.Size()
	f, err := fat32.Read(store, size, 0, virtio.SectorSize)
	if err != nil {
		// Unformatted (e.g. a blank image): lay down a fresh FAT32 and seed it.
		f, err = fat32.Create(store, size, 0, virtio.SectorSize, "HONK", false)
		if err != nil {
			puts("honk/virt: FAT32 format failed: ")
			puts(err.Error())
			puts("\n")
			return
		}
		seedFS(f)
		disk.Flush() // commit the format + motd to the backing image
		puts("honk/virt: formatted FAT32 disk\n")
	}
	FS = f
	puts("honk/virt: ")
	listDisk(uart0)
}

// seedFS writes honk's initial files to a freshly formatted disk.
func seedFS(f filesystem.FileSystem) {
	w, err := f.OpenFile("/motd", os.O_CREATE|os.O_RDWR|os.O_TRUNC)
	if err != nil {
		return
	}
	io.WriteString(w, "honk: a small RISC-V 64-bit operating system in pure Go.\n")
	io.WriteString(w, "This is a writable FAT32 disk; files you write here persist across reboots.\n")
	w.Close()
}

// listDisk writes the root directory listing to w, backing the shell `ls`
// command for both the UART console and SSH sessions.
func listDisk(w io.Writer) {
	fsMu.Lock()
	defer fsMu.Unlock()
	io.WriteString(w, "disk (fat32):")
	entries, err := FS.ReadDir(".") // go-diskfs io/fs methods want "." for root
	if err != nil {
		io.WriteString(w, " <error>")
	}
	for _, e := range entries {
		io.WriteString(w, " ")
		io.WriteString(w, e.Name())
	}
	io.WriteString(w, "\r\n")
}

// ReadFile returns the contents of name from the root of the disk.
func ReadFile(name string) ([]byte, error) {
	fsMu.Lock()
	defer fsMu.Unlock()
	f, err := FS.OpenFile("/"+name, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// WriteFile creates or overwrites name at the root of the disk; the data
// persists across reboots.
func WriteFile(name string, data []byte) error {
	fsMu.Lock()
	defer fsMu.Unlock()
	f, err := FS.OpenFile("/"+name, os.O_CREATE|os.O_RDWR|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return disk.Flush() // commit to non-volatile storage (no-op if no write cache)
}
