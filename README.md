# honk

A modern pure-Go multiprocess RISC-V 64 operating system, built on
[TamaGo](https://github.com/usbarmory/tamago). honk maps OS concepts onto Go
language primitives (goroutine = process, `context` = lifecycle, type system +
capabilities = address space, `recover()` = fault containment, wazero =
untrusted sandbox, `io/fs.FS` = VFS) and writes low-level code only where the
hardware is intrinsic: SMP bring-up, device drivers, and the H-extension VMM.

honk boots as an **HS-mode payload under OpenSBI** on the QEMU `virt` machine.

## Build & run

```sh
make run              # build + boot under QEMU (Ctrl-A x to quit)
tools/smoke-test.sh   # build + boot + assert M0 output
```

Needs a host Go toolchain and `qemu-system-riscv64`. The TamaGo Go distribution
is downloaded and built automatically on first use.

## Layout

```
kernel/        the HS-mode Go program (the OS): boot, SMP demo, shell
kernel/proc/   process model: goroutine + context + capabilities (host-tested)
block/         block-device interface + in-memory device (host-tested)
kernel/kv/     crash-safe log-structured key/value store (host-tested)
kernel/vfs/    io/fs.FS over the kv store + overlay on the embedded core (host-tested)
board/virt/    QEMU virt board: startup, SMP, traps, PLIC, UART, PCIe/NVMe, virtio-blk, SBI
tools/         build.sh, run-qemu.sh, smoke-test.sh, mkboot (boot trampoline)
HONK.md        full design and roadmap
docs/STATUS.md what works today and what's next
GO.md RV64.md OS.md   language / hardware / domain references
```

Status: **Phase A complete (M0-M2); Phase B underway (M3-M4 done)** - HS-mode
boot under OpenSBI, SMP across all harts, an interrupt-driven UART console +
shell, a process model (goroutine + context + capabilities, `recover()` fault
domains), a persistent block device (NVMe-over-PCIe + virtio-blk fallback), and
a crash-safe log-structured kv store exposed as an overlay filesystem over an
embedded core. `make run` drops you at a `honk>` prompt; try `help`, `ls`,
`cat motd`. See `docs/STATUS.md`.
