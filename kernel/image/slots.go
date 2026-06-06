package image

import "honk/block"

// SlotBytes is the on-disk size reserved for each A/B image slot. The core
// image is tiny, but a generous fixed slot leaves room for growth and keeps the
// device layout (slot A, slot B, then the kv region) trivial to compute.
const SlotBytes = 1 << 20 // 1 MiB

// SlotBlocks returns the number of device blocks one slot occupies.
func SlotBlocks(blockSize int) int64 {
	return (SlotBytes + int64(blockSize) - 1) / int64(blockSize)
}

// ReadSlot reads a candidate image (the whole slot) from dev starting at the
// given block. Trailing bytes beyond the image are ignored by Verify, which
// uses the header's declared geometry.
func ReadSlot(dev block.Device, startBlock int64) ([]byte, error) {
	buf := make([]byte, SlotBlocks(dev.BlockSize())*int64(dev.BlockSize()))
	if err := dev.ReadBlocks(startBlock, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// WriteSlot writes img (zero-padded to the device block size) into the slot at
// startBlock and flushes. It is used by tests and a future on-device updater;
// the kernel itself never builds or signs images.
func WriteSlot(dev block.Device, startBlock int64, img []byte) error {
	bs := int64(dev.BlockSize())
	if int64(len(img)) > SlotBlocks(int(bs))*bs {
		return block.ErrRange
	}
	buf := make([]byte, (int64(len(img))+bs-1)/bs*bs)
	copy(buf, img)
	if err := dev.WriteBlocks(startBlock, buf); err != nil {
		return err
	}
	return dev.Flush()
}
