package image

import "crypto/ed25519"

// DevPublicKey is the Ed25519 public key of honk's development signing key, the
// trust anchor for the QEMU software chain. The matching private key is the
// fixed dev seed in tools/mkimage (committed-safe: it signs only local QEMU
// images, never production ones).
//
// This is the one byte string a real board would NOT embed in mutable flash:
// on silicon the key (or its hash) is fused into OTP and the SoftwareAnchor is
// replaced by a hardware-backed Anchor. Rotating the dev key means regenerating
// this value and re-signing with the new seed.
var DevPublicKey = ed25519.PublicKey{
	0x25, 0x43, 0xb9, 0x2f, 0xf1, 0x09, 0x55, 0x11,
	0x47, 0x6a, 0xdc, 0x83, 0x69, 0xdb, 0x6d, 0xdc,
	0x93, 0x36, 0x65, 0xa1, 0x19, 0x78, 0xdd, 0xa1,
	0x40, 0x4e, 0xe1, 0x06, 0x6c, 0xa9, 0x55, 0x9d,
}
