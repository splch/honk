// Package block is honk's block-device abstraction: the small interface the
// storage stack (KV store, image verity, filesystems) is built on, with the
// transport (virtio-blk now; NVMe over PCIe later) hidden entirely behind it.
//
// It is pure Go with no bare-metal dependency, so it builds and tests on the
// host; drivers that implement it live in board packages.
package block

import "errors"

var (
	// ErrRange is returned when a request falls outside the device.
	ErrRange = errors.New("block: request out of range")
	// ErrAlign is returned when a buffer length is not a multiple of the
	// block size.
	ErrAlign = errors.New("block: length not a multiple of block size")
	// ErrIO is returned when the device reports a failed transfer.
	ErrIO = errors.New("block: device I/O error")
)

// Device is a fixed-size array of equal-length blocks addressed by index.
// ReadBlocks/WriteBlocks transfer one or more contiguous blocks starting at
// block start; len(p) must be a multiple of BlockSize and the range must lie
// within [0, Blocks).
type Device interface {
	// BlockSize returns the size of a block in bytes.
	BlockSize() int
	// Blocks returns the number of blocks on the device.
	Blocks() int64
	// ReadBlocks reads len(p)/BlockSize blocks starting at start into p.
	ReadBlocks(start int64, p []byte) error
	// WriteBlocks writes len(p)/BlockSize blocks from p starting at start.
	WriteBlocks(start int64, p []byte) error
}
