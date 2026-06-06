// mkimage builds honk's immutable core image: it packs a directory of files
// into a Merkle-tree'd, Ed25519-signed image (kernel/image format) that the
// kernel verifies at boot and serves read-only.
//
// The build is reproducible: files are laid out in sorted order and signed with
// honk's fixed development seed (the matching public key is embedded in the
// kernel as image.DevPublicKey). This dev key signs only local QEMU images; a
// real board fuses its key into OTP and signs with an offline private key.
//
// Usage:
//
//	mkimage [-version N] <core-dir> <out.img>
//
// -version sets the anti-rollback security version (default 1). Bump it for an
// update so A/B selection prefers the new image; the kernel refuses any image
// below the floor it has recorded.
package main

import (
	"crypto/ed25519"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"honk/kernel/image"
)

// devSeed is honk's fixed development signing seed. It must match the public
// key embedded as image.DevPublicKey (kernel/image/key.go). Regenerating one
// means regenerating the other.
func devSeed() []byte {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(0x40 + i)
	}
	return seed
}

func main() {
	version := flag.Uint64("version", 1, "anti-rollback security version")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: mkimage [-version N] <core-dir> <out.img>")
		os.Exit(2)
	}
	dir, out := flag.Arg(0), flag.Arg(1)

	files, err := collect(dir)
	if err != nil {
		fatal(err)
	}
	if len(files) == 0 {
		fatal(fmt.Errorf("no files under %s", dir))
	}

	priv := ed25519.NewKeyFromSeed(devSeed())
	img := image.Build(files, *version, func(msg []byte) []byte { return ed25519.Sign(priv, msg) })

	if err := os.WriteFile(out, img, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("mkimage: %s -> %s (%d files, %d bytes, version %d)\n",
		dir, out, len(files), len(img), *version)
}

// collect reads every regular file under dir into a map keyed by its
// slash-separated path relative to dir.
func collect(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	return files, err
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "mkimage:", err)
	os.Exit(1)
}
