// Package fdt builds Flattened Device Tree (FDT) blobs for tests. It is the
// write-side twin of package dtb (the parser): a test constructs a tree with
// Begin/End and property calls, then Bytes emits a spec-valid blob to feed back
// through dtb.Parse. Keeping the builder here means the FDT byte layout lives in
// exactly one place besides the parser, instead of being re-implemented in every
// test package that needs a device tree.
package fdt

import "encoding/binary"

// FDT header magic and struct-block tokens (Devicetree Specification). These
// mirror the parser's constants in package dtb — reader and writer are the two
// irreducible owners of the wire format.
const (
	magic        = 0xd00dfeed
	tokBeginNode = 0x1
	tokEndNode   = 0x2
	tokProp      = 0x3
	tokEnd       = 0x9
)

// Builder accumulates device-tree nodes and properties. The zero value is ready
// to use; build a tree with Begin/End and the property helpers, then call Bytes.
type Builder struct {
	structb []byte
	strings []byte
	offsets map[string]uint32
}

// Begin opens a node named name (e.g. "" for the root, "soc", or
// "serial@10000000"). Each Begin must be matched by an End.
func (b *Builder) Begin(name string) {
	b.tok(tokBeginNode)
	b.structb = append(b.structb, name...)
	b.structb = append(b.structb, 0)
	b.align()
}

// End closes the most recently opened node.
func (b *Builder) End() { b.tok(tokEndNode) }

// Str adds a null-terminated string property.
func (b *Builder) Str(name, s string) { b.Prop(name, append([]byte(s), 0)) }

// U32 adds a single big-endian 32-bit cell property (e.g. #address-cells).
func (b *Builder) U32(name string, v uint32) {
	var p [4]byte
	binary.BigEndian.PutUint32(p[:], v)
	b.Prop(name, p[:])
}

// Reg adds a "reg" property of one (address, size) pair, each two cells — the
// 2/2 cell layout the QEMU 'virt' machine uses.
func (b *Builder) Reg(addr, size uint64) {
	var p [16]byte
	binary.BigEndian.PutUint64(p[0:], addr)
	binary.BigEndian.PutUint64(p[8:], size)
	b.Prop("reg", p[:])
}

// Prop adds a property with raw value bytes, interning name in the strings block
// so repeated names share one offset.
func (b *Builder) Prop(name string, val []byte) {
	if b.offsets == nil {
		b.offsets = map[string]uint32{}
	}
	off, ok := b.offsets[name]
	if !ok {
		off = uint32(len(b.strings))
		b.offsets[name] = off
		b.strings = append(append(b.strings, name...), 0)
	}
	b.tok(tokProp)
	b.tok(uint32(len(val)))
	b.tok(off)
	b.structb = append(b.structb, val...)
	b.align()
}

// Bytes finalizes the tree and returns a spec-valid FDT blob: a header, one
// terminating (empty) memory-reservation entry, the struct block, and the
// strings block.
func (b *Builder) Bytes() []byte {
	b.tok(tokEnd)
	const hdr, memrsv = 40, 16
	structOff := uint32(hdr + memrsv)
	stringsOff := structOff + uint32(len(b.structb))

	var out []byte
	put := func(v uint32) { out = beAppend(out, v) }
	put(magic)
	put(stringsOff + uint32(len(b.strings))) // totalsize
	put(structOff)
	put(stringsOff)
	put(hdr) // off_mem_rsvmap
	put(17)  // version
	put(16)  // last_comp_version
	put(0)   // boot_cpuid_phys
	put(uint32(len(b.strings)))
	put(uint32(len(b.structb)))
	out = append(out, make([]byte, memrsv)...) // terminating reservation entry
	out = append(out, b.structb...)
	out = append(out, b.strings...)
	return out
}

func (b *Builder) tok(v uint32) { b.structb = beAppend(b.structb, v) }

func (b *Builder) align() {
	for len(b.structb)%4 != 0 {
		b.structb = append(b.structb, 0)
	}
}

func beAppend(b []byte, v uint32) []byte {
	var w [4]byte
	binary.BigEndian.PutUint32(w[:], v)
	return append(b, w[:]...)
}
