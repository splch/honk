//go:build tamago && riscv64

package virt

import _ "unsafe"

// rngState backs a non-cryptographic splitmix64 generator seeded from the
// `time` counter. It is sufficient to bring up the Go runtime (hash seeds) but
// is NOT secure: honk has no entropy source on virt yet. A virtio-rng driver or
// the Zkr `seed` CSR is the Phase 1+ fix; until then crypto randomness is unsafe.
var rngState uint64

//go:linkname initRNG runtime/goos.InitRNG
func initRNG() { rngState = readTime() ^ 0x9e3779b97f4a7c15 }

//go:linkname getRandomData runtime/goos.GetRandomData
func getRandomData(b []byte) {
	if rngState == 0 {
		initRNG()
	}
	for i := range b {
		rngState += 0x9e3779b97f4a7c15
		z := rngState
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		z ^= z >> 31
		b[i] = byte(z)
	}
}
