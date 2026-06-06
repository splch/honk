package image

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	"honk/block"
)

// devSeed mirrors tools/mkimage: the fixed development signing seed. The public
// key it derives must equal the embedded DevPublicKey.
func devSeed() []byte {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(0x40 + i)
	}
	return seed
}

func devKey(t *testing.T) (ed25519.PrivateKey, SoftwareAnchor) {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(devSeed())
	return priv, SoftwareAnchor{Key: priv.Public().(ed25519.PublicKey)}
}

func build(t *testing.T, priv ed25519.PrivateKey, secVer uint64, files map[string][]byte) []byte {
	t.Helper()
	return Build(files, secVer, func(msg []byte) []byte { return ed25519.Sign(priv, msg) })
}

var coreFiles = map[string][]byte{
	"motd":         []byte("welcome to honk\n"),
	"etc/hostname": []byte("honkbox"),
	"etc/os":       []byte("honk 0.1"),
	"big":          bytes.Repeat([]byte("X"), LeafSize*3+7), // spans several Merkle leaves
}

func TestDevKeyMatchesEmbedded(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(devSeed())
	if !bytes.Equal(priv.Public().(ed25519.PublicKey), DevPublicKey) {
		t.Fatal("derived dev public key != embedded DevPublicKey (regenerate key.go)")
	}
}

func TestBuildVerifyRoundTrip(t *testing.T) {
	priv, anchor := devKey(t)
	img := build(t, priv, 7, coreFiles)

	v, err := Verify(img, anchor)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.SecVersion != 7 {
		t.Fatalf("SecVersion = %d, want 7", v.SecVersion)
	}
	got := v.Files()
	if len(got) != len(coreFiles) {
		t.Fatalf("file count = %d, want %d", len(got), len(coreFiles))
	}
	for name, want := range coreFiles {
		if !bytes.Equal(got[name], want) {
			t.Fatalf("file %q = %d bytes, want %d", name, len(got[name]), len(want))
		}
	}
}

func TestTamperDataFailsMerkle(t *testing.T) {
	priv, anchor := devKey(t)
	img := build(t, priv, 1, coreFiles)
	img[len(img)-1] ^= 0xff // a byte in the data region
	if _, err := Verify(img, anchor); err != ErrMerkle {
		t.Fatalf("err = %v, want ErrMerkle", err)
	}
}

func TestTamperTableFailsHash(t *testing.T) {
	priv, anchor := devKey(t)
	img := build(t, priv, 1, coreFiles)
	img[HeaderSize] ^= 0xff // first byte of the file table
	if _, err := Verify(img, anchor); err != ErrTable {
		t.Fatalf("err = %v, want ErrTable", err)
	}
}

func TestTamperHeaderFailsSignature(t *testing.T) {
	priv, anchor := devKey(t)
	img := build(t, priv, 1, coreFiles)
	img[offSecVer] ^= 0x01 // mutate a signed header field
	if _, err := Verify(img, anchor); err != ErrSignature {
		t.Fatalf("err = %v, want ErrSignature", err)
	}
}

func TestWrongKeyFailsSignature(t *testing.T) {
	priv, _ := devKey(t)
	img := build(t, priv, 1, coreFiles)
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x01}, 32))
	anchor := SoftwareAnchor{Key: other.Public().(ed25519.PublicKey)}
	if _, err := Verify(img, anchor); err != ErrSignature {
		t.Fatalf("err = %v, want ErrSignature", err)
	}
}

func TestAntiRollback(t *testing.T) {
	priv, _ := devKey(t)
	img := build(t, priv, 3, coreFiles)
	anchor := SoftwareAnchor{Key: priv.Public().(ed25519.PublicKey), Floor: 5}
	if _, err := Verify(img, anchor); err != ErrRollback {
		t.Fatalf("err = %v, want ErrRollback", err)
	}
	// At or above the floor it verifies.
	anchor.Floor = 3
	if _, err := Verify(img, anchor); err != nil {
		t.Fatalf("Verify at floor: %v", err)
	}
}

func TestSelectHighestVersion(t *testing.T) {
	priv, anchor := devKey(t)
	a := build(t, priv, 2, coreFiles)
	b := build(t, priv, 1, coreFiles)
	v, idx, err := Select(anchor, a, b)
	if err != nil || idx != 0 || v.SecVersion != 2 {
		t.Fatalf("Select = idx %d v%d err %v, want idx 0 v2", idx, version(v), err)
	}

	// Corrupt the newer slot: A/B falls back to the valid older one.
	a[len(a)-1] ^= 0xff
	v, idx, err = Select(anchor, a, b)
	if err != nil || idx != 1 || v.SecVersion != 1 {
		t.Fatalf("fallback Select = idx %d v%d err %v, want idx 1 v1", idx, version(v), err)
	}
}

func TestSelectNoneValid(t *testing.T) {
	priv, anchor := devKey(t)
	a := build(t, priv, 1, coreFiles)
	a[offSecVer] ^= 0xff
	if _, _, err := Select(anchor, a, nil, []byte{}); err != ErrNoImage {
		t.Fatalf("err = %v, want ErrNoImage", err)
	}
}

func TestSlotsRoundTrip(t *testing.T) {
	priv, anchor := devKey(t)
	dev := block.NewMemory(8192, 512) // 4 MiB: room for two 1 MiB slots
	a := build(t, priv, 4, coreFiles)
	b := build(t, priv, 9, coreFiles)

	sb := SlotBlocks(dev.BlockSize())
	if err := WriteSlot(dev, 0, a); err != nil {
		t.Fatal(err)
	}
	if err := WriteSlot(dev, sb, b); err != nil {
		t.Fatal(err)
	}

	ra, err := ReadSlot(dev, 0)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := ReadSlot(dev, sb)
	if err != nil {
		t.Fatal(err)
	}
	v, idx, err := Select(anchor, ra, rb)
	if err != nil || idx != 1 || v.SecVersion != 9 {
		t.Fatalf("Select from slots = idx %d v%d err %v, want idx 1 v9", idx, version(v), err)
	}
}

func version(v *Image) uint64 {
	if v == nil {
		return 0
	}
	return v.SecVersion
}
