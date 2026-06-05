//go:build tamago && riscv64

package virt

import (
	"errors"
	"io"
	"os"
	"strings"
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
		if !blankDisk(disk) {
			// A populated or corrupt filesystem, or a transient read error — NOT a
			// blank image. Reformatting here would silently destroy data, so refuse
			// and leave FS nil (the shell then reports "no disk").
			puts("honk/virt: FAT32 mount failed and disk is not blank; refusing to reformat (")
			puts(err.Error())
			puts(")\n")
			return
		}
		// Blank image: lay down a fresh FAT32 and seed it.
		f, err = fat32.Create(store, size, 0, virtio.SectorSize, "HONK", false)
		if err != nil {
			puts("honk/virt: FAT32 format failed: ")
			puts(err.Error())
			puts("\n")
			return
		}
		seedFS(f)
		disk.Flush() // commit the format + motd to the backing image
		puts("honk/virt: formatted blank FAT32 disk\n")
	}
	FS = f
	puts("honk/virt: ")
	listDisk(uart0)
}

// blankDisk reports whether the device carries no filesystem. A boot sector
// without the 0x55AA signature (offset 510-511) is treated as blank; anything
// else — including an unreadable sector 0 — is treated as populated, so a
// transient I/O error or an unrecognized-but-real filesystem is never silently
// reformatted into oblivion.
func blankDisk(d *virtio.Block) bool {
	var sec [virtio.SectorSize]byte
	if _, err := d.ReadAt(sec[:], 0); err != nil {
		return false // cannot read sector 0: do not assume blank
	}
	return sec[510] != 0x55 || sec[511] != 0xAA
}

// validName rejects file names FAT/go-diskfs cannot represent safely. honk's
// shell writes only to the disk root, so a name is a single path component:
// reject empty, over-long, dot entries, path separators, and reserved chars.
func validName(name string) bool {
	if name == "" || len(name) > 255 || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return false
		}
	}
	return true
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
	if !validName(name) {
		return errors.New("invalid file name")
	}
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
