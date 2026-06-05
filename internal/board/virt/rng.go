//go:build tamago && riscv64

package virt

import (
	"sync/atomic"
	_ "unsafe"

	"github.com/splch/honk/internal/sbi"
	"github.com/splch/honk/internal/virtio"
)

// entropy is the installed hardware entropy source, or nil before initEntropy.
// It is loaded atomically because getRandomData runs very early, before the
// scheduler is fully up.
var entropy atomic.Pointer[virtio.RNG]

// rngState backs a NON-cryptographic splitmix64 fallback used only before a real
// entropy device is installed.
var rngState uint64

//go:linkname initRNG runtime/goos.InitRNG
func initRNG() { rngState = readTime() ^ 0x9e3779b97f4a7c15 }

// getRandomData is the entropy source for BOTH the Go runtime (the ChaCha8 hash
// seed) and crypto/rand (crypto/internal/sysrand on tamago calls
// runtime.GetRandomData, which links here). Once initEntropy installs a
// virtio-rng device it returns real entropy; before that it falls back to a
// weak time-seeded generator — acceptable only for the runtime's early hash
// seed, never for keys (see DESIGN.md §15.4).
//
//go:linkname getRandomData runtime/goos.GetRandomData
func getRandomData(b []byte) {
	if dev := entropy.Load(); dev != nil {
		dev.Read(b)
		return
	}
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

// initEntropy finds a virtio-rng device and installs it as the system entropy
// source, replacing the weak fallback. It runs first in hwinit1 so the
// crypto/rand DRBG is seeded from real entropy before any TLS/SSH key exists.
func initEntropy() {
	for i := uintptr(0); i < 8; i++ {
		base := uintptr(virtioBase) + i*0x1000
		if !virtio.IsRNG(base) {
			continue
		}
		r, err := virtio.NewRNG(base)
		if err != nil {
			puts("honk/virt: virtio-rng init failed: ")
			puts(err.Error())
			puts("\n")
			return
		}
		entropy.Store(r)

		var sample [8]byte
		r.Read(sample[:])
		var v uint64
		for _, x := range sample {
			v = v<<8 | uint64(x)
		}
		puts("honk/virt: entropy from virtio-rng, sample ")
		printHex(v)
		puts("\n")
		return
	}
	// Fail closed: with no hardware entropy, crypto/rand would key the SSH host
	// key and all TLS material from the weak time-seeded fallback. On bare metal
	// we cannot block until seeded the way Linux getrandom(2) does (the source is
	// simply absent), so refuse to boot rather than serve predictable keys
	// (DESIGN.md §15.4). The fallback above remains only for the runtime's early,
	// pre-hwinit ChaCha8 hash seed, which is non-cryptographic.
	puts("honk/virt: FATAL no virtio-rng entropy source; refusing to boot (would generate predictable SSH/TLS keys)\n")
	sbi.Shutdown()
}
