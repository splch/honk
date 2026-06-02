# honk

A small **RISC-V 64-bit operating system written in pure Go** — a
[TamaGo](https://github.com/usbarmory/tamago) unikernel in which the Go runtime
*is* the kernel, goroutines are the tasks, and channels are the IPC. No
user/kernel split, no process model; just drivers plus the full Go runtime and
standard library.

- **Why it's built this way** → [DESIGN.md](./DESIGN.md)
- **The hardware model** → [RV64.md](./RV64.md)
- **The Go we write** → [GO.md](./GO.md)

## Quickstart

Needs a host Go (for bootstrap + host tests), QEMU, and a RISC-V ELF toolchain
for the boot trampolines (`dtc` is only needed for the sifive_u board):

```sh
brew install qemu dtc riscv64-elf-gcc      # macOS

make toolchain     # build the tamago-go compiler into ~/.cache/tamago-go (~40s, once)
make qemu          # boot honk in QEMU virt, S-mode under OpenSBI (quit: Ctrl-A x)
make qemu TARGET=sifive_u   # the Phase 0 board (machine-mode, via a BIOS)
```

Expected boot output:

```
honk: go1.26.3 riscv64 (qemu/virt)
  honk: task 1 reporting in
  ...
honk #1 — up 3s, N goroutines
```

Other targets: `make smoke` (non-interactive boot + banner check, for CI),
`make test` (host-side unit tests with the race detector), `make qemu-gdb`,
`make vet`, `make fmt`, `make clean`, `make help`.

> Already have a `tamago-go` build elsewhere? Skip `make toolchain` and pass
> `TAMAGO=/path/to/tamago-go/bin/go` to any target.

## Layout

```
main.go              # honk entry (//go:build tamago): banner + tasks + heartbeat
board_virt.go        # //go:build virt     — selects the virt board (default)
board_sifive_u.go    # //go:build sifive_u — selects the Phase 0 board
internal/banner/     # hardware-independent, host-testable pure logic
internal/board/virt/ # S-mode-under-OpenSBI board: the runtime seam (DESIGN.md §6)
boot/virt/           # 20-byte load-base trampoline (trampoline.s)
boot/sifive_u/       # Phase 0 trampoline BIOS (bios.s + bios.ld)
Makefile             # toolchain + build + qemu + smoke + test (TARGET=virt|sifive_u)
GO.md RV64.md DESIGN.md
```

Planned next (DESIGN.md §11): drivers under `internal/{fdt,trap,plic,clint,vm,uart,virtio}`
and an `internal/sbi` ecall package, in RV64.md's bringup order.

## Status

**Phase 1 (current)** — boots to full Go in **supervisor mode under OpenSBI** on
QEMU `virt` (honk's own `internal/board/virt`): S-mode `cpuinit`, SBI console
(`internal/sbi`), `time`-CSR clock, goroutines + timer, and device-tree hardware
discovery (`internal/fdt`, host-unit-tested) — e.g. it reports `riscv-virtio,qemu,
1 hart(s), RAM 512 MiB, timebase 10 MHz` at boot. It installs an S-mode trap
handler that reports faults (`scause`/`sepc`/`stval`) and halts, and idles the
hart in `wfi` between timer deadlines (no busy-poll). Next: a PLIC driver →
interrupt-driven NS16550A UART, then Sv39 W^X (DESIGN.md §11, RV64.md bringup order).

**Phase 0** — runs on QEMU `sifive_u` (M-mode trampoline; the existing TamaGo
RISC-V port). **Phase 1** (next) — a `virt` board package booting under OpenSBI,
then drivers in RV64.md's bringup order. See DESIGN.md §4 and §14.
