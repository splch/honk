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
internal/fdt/        # device-tree parser (host-tested)
internal/ring/       # lock-free SPSC byte queue (host-tested, -race)
internal/sbi/        # SBI firmware ecall wrappers
internal/mmio/       # volatile MMIO accessors (asm)
internal/uart/       # NS16550A driver
internal/plic/       # PLIC interrupt-controller driver
internal/virtio/     # virtio-mmio v2 block + net drivers
internal/inet/       # tiny IPv4 stack: Ethernet/ARP/IPv4/ICMP (host-tested)
internal/board/virt/ # S-mode-under-OpenSBI board: runtime seam, trap, vm, console, disk, net
boot/virt/           # 20-byte load-base trampoline (trampoline.s)
boot/sifive_u/       # Phase 0 trampoline BIOS (bios.s + bios.ld)
Makefile             # toolchain + build + qemu + smoke + test (TARGET=virt|sifive_u)
GO.md RV64.md DESIGN.md
```

honk has now walked the whole RV64.md bringup order. The one piece intentionally
*not* built is a full TCP stack for stdlib `net`/`net/http`: the realistic option
is gVisor's netstack behind `net.SocketFunc`, which would roughly 10× the binary
and add a large dependency — against honk's small-and-simple ethos, so it is left
as a deliberate opt-in (DESIGN.md §8/§11).

## Status

**Phase 1 (current)** — boots to full Go in **supervisor mode under OpenSBI** on
QEMU `virt` (honk's own `internal/board/virt`): S-mode `cpuinit`, SBI console
(`internal/sbi`), `time`-CSR clock, goroutines + timer, and device-tree hardware
discovery (`internal/fdt`, host-unit-tested) — e.g. it reports `riscv-virtio,qemu,
1 hart(s), RAM 512 MiB, timebase 10 MHz` at boot. It installs an S-mode trap
handler that reports faults (`scause`/`sepc`/`stval`) and halts, idles the hart
in `wfi` between timer deadlines (no busy-poll), and runs under an
**identity-mapped Sv39 page table enforcing W^X** (kernel text `R|X`, all else
`R|W`-no-exec, A/D preset). It owns the **NS16550A UART** for interrupt-driven
input driving a tiny **shell** (`help`, `ls`, `cat <file>`, `write <file>
<text>`) at the `honk>` prompt: a keystroke raises a PLIC interrupt that wakes
the hart from `wfi`, `idle` drains it into a lock-free ring, and a console
goroutine runs commands. `ls`/`cat`/`write` use a **writable FAT32 filesystem**
(`diskfs/go-diskfs`) on a **virtio-blk** disk — honk formats a blank image on
first boot, and files written there **persist across reboots** (and the image
mounts on the host). The `net`/`rand` commands ARP+ping the gateway
(`internal/inet`) and read `crypto/rand` (seeded by a **virtio-rng** entropy
source).

Toward a daily driver (DESIGN.md §15), honk now runs a full **gVisor TCP/IP
stack**: setting `net.SocketFunc` routes all of stdlib `net` through it, so a
plain `net/http` server on honk is reachable from your machine
(`curl http://127.0.0.1:8080/`), and you can **`ssh` in** to the `honk>` shell
(`ssh -p 2222 honk@127.0.0.1` — `gliderlabs/ssh`, ed25519 host key from
`crypto/rand`). It also makes **outbound TLS** connections: the shell's
`fetch https://example.com` resolves DNS, dials over the stack, and verifies the
certificate against embedded Mozilla roots; `ntp` syncs the clock, `date` shows
it. Core honk is ~2.3 MB; the full networked build (gVisor + `net/http` + SSH +
TLS roots) is ~12 MB.

**Phase 0** — also runs on QEMU `sifive_u` (M-mode trampoline; the existing
TamaGo RISC-V port), kept as a second board to keep the driver boundaries honest.
See DESIGN.md §4 and §14.
