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
| M1 IRQ + console + shell | ⬜ next | Trap-vector hook → channels; NS16550A UART; tamago-example/shell. |
| M2 process model | ⬜ | `proc` table = goroutine + context + caps; `recover()` domains. |

## What boots today (M0)

`make run` (defaults to `-smp 4`) produces:

```
honk: entered main
   __     honk
 >(o )___   pure-Go RISC-V64 OS
  (  ._> /  HS-mode under OpenSBI
   '---'
honk: HS-mode boot ok  hart=0  dtb=0x9fe00000
honk: SMP up  harts=4  GOMAXPROCS=4
honk: SMP OK - goroutines ran on 4/4 harts [0 1 2 3]
honk: goroutine+channel round-trip -> "honk"
honk: M0 ok - clean shutdown
```

QEMU exits 0 via SBI System Reset (not the smoke-test watchdog). The boot hart
is whatever OpenSBI picks (not always 0); honk starts all the others.

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

Memory map (sized for `-m 512M`, hardcoded until DTB parsing lands):
`RamStart=0x80400000`, `RamSize=0x1DA00000` (ends below the DTB at ~0x9fe00000
so the runtime arena/boot stack never clobber it).

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

## Next: M1

IRQ→channel plumbing (trap-vector hook), an NS16550A UART driver, and a shell.
Cheap groundwork that M1 also needs: DTB parsing (hart count, RAM size, MMIO
bases), which would also replace the hardcoded memory map and hart probing here.
