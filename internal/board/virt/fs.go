//go:build tamago && riscv64

package virt

import (
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/diskfs/go-diskfs/backend"
	"github.com/splch/honk/internal/virtio"
)

// blkStorage adapts honk's virtio.Block to go-diskfs's backend.Storage so the
// FAT32 implementation can read and write the device. The block device is the
// only backing store, so Writable returns the storage itself; there is no
// underlying *os.File, so Sys is unavailable and the streaming Read/Seek (which
// fat32 never calls — it uses ReadAt/WriteAt) are stubs.
type blkStorage struct{ b *virtio.Block }

func (s *blkStorage) ReadAt(p []byte, off int64) (int, error)  { return s.b.ReadAt(p, off) }
func (s *blkStorage) WriteAt(p []byte, off int64) (int, error) { return s.b.WriteAt(p, off) }
func (s *blkStorage) Writable() (backend.WritableFile, error)  { return s, nil }
func (s *blkStorage) Stat() (fs.FileInfo, error)               { return blkInfo{s.b.Size()}, nil }
func (s *blkStorage) Close() error                             { return nil }
func (s *blkStorage) Path() string                             { return "" }
func (s *blkStorage) Sys() (*os.File, error)                   { return nil, backend.ErrNotSuitable }
func (s *blkStorage) Seek(int64, int) (int64, error) {
	return 0, errors.New("virtio: seek unsupported")
}
func (s *blkStorage) Read([]byte) (int, error) {
	return 0, errors.New("virtio: streaming read unsupported")
}

// blkInfo is the minimal fs.FileInfo backend.Storage requires.
type blkInfo struct{ size int64 }

func (i blkInfo) Name() string       { return "honk-disk" }
func (i blkInfo) Size() int64        { return i.size }
func (i blkInfo) Mode() fs.FileMode  { return 0 }
func (i blkInfo) ModTime() time.Time { return time.Time{} }
func (i blkInfo) IsDir() bool        { return false }
func (i blkInfo) Sys() any           { return nil }
