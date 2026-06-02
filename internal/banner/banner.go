// Package banner formats honk's boot banner.
//
// It is deliberately hardware-independent (no board imports, no build
// constraints) so it compiles and unit-tests on the host with the stock Go
// toolchain — the host-testability win described in DESIGN.md §10 and GO.md §16.
package banner

import "fmt"

// Line returns honk's one-line boot banner for the given runtime identity,
// e.g. "honk: go1.26.3 riscv64 (qemu/sifive_u)".
func Line(version, goarch, board string) string {
	return fmt.Sprintf("honk: %s %s (%s)", version, goarch, board)
}
