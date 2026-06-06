// honk - the hypervisor tier (Phase E): launch a guest VM via the RISC-V
// H-extension. This is the kernel-side glue; the world switch, G-stage paging,
// SBI emulation, and timer/injection live in board/virt (vmm.go) and the
// encodable guest/page-table logic in kernel/vmm.

//go:build tamago && riscv64

package main

import (
	"fmt"

	"honk/board/virt"
	"honk/kernel/vmm"
)

// timerTicks is how many timer interrupts the M12 timer guest takes before it
// shuts down (each prints one '*').
const timerTicks = 5

// vmcmd is the shell's `vm` command. With no argument it runs the M11 demo: a
// VS-mode guest that prints a line via emulated SBI and shuts down (proving
// H-ext enable, G-stage paging, and trap-and-emulate). `vm timer` runs the M12
// demo: a guest that arms an SBI timer and, on each VS-timer interrupt honk
// injects, prints a '*' and reprograms the timer, then shuts down after a few
// ticks - proving the timer, interrupt injection, and preemption path. Guest
// output appears inline on honk's console.
func vmcmd(fields []string) {
	if len(fields) > 1 && fields[1] == "timer" {
		guest := vmm.TimerGuest('*', timerTicks)
		fmt.Printf("vm: launching a timer guest (%d bytes): %d ticks via VS-timer injection\n",
			len(guest), timerTicks)
		fmt.Print("vm: guest ticks: ")
		reason := virt.RunGuest(guest) // the '*' ticks print inline as they fire
		fmt.Printf("\nvm: %s\n", reason)
		return
	}

	guest := vmm.DemoGuest("honk: hello from a guest VM\n")
	fmt.Printf("vm: launching a VS-mode guest (%d bytes) under the H-extension\n", len(guest))
	fmt.Printf("vm: %s\n", virt.RunGuest(guest))
}
