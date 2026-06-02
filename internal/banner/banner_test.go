package banner

import "testing"

func TestLine(t *testing.T) {
	tests := map[string]struct {
		version, goarch, board string
		want                   string
	}{
		"sifive_u": {"go1.26.3", "riscv64", "qemu/sifive_u", "honk: go1.26.3 riscv64 (qemu/sifive_u)"},
		"virt":     {"go1.27.0", "riscv64", "qemu/virt", "honk: go1.27.0 riscv64 (qemu/virt)"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := Line(tc.version, tc.goarch, tc.board); got != tc.want {
				t.Errorf("Line() = %q, want %q", got, tc.want)
			}
		})
	}
}
