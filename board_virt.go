//go:build tamago && virt

package main

// Phase 1 board: honk's own virt support, booting as an S-mode payload under
// OpenSBI. Build with: make TARGET=virt
import _ "github.com/splch/honk/internal/board/virt"

const boardName = "qemu/virt"
