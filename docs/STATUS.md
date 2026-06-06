# honk - build status

Living record of what is implemented and verified, and what is next. See
`HONK.md` for the full design and roadmap.

## Quickstart

```sh
make run        # build + boot under QEMU virt (Ctrl-A x to quit)
tools/smoke-test.sh   # build + boot + assert expected output (CI gate)
make vet        # go vet under the tamago toolchain
```

Requirements: a host Go toolchain, `qemu-system-riscv64`. The TamaGo Go
distribution (`tamago-go`) is auto-built on first use into the OS cache dir.

## Milestone status

| Milestone | State | Notes |
|---|---|---|
| **M0 boot + SMP + hello** | ✅ **complete** | Boots in HS-mode under OpenSBI on QEMU virt; **all harts run Go Ms** (`GOMAXPROCS=nharts`), scheduler spreads goroutines across every hart; clean SBI shutdown. Verified at `-smp 1/4/8`, boot-hart-agnostic. |
| **M1 IRQ + console + shell** | ✅ **complete** | honk S-mode trap vector (proper `sret`); UART RX interrupt → PLIC → ring → channel; interactive shell over the UART; S-mode exceptions print `scause`/`sepc`/`stval` and halt. |
| **M2 process model** | ✅ **complete** | `proc` table = goroutine + `context` (cancel = kill) + capabilities; `run`/`ps`/`kill`/`crash`/`reap`/`stress` shell commands; `recover()` fault domains (a panicking process is reaped, kernel + siblings survive); race-tested (`go test -race ./kernel/proc`) and stressed under `-smp 4`. |

**Phase A (foundation) is complete.** Next is Phase B (storage): M3 PCIe + NVMe.

## What boots today (M0+M1)

`make run` (defaults to `-smp 4`) boots and drops into an interactive shell:

```
honk: HS-mode boot ok  hart=0  dtb=0x9fe00000
honk: SMP up  harts=4  GOMAXPROCS=4
honk: SMP OK - goroutines ran on 4/4 harts [0 1 2 3]

honk: shell ready (type 'help')
honk> harts
harts: 4 online  GOMAXPROCS=4  this=hart 0
honk> exit
honk: shutting down
```

`exit` powers off via SBI (QEMU exits 0). The boot hart is whatever OpenSBI
picks (not always 0); honk starts all the others.

## Boot model (the load-bearing decisions)

honk targets QEMU `virt` and boots as an **HS-mode payload under OpenSBI**
(`-bios default`), the mainline RISC-V model and the prerequisite for the
Phase-E hypervisor. Getting there required solving three things TamaGo does not
do out of the box (TamaGo's only riscv64 board, `sifive_u`, boots in M-mode via
a custom bios):

1. **HS-mode startup, no M-mode CSRs.** The TamaGo runtime entry
   (`_rt0_riscv64_tamago`) is privilege-agnostic: it jumps to an application
   `goos.CPUInit`, which jumps to a bare `cpuinit` symbol. honk's `board/virt`
   supplies its own `cpuinit` (`boot_riscv64.s`) that enables the FPU via
   `sstatus`, sets the boot stack, stashes `a0`=hartid/`a1`=DTB, and jumps to
   `_rt0_tamago_start` - touching **no** M-mode CSR (OpenSBI owns them).
   Because honk does not import `tamago/riscv64`, its M-mode `cpuinit` is simply
   absent from the link; honk also provides the full `runtime/goos` overlay
   (RamStart/Size, Printk→SBI, Nanotime→`rdtime`, Hwinit0/1, RNG).

2. **The fw_dynamic entry quirk.** QEMU deliberately enters the OpenSBI payload
   at the image **load base, not the ELF entry point** (QEMU `hw/riscv/boot.c`).
   A Go ELF puts its header page at the load base and `_rt0` mid-`.text`, so
   OpenSBI cannot enter it directly. Fix: `tools/mkboot` emits a 24-byte
   position-fixed **trampoline** loaded by `-kernel` at 0x80200000 that jumps to
   honk's real entry; honk itself is loaded (ELF addresses honored) via
   `-device loader`. honk links at **0x80400000** to clear the trampoline.

3. **Toolchain GOROOT.** The `tamago-go` binary must run with its own `GOROOT`;
   `tools/build.sh` overrides any globally-set `GOROOT` (e.g. from mise/asdf),
   which otherwise selects the host compiler and fails with
   `compile: unknown goos tamago`.

Memory map: `RamStart=0x80400000` (honk's load address; must match `-T` in
build.sh). `RamSize` is **discovered, not hardcoded**: OpenSBI/QEMU place the
DTB at the top of usable RAM, so cpuinit puts the g0 stack just below the DTB
(`a1`) and `hwinit0` sets `RamSize = DTB - RamStart`. This is exact for any
QEMU `-m` (verified at 256M/512M/1G/2G), needs no FDT parsing, and leaves the
DTB intact. (Note: tamago's allocator grows the heap up to `g0.stack.lo`, so
the g0 stack must be at the top of RAM, above the heap.)

