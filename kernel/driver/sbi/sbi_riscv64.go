// Go declarations for the SBI console primitives implemented in assembly
// (sbi_riscv64.s) on the noos/riscv64 target. Off-target, sbi_stub.go provides
// stand-ins instead, so this pair and the stub are mutually exclusive.

//go:build noos

package sbi

func putchar(c byte)
func getchar() int
