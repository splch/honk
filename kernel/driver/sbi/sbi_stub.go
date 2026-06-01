// Host stubs so package sbi builds and its callers test off-target, where there
// is no SBI firmware to ecall into. Putc is dropped; Getc would block, so it is
// never called off-target.

//go:build !(noos && riscv64)

package sbi

func putchar(byte) {}
func getchar() int { return -1 }
