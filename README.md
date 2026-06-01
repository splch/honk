# Honk OS 🪿

[![CI](https://github.com/splch/honk/actions/workflows/ci.yml/badge.svg)](https://github.com/splch/honk/actions/workflows/ci.yml)

A small, educational operating system written in **pure Go** for **RISC-V 64-bit**.

Honk OS boots under [OpenSBI](https://github.com/riscv-software-src/opensbi) on
the QEMU `virt` machine and runs the **standard Go runtime — goroutines,
channels, and the garbage collector — directly in supervisor mode as the kernel
itself.** There is no C in the kernel: shell, drivers, and boot logic
are all Go.

A session looks like this — the goroutine count, memory, and version figures are
illustrative; they shift as the GC runs and track the toolchain:

```
      __      Honk OS
 \ __( o)<    pure Go on RISC-V, supervisor mode under OpenSBI
  (  > )      goroutines and a garbage collector are the kernel
  ~~~~~~      type 'help' for commands

honk> uname
Honk OS (pure Go) riscv64 S-mode/OpenSBI
honk> stats
goroutines 1, noos/riscv64, go1.24.5-embedded
honk> mem
heap 20272B, total 20568B, numGC 1
honk> gc
gc done
honk> mem
heap 20048B, total 20696B, numGC 2          # the GC is live and observable
honk> halt
honk... powering off.
```

## Quick start

Requirements: an existing Go (≥1.22) as a bootstrap compiler, `git`, and
`qemu-system-riscv64`.

```sh
make toolchain   # one-time: clone + patch + build the Embedded Go toolchain (~5 min)
make run         # build the kernel and boot it in QEMU  (quit with Ctrl-A then x)
make test        # non-interactive smoke test: pipes a command session, expects poweroff
```

## How it works

Plain Go can't target bare metal — it always assumes an OS underneath. Honk OS
builds on [**Embedded Go**](https://embeddedgo.github.io/) (`GOOS=noos`), which
runs the *real* Go runtime freestanding, plus a small vendored patch
([`toolchain/`](toolchain/)) that ports its RISC-V scheduler and trap handler from
**machine mode** to **supervisor mode** so the kernel runs cleanly under OpenSBI.

The boot path:

```
QEMU  ->  OpenSBI (M-mode firmware)  ->  Honk kernel (S-mode) @ 0x80200000
                                            |
                          runtime entry sets up a stack, BSS, g
                                            |
                          rt0_go: heap init, scheduler init
                                            |
                     SRET to U-mode  ->  main goroutine  ->  shell
```

**The key idea** (the same split [xv6](https://pdos.csail.mit.edu/6.1810/) uses):
the kernel, scheduler, and trap handlers run in **supervisor mode (S)**, while
goroutines run in **user mode (U)**. Embedded Go's runtime implements its
goroutine→kernel syscalls with the `ecall` instruction — and an `ecall` from
U-mode traps *down* into our S-mode handler (whereas an `ecall` from S-mode would
trap *up* to OpenSBI). Running goroutines in U-mode is what makes the whole
runtime work in supervisor mode. With no paging yet, OpenSBI's PMP already grants
U/S full access to RAM and the device MMIO pages, so user-mode goroutines can
touch the UART directly.

## Status

Working today: boot → full Go runtime (goroutines, channels, GC, maps) in S-mode
→ interactive shell over a UART → clean poweroff.

**The vision** — Honk as a minimal, readable, modern successor to xv6 — and the full
curriculum and roadmap live in **[docs/ROADMAP.md](docs/ROADMAP.md)**.

## Credits & references

- [Embedded Go](https://github.com/embeddedgo/go) — the freestanding Go runtime this builds on
- [TamaGo](https://github.com/usbarmory/tamago) and Go proposal [#73608](https://github.com/golang/go/issues/73608) — bare-metal Go
- [xv6-riscv](https://github.com/mit-pdos/xv6-riscv) — the S/U-mode kernel reference
- [OpenSBI](https://github.com/riscv-software-src/opensbi) and the [RISC-V SBI](https://github.com/riscv-non-isa/riscv-sbi-doc) / privileged specs
