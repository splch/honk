// Package image is honk's immutable, integrity-verified core image (HONK.md §2
// storage, milestone M5).
//
// An image is a set of read-only files packed with a Merkle tree (SHA-256) over
// its data blocks; the Merkle root, an anti-rollback security version, and a
// hash of the file table live in a fixed header that is signed with Ed25519.
// Boot verifies the signature against a trusted public key (the anchor), checks
// the security version against a monotonic floor (anti-rollback), recomputes
// the Merkle root and the table hash, and only then serves the files. Tamper
// with any byte and verification fails closed.
//
// The verification root of trust is abstracted behind Anchor (verify.go): the
// QEMU baseline embeds the public key in the binary (a software chain); real
// silicon anchors the key hash in OTP fuses and the version floor in a
// monotonic counter. honk holds two slots and boots the valid one with the
// highest version, falling back to the other (the A/B model).
//
// This package is pure Go with no bare-metal dependency, so the same format and
// verification code is exercised host-side (go test -race) and by the mkimage
// tool that builds images.
package image

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sort"
)

var (
	ErrFormat    = errors.New("image: bad format")
	ErrSignature = errors.New("image: signature verification failed")
	ErrMerkle    = errors.New("image: merkle root mismatch")
	ErrTable     = errors.New("image: file table hash mismatch")
	ErrRollback  = errors.New("image: security version below anti-rollback floor")
	ErrNoImage   = errors.New("image: no valid image found")
)

const (
	magic         = "HONKIMG1"
	formatVersion = 1

	// HeaderSize is the fixed signed header at the front of every image.
	HeaderSize = 512
	// sigOffset is where the Ed25519 signature sits; everything before it
	// (magic, version, secVersion, geometry, merkleRoot, tableHash) is signed.
	sigOffset = HeaderSize - 64

	// LeafSize is the Merkle leaf (data block) size. It is independent of any
	// storage device's block size: an image is a byte blob that may be embedded
	// in the kernel or stored across device blocks.
	LeafSize = 4096

	// header field offsets (little-endian)
	offMagic     = 0  // 8
	offFormatVer = 8  // 4
	offSecVer    = 12 // 8
	offLeafSize  = 20 // 4
	offNData     = 24 // 8 (data blocks)
	offDataLen   = 32 // 8 (exact data bytes, before block padding)
	offNFiles    = 40 // 4
	offTableLen  = 44 // 4
	offRoot      = 48 // 32
	offTableHash = 80 // 32
)

// MerkleRoot computes the SHA-256 binary Merkle tree root over data split into
// leaves of leafSize bytes (the final leaf zero-padded). It is the single
// definition of the tree shared by the builder and the verifier. An odd node
// count promotes the lone node by hashing it with itself.
func MerkleRoot(leafSize int, data []byte) [32]byte {
	n := (len(data) + leafSize - 1) / leafSize
	if n == 0 {
		return sha256.Sum256(nil)
	}
	level := make([][32]byte, n)
	for i := 0; i < n; i++ {
		lo := i * leafSize
		hi := lo + leafSize
		if hi <= len(data) {
			level[i] = sha256.Sum256(data[lo:hi])
		} else {
			leaf := make([]byte, leafSize)
			copy(leaf, data[lo:])
			level[i] = sha256.Sum256(leaf)
		}
	}
	for len(level) > 1 {
		next := make([][32]byte, (len(level)+1)/2)
		for i := range next {
			l := level[2*i]
			r := l
			if 2*i+1 < len(level) {
				r = level[2*i+1]
			}
			h := sha256.New()
			h.Write(l[:])
			h.Write(r[:])
			h.Sum(next[i][:0])
		}
		level = next
	}
	return level[0]
}