**Deferred hardening (tracked, not yet done):** the trap handler runs on the
interrupted goroutine's stack (relying on the runtime's nosplit red zone, as
TamaGo's own IRQ path does) rather than a dedicated per-hart interrupt stack;
and idle harts busy-poll rather than `WFI`. Neither is a correctness bug in the
tested configs, but both are worth closing as interrupts become frequent in
Phase B+.

## Process model (M2) - how it works

`kernel/proc` maps the OS "process" onto Go primitives (HONK.md §1) and is
pure Go, so it is race-tested on the host with `go test -race`:

- **Process = goroutine + `context`.** `Table.Spawn(name, caps, fn)` allocates a
  PID, makes a cancelable context, records the `*Proc` (PID, name, caps,
  start time, state), and runs `fn(ctx)` in a goroutine. `Kill(pid)` cancels
  the context (cancel = kill); cooperative `fn`s return on `ctx.Done()`.
- **`recover()` fault domain.** The per-process goroutine defers a `recover()`,
  so a panic is contained: the process is reaped as `panicked` and the kernel
  and every sibling keep running (the `crash` command demonstrates it).
- **Capabilities.** Each process carries a `Caps` set (`console`/`net`/`block`/
  `proc`); `proc.Self(ctx).Can(cap)` is the query the kernel gates on. `ps`
  shows each process's grant. (Real authority is the interface values handed to
  a process; `Caps` is the bookkeeping.)

The shell exposes `run`/`ps`/`kill`/`crash`/`reap`/`stress`. `stress N` spawns
N short-lived processes that run across all harts, exercising the table under
SMP; `go test -race ./kernel/proc` is the host-side race gate (both run by
`tools/smoke-test.sh`). Uncooperative (tight-loop, unkillable) code is a job
for the WASM/VM tiers, not a trusted goroutine.

## Console + traps (M1) - how it works

TamaGo's riscv64 trap handler is M-mode and never does a real trap return, so
honk installs its **own** S-mode handler (`trapEntry`, trap_riscv64.s) in
`stvec` on every hart (via cpuinit/secondaryEntry):

- **Exceptions** (`scause >= 0`) are fatal: `handleFault` prints
  `scause`/`sepc`/`stval` via the raw SBI console (alloc-free, trap-safe) and
  powers off. The shell's `fault` command (an `EBREAK`, delegated to S-mode)
  exercises this.
- **Interrupts** (`scause < 0`) are serviced synchronously: the handler saves
  the integer caller-saved registers, calls the nosplit, FP-free `handleIRQ`
  (PLIC claim → drain UART RX into a lock-free ring → PLIC complete), restores,
  and `sret`s. Because it fully drains + completes, the return does not storm.

Interrupts are enabled (`sstatus.SIE` + `sie.SEIE`) on the **boot hart only**;
the UART (PLIC source 10) is routed to that hart's S-context, so there is a
single interrupt consumer and no cross-hart claim races. Secondaries set only
`stvec` (exception safety). A reader goroutine moves bytes from the ring onto
the `virt.Console()` channel, and `kernel/shell.go` provides a small line shell
(`help`/`harts`/`uptime`/`mem`/`echo`/`fault`/`exit`). Output stays on the SBI
console (`printk`).

*Known benign caveat:* a byte that arrives before honk's console is initialized
may be consumed by OpenSBI's own UART init. Interactive input (typed after the
prompt) is unaffected; the smoke test sends a leading newline to absorb it.

## SMP (M0) - how it works

The project's stated #1 risk - per-hart Go `M` bring-up - is solved, and
**without a `tamago-go` fork**. The TamaGo runtime already exposes the exact
hook the working amd64 SMP support uses:

1. `board/virt/smp.go` `InitSMP()` enumerates harts via SBI HSM
   `hart_get_status`, starts each non-boot hart with SBI HSM `hart_start` into
   `secondaryEntry` (boot_riscv64.s). Each secondary records its id in `tp`,
   enables its FPU, signals readiness, and **parks** in a register-only spin
   loop (its stack is undefined until it adopts one).
2. `InitSMP` then sets `goos.Task`/`goos.ProcID` and calls
   `runtime.GOMAXPROCS(nharts)`.
3. When the scheduler needs another M, the runtime calls
   `newosproc` → `goos.Task(sp, mp, g0, mstart)`. honk's `honkTask` publishes
   that stack + g0 + entry into the next parked hart's slot (release store); the
   park loop adopts them and jumps into `runtime.mstart`, becoming a full Go M.

No IPIs are needed: idle Ms busy-spin in the runtime's `semasleep` (pure
shared-memory atomics), and the initial hand-off is a polled, fenced slot.
`tp` holds each hart's id (the runtime never touches `tp`), so
`virt.CurrentHart()` / `goos.ProcID` report the live hart. The boot hart is
whatever OpenSBI selects; `InitSMP` skips it and starts all the others.

`maxHarts` (currently 8) bounds the hand-off tables; raise it and re-test for
larger `-smp` values.

## Next: M3 (Phase B - storage)

PCIe ECAM enumeration and an NVMe driver (with a virtio-blk fallback)
implementing a `BlockDevice` interface. RAM size is now DTB-derived; hart count
is SBI-probed and MMIO bases are board constants (standard for a fixed board),
so a full FDT parser is optional until honk targets boards beyond QEMU virt.
