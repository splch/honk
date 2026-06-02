//go:build tamago && sifive_u

package main

// Phase 0 board: the existing TamaGo RISC-V port (machine-mode boot via the
// trampoline BIOS). Build with: make TARGET=sifive_u
import _ "github.com/usbarmory/tamago/board/qemu/sifive_u"

const boardName = "qemu/sifive_u"