// Build packs files into an image with the given security version, signing the
// header's signed region with sign (mkimage supplies an Ed25519 closure; the
// kernel never signs). Names are slash paths and the layout is deterministic
// (sorted names), so builds are reproducible.
func Build(files map[string][]byte, secVersion uint64, sign func(msg []byte) []byte) []byte {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)

	// data region: file contents concatenated, recording (offset, length).
	var data []byte
	type loc struct{ off, length uint64 }
	locs := make(map[string]loc, len(names))
	for _, n := range names {
		locs[n] = loc{off: uint64(len(data)), length: uint64(len(files[n]))}
		data = append(data, files[n]...)
	}
	dataLen := uint64(len(data))
	if pad := len(data) % LeafSize; pad != 0 {
		data = append(data, make([]byte, LeafSize-pad)...)
	}
	nData := uint64(len(data) / LeafSize)

	// file table: nameLen(u16) name off(u64) length(u64) per entry.
	var table []byte
	for _, n := range names {
		var ent [10]byte
		binary.LittleEndian.PutUint16(ent[0:], uint16(len(n)))
		table = append(table, ent[:2]...)
		table = append(table, n...)
		binary.LittleEndian.PutUint64(ent[0:], locs[n].off)
		table = append(table, ent[:8]...)
		binary.LittleEndian.PutUint64(ent[0:], locs[n].length)
		table = append(table, ent[:8]...)
	}
	tableHash := sha256.Sum256(table)
	root := MerkleRoot(LeafSize, data)

	hdr := make([]byte, HeaderSize)
	copy(hdr[offMagic:], magic)
	binary.LittleEndian.PutUint32(hdr[offFormatVer:], formatVersion)
	binary.LittleEndian.PutUint64(hdr[offSecVer:], secVersion)
	binary.LittleEndian.PutUint32(hdr[offLeafSize:], LeafSize)
	binary.LittleEndian.PutUint64(hdr[offNData:], nData)
	binary.LittleEndian.PutUint64(hdr[offDataLen:], dataLen)
	binary.LittleEndian.PutUint32(hdr[offNFiles:], uint32(len(names)))
	binary.LittleEndian.PutUint32(hdr[offTableLen:], uint32(len(table)))
	copy(hdr[offRoot:], root[:])
	copy(hdr[offTableHash:], tableHash[:])
	copy(hdr[sigOffset:], sign(hdr[:sigOffset]))

	out := make([]byte, 0, HeaderSize+len(table)+len(data))
	out = append(out, hdr...)
	out = append(out, table...)
	out = append(out, data...)
	return out
}

// header is the parsed, not-yet-trusted view of an image's fixed header.
type header struct {
	secVersion uint64
	leafSize   int
	nData      uint64
	dataLen    uint64
	nFiles     uint32
	tableLen   uint32
	root       [32]byte
	tableHash  [32]byte
	sig        []byte
}

// parseHeader validates the magic/format and the declared geometry against the
// image length, returning the header and the table/data byte ranges. It does
// not verify any cryptographic claim (Verify does).
func parseHeader(img []byte) (h header, tableOff, dataOff int, err error) {
	if len(img) < HeaderSize || string(img[offMagic:offMagic+8]) != magic {
		return h, 0, 0, ErrFormat
	}
	if binary.LittleEndian.Uint32(img[offFormatVer:]) != formatVersion {
		return h, 0, 0, ErrFormat
	}
	h.secVersion = binary.LittleEndian.Uint64(img[offSecVer:])
	h.leafSize = int(binary.LittleEndian.Uint32(img[offLeafSize:]))
	h.nData = binary.LittleEndian.Uint64(img[offNData:])
	h.dataLen = binary.LittleEndian.Uint64(img[offDataLen:])
	h.nFiles = binary.LittleEndian.Uint32(img[offNFiles:])
	h.tableLen = binary.LittleEndian.Uint32(img[offTableLen:])
	copy(h.root[:], img[offRoot:])
	copy(h.tableHash[:], img[offTableHash:])
	h.sig = img[sigOffset:HeaderSize]

	if h.leafSize <= 0 || h.dataLen > h.nData*uint64(h.leafSize) {
		return h, 0, 0, ErrFormat
	}
	tableOff = HeaderSize
	dataOff = tableOff + int(h.tableLen)
	end := dataOff + int(h.nData)*h.leafSize
	if end < dataOff || end > len(img) { // overflow / truncation
		return h, 0, 0, ErrFormat
	}
	return h, tableOff, dataOff, nil
}
