// honk - QEMU virt board: runtime entropy hook.

//go:build tamago && riscv64

package virt

import _ "unsafe"

// initRNG initializes random number generation for the runtime.
//
//go:linkname initRNG runtime/goos.InitRNG
func initRNG() {}

// getRandomData fills b with bytes derived from the time counter.
//
// TODO(security): this is a boot-time stopgap and is NOT cryptographically
// secure. The runtime uses it for hashmap seeds at startup. Replace with a
// real entropy source (the Zkr seed CSR, or a virtio-rng / SoC TRNG) before
// any cryptographic use in honk.
//
//go:linkname getRandomData runtime/goos.GetRandomData
//go:nosplit
func getRandomData(b []byte) {
	for i := range b {
		t := readTime() + uint64(i)
		b[i] = byte(t ^ (t >> 7) ^ (t >> 17) ^ (t >> 31))
	}
}
