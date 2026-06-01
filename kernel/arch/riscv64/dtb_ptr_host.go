// Off the noos/riscv64 target (host builds and `go test`) there is no firmware
// and no runtime.honkDTBPtr to alias, so the device tree pointer is simply
// unset: DTB returns nil and the board uses its defaults. This keeps package
// arch/riscv64 buildable and testable with the stock Go toolchain.

//go:build !(noos && riscv64)

package riscv64

var dtbPtr uintptr
