package block

import "sync"

// Memory is an in-RAM block.Device, used to test the storage stack on the host.
type Memory struct {
	mu        sync.Mutex
	blockSize int
	data      []byte
}

// NewMemory returns a zeroed in-memory device of the given geometry.
func NewMemory(blocks int64, blockSize int) *Memory {
	return &Memory{blockSize: blockSize, data: make([]byte, blocks*int64(blockSize))}
}

func (m *Memory) BlockSize() int { return m.blockSize }
func (m *Memory) Blocks() int64  { return int64(len(m.data)) / int64(m.blockSize) }

func (m *Memory) ReadBlocks(start int64, p []byte) error {
	if err := m.check(start, p); err != nil {
		return err
	}
	m.mu.Lock()
	copy(p, m.data[start*int64(m.blockSize):])
	m.mu.Unlock()
	return nil
}

func (m *Memory) WriteBlocks(start int64, p []byte) error {
	if err := m.check(start, p); err != nil {
		return err
	}
	m.mu.Lock()
	copy(m.data[start*int64(m.blockSize):], p)
	m.mu.Unlock()
	return nil
}

func (m *Memory) check(start int64, p []byte) error {
	if len(p) == 0 || len(p)%m.blockSize != 0 {
		return ErrAlign
	}
	if start < 0 || start+int64(len(p)/m.blockSize) > m.Blocks() {
		return ErrRange
	}
	return nil
}
