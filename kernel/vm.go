// honk - the hypervisor tier (M11): launch a guest VM via the RISC-V
// H-extension. This is the kernel-side glue; the world switch, G-stage paging,
// and SBI emulation live in board/virt (vmm.go) and the encodable guest/page-
// table logic in kernel/vmm.

//go:build tamago && riscv64

package main

import (
	"fmt"

	"honk/board/virt"
	"honk/kernel/vmm"
)

// vmcmd is the shell's `vm` command: build a tiny VS-mode guest that prints a
// line via emulated SBI and then shuts down, run it under the H-extension, and
// report why it exited. The guest's output appears inline on honk's console -
// proof that real guest instructions executed, were translated through hgatp,
// trapped to honk, and were emulated.
func vmcmd() {
	guest := vmm.DemoGuest("honk: hello from a guest VM\n")
	fmt.Printf("vm: launching a VS-mode guest (%d bytes) under the H-extension\n", len(guest))
	fmt.Printf("vm: %s\n", virt.RunGuest(guest))
}
