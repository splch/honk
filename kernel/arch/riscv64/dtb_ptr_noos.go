// On the noos/riscv64 target the boot device tree pointer comes from the runtime
// entry stub (toolchain S-mode patch), which saves the address the firmware
// passed in a1 across BSS-clear into runtime.honkDTBPtr. Aliasing that symbol
// here lets DTB return the live blob, so the board discovers real hardware
// instead of falling back to the QEMU 'virt' defaults.

//go:build noos && riscv64

package riscv64

import _ "unsafe" // for the go:linkname below

//go:linkname dtbPtr runtime.honkDTBPtr
var dtbPtr uintptr
