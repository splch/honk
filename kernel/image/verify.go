package image

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
)

// Anchor is honk's root of trust for image verification - the "anchored-boot
// interface" (HONK.md §2). It yields the trusted signing key and the
// anti-rollback version floor below which an otherwise-valid image is refused.
//
// On QEMU the anchor is SoftwareAnchor: the public key is embedded in the
// binary and the floor is read from persistent state. On real silicon the same
// interface is backed by an OTP-fused key hash and a monotonic security-version
// counter, so the policy is identical and only the hardware backing changes.
type Anchor interface {
	// PublicKey returns the trusted Ed25519 verification key.
	PublicKey() ed25519.PublicKey
	// MinVersion returns the anti-rollback floor: images with a lower security
	// version are rejected even if correctly signed.
	MinVersion() uint64
}

// SoftwareAnchor is the QEMU-baseline Anchor: an embedded public key and a
// version floor sourced from persistent state (the kv store).
type SoftwareAnchor struct {
	Key   ed25519.PublicKey
	Floor uint64
}

func (a SoftwareAnchor) PublicKey() ed25519.PublicKey { return a.Key }
func (a SoftwareAnchor) MinVersion() uint64           { return a.Floor }

// Image is a verified, read-only core image: trusted file contents plus the
// security version that passed the anti-rollback check.
type Image struct {
	SecVersion uint64
	files      map[string][]byte
}

// Files returns the verified files (slash-separated paths to contents). The
// returned map and slices are read-only; callers must not mutate them.
func (i *Image) Files() map[string][]byte { return i.files }

// Verify checks img against anchor and returns the verified Image, or an error
// identifying the first check that failed. The checks, in order: format,
// signature over the header, anti-rollback floor, file-table hash, Merkle root
// over the data blocks, and per-file bounds. It fails closed - any tampering
// anywhere in the image is detected before a single file is served.
func Verify(img []byte, anchor Anchor) (*Image, error) {
	h, tableOff, dataOff, err := parseHeader(img)
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(anchor.PublicKey(), img[:sigOffset], h.sig) {
		return nil, ErrSignature
	}
	if h.secVersion < anchor.MinVersion() {
		return nil, ErrRollback
	}

	table := img[tableOff : tableOff+int(h.tableLen)]
	if gotTable := sha256.Sum256(table); subtle.ConstantTimeCompare(gotTable[:], h.tableHash[:]) != 1 {
		return nil, ErrTable
	}
	data := img[dataOff : dataOff+int(h.nData)*h.leafSize]
	if gotRoot := MerkleRoot(h.leafSize, data); subtle.ConstantTimeCompare(gotRoot[:], h.root[:]) != 1 {
		return nil, ErrMerkle
	}

	files, err := parseTable(table, data[:h.dataLen], h.nFiles)
	if err != nil {
		return nil, err
	}
	return &Image{SecVersion: h.secVersion, files: files}, nil
}

// parseTable decodes the (already hash-verified) file table into a map of
// verified file contents, bounds-checking every entry against the data region.
func parseTable(table, data []byte, nFiles uint32) (map[string][]byte, error) {
	files := make(map[string][]byte, nFiles)
	off := 0
	for i := uint32(0); i < nFiles; i++ {
		if off+2 > len(table) {
			return nil, ErrFormat
		}
		nameLen := int(binary.LittleEndian.Uint16(table[off:]))
		off += 2
		if off+nameLen+16 > len(table) {
			return nil, ErrFormat
		}
		name := string(table[off : off+nameLen])
		off += nameLen
		fileOff := binary.LittleEndian.Uint64(table[off:])
		fileLen := binary.LittleEndian.Uint64(table[off+8:])
		off += 16
		if fileOff > uint64(len(data)) || fileOff+fileLen > uint64(len(data)) || fileOff+fileLen < fileOff {
			return nil, ErrFormat
		}
		v := make([]byte, fileLen)
		copy(v, data[fileOff:fileOff+fileLen])
		files[name] = v
	}
	return files, nil
}

// Select implements honk's A/B boot policy: verify every candidate against
// anchor and return the valid one with the highest security version, plus its
// index. Invalid or corrupt candidates are skipped (verify-then-switch with
// fallback); empty candidates are ignored. If none verifies it returns
// ErrNoImage. Earlier candidates win ties, so list the embedded factory image
// last as the guaranteed-good fallback.
func Select(anchor Anchor, candidates ...[]byte) (*Image, int, error) {
	var best *Image
	bestIdx := -1
	for i, c := range candidates {
		if len(c) == 0 {
			continue
		}
		img, err := Verify(c, anchor)
		if err != nil {
			continue
		}
		if best == nil || img.SecVersion > best.SecVersion {
			best, bestIdx = img, i
		}
	}
	if best == nil {
		return nil, -1, ErrNoImage
	}
	return best, bestIdx, nil
}
