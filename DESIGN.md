# DESIGN.md — honk: a small RISC-V 64-bit OS in pure Go

The design rationale for **honk**, a modern, best-practice, small, and simple
RV64 operating system written in pure Go. This document is the third pillar
alongside [GO.md](./GO.md) (how to write idiomatic modern Go) and
[RV64.md](./RV64.md) (how RV64 boots, traps, pages, and talks to devices). It
ties the two together into a single buildable plan and — more importantly —
makes the one architectural decision that RV64.md deliberately leaves open.

> **Vantage:** mid-2026, Go 1.26.x, QEMU 11.x. Facts here were verified against
> primary sources (the RISC-V Privileged/SBI specs, the TamaGo project and its
> Go-distribution fork, the upstream Go `GOOS=none` proposal, the QEMU `virt`
> docs) and against working code (the TamaGo `riscv64` port, the `kotama` RV64
> unikernel demonstrator, the MIT `Biscuit` research kernel). Confidence is
> implicit unless a value is flagged version-dependent or nascent.

> Convention, borrowed from the sibling docs:
> - **Decision** = a choice this project commits to, with rationale.
> - **Rule** = guaranteed by a spec or toolchain.
> - **Practice** = idiom/best-practice consensus.
> - **Contested / nascent** = authorities disagree, or the capability is still
>   landing upstream — pinned here so we don't build on sand.

---

## Table of contents

0. [Bottom line](#0-bottom-line)
1. [The fork RV64.md hides: unikernel vs. multiprocess kernel](#1-the-fork-rv64md-hides-unikernel-vs-multiprocess-kernel)
2. [The runtime problem, and why TamaGo](#2-the-runtime-problem-and-why-tamago)
3. [Alternatives considered](#3-alternatives-considered)
4. [Target and boot model](#4-target-and-boot-model)
5. [Architecture](#5-architecture)
6. [The seam: the runtime shim surface](#6-the-seam-the-runtime-shim-surface)
7. [What "pure Go" means here](#7-what-pure-go-means-here)
8. [The concurrency and interrupt model](#8-the-concurrency-and-interrupt-model)
9. [Memory, paging, and W^X](#9-memory-paging-and-wx)
10. [Best practices from GO.md, applied](#10-best-practices-from-gomd-applied)
11. [Feature roadmap, mapped to RV64.md Appendix G](#11-feature-roadmap-mapped-to-rv64md-appendix-g)
12. [Limitations and the hard parts](#12-limitations-and-the-hard-parts)
13. [Repository skeleton and build](#13-repository-skeleton-and-build)
14. [Milestones](#14-milestones)
15. [References](#15-references)

---

## 0. Bottom line

**Build a Go *unikernel* on the TamaGo runtime, booting as an S-mode payload
under OpenSBI on QEMU `virt`. The "from-scratch kernel" of RV64.md becomes a
single small board-support package; everything above the drivers is the
unmodified Go runtime and standard library.**

In one sentence: *don't write a kernel that hosts Go — let the Go runtime be the
kernel.* The runtime's scheduler is the process scheduler, goroutines are the
threads, channels are the IPC, and the garbage collector is the memory manager.
That deletes the hardest third of RV64.md (Part 8: user processes, trampolines,
context switching, syscalls) and keeps the tractable, fun two-thirds (boot, SBI,
traps, timer, Sv39, atomics, virtio).

The whole approach rests on one external dependency — the **TamaGo** modified Go
distribution — so Milestone 0 is to *prove that dependency works on this machine
before committing further* (§14).

---

## 1. The fork RV64.md hides: unikernel vs. multiprocess kernel

RV64.md describes the **xv6/Linux model**: an S-mode kernel that hosts isolated
U-mode processes, each with its own page table, reached through syscalls and a
trampoline (its Part 8). That is a *multiprocess kernel*. But "an OS in pure Go"
can mean two structurally different artifacts, and the choice dominates every
later decision.

| | **(A) Unikernel / library-OS** | **(B) Multiprocess kernel** |
|---|---|---|
| Address spaces | one (the Go program *is* the machine) | kernel + N isolated user processes |
| "Processes" | goroutines | U-mode programs + a syscall ABI |
| Scheduler | the Go runtime's | yours, in S-mode |
| IPC | channels | pipes/syscalls over a trap boundary |
| RV64.md coverage | Parts 0–7 (boot → devices) | + **Part 8** (trapframe, trampoline, `swtch`) |
| Pure-Go feasibility | **high** | low |
| "Small and simple" | **yes** | no (research-scale) |
| Prior art on RV64 | TamaGo, `kotama` — **runs today** | none in pure Go |
| Prior art at all | many (TamaGo ecosystem) | `Biscuit` (MIT PDOS) — x86-64 only, Go 1.10 fork, last touched 2022 |

**Decision: (A), the unikernel.** Rationale:

- **It satisfies the brief.** "Modern, best-practice, small, simple" is the exact
  profile of a Go unikernel and the exact opposite of a multiprocess kernel. The
  `kotama` demonstrator is a complete interactive RV64 Go system in **6 MiB of
  RAM** with **~888 KiB of text**.
- **(B) fights the language.** A Go kernel that hosts non-Go processes has to run
  the Go runtime in trap context, manage page tables the GC doesn't know about,
  and survive interrupts landing mid-GC at unsafe points. `Biscuit` proves this
  is *possible* — and also proves it is neither small nor simple: it is a frozen
  research fork of the Go runtime (~tens of thousands of LOC of kernel) that only
  ever targeted x86-64 and stopped tracking Go in 2022.
- **(A) is mostly already built.** The Go runtime already solves scheduling,
  preemption-points, stack growth, memory allocation, and GC. RV64.md Part 8 is
  *re-implementing those four things badly in a new language*. Skip it.

**Consequence — the honest trade:** honk has **no hardware-enforced isolation
between "tasks."** A buggy goroutine can corrupt another's memory the same way
any Go program can. If a real privilege boundary is later required, the
Go-flavored answer is **not** to build Part 8 — it is **GoTEE** (a TamaGo sibling
that runs an isolated Go "applet" in U-mode behind a tiny syscall interface using
PMP, and already supports `riscv64`). That keeps us in the unikernel world while
adding one boundary where it's actually wanted (§12).

---

## 2. The runtime problem, and why TamaGo

The reason "pure Go on bare metal" is hard at all: the stock `gc` runtime assumes
a hosted OS. It calls `mmap`/`munmap` for memory, `futex`/`clone` for the
scheduler's M:N threads, signals for async preemption and `time`, and `write(2)`
for output. Bare metal has none of those. Something must supply them.

**Decision: use TamaGo (`GOOS=tamago`).** TamaGo is a *minimal, actively
maintained* fork of the **vanilla** Go toolchain (the `tamago-go` distribution,
currently tracking Go 1.26.x — i.e. the same release line as the host's
`go1.26.3`) plus a support library (`github.com/usbarmory/tamago`). It keeps the
**entire runtime, garbage collector, goroutine scheduler, and 100% of the
standard library**, and it already has a working **`riscv64`** port that runs in
QEMU. It is the basis of the open upstream proposal to add a first-class
`GOOS=none` target (golang/go#73608), which was reopened by a Go-team member and
has broad community support.

Why this specific fork and not the others:

- **Full-fat Go, not a dialect.** Unlike compile-to-LLVM approaches, TamaGo is
  the real `gc` compiler and runtime. `reflect`, `encoding/json`, `net/http`,
  generics, `iter`, `slog` — everything in GO.md §14 — works unchanged. "Modern
  best-practice Go" *is the language*, so the foundation must be the real one.
- **The seam is tiny.** A board package supplies only ~8 functions and 3 vars
  (§6). Everything in RV64.md Parts 0–7 collapses into implementing that seam
  plus drivers — and the existing `riscv64` port already implements most of it.
  We are *porting a board*, not inventing a runtime.
- **It is the maintained version of "roll your own."** The third option below is
  to fork the Go runtime ourselves to add `GOOS=none`. That is *exactly what
  TamaGo already is*, maintained across Go releases by people who do it for a
  living. Re-doing it violates every constraint in the brief.
- **Host-side testing for free** (this is the sleeper feature — see §10).

**Runtime facts to design around** (verified from the TamaGo internals notes;
these shape the whole architecture):

| Property | Consequence for honk |
|---|---|
| No asynchronous preemption | a CPU-bound goroutine with no calls can starve others; document it, avoid it |
| `M ≤ P`, one persistent OS thread per CPU | SMP needs the `Task` hook (nascent); start single-hart |
| Only `write` syscall (stdout/stderr) | console is the one privileged sink; everything else is drivers |
| Filesystem = in-memory, NaCl-style | real storage = a virtio-blk driver + a Go `io/fs` |
| `net` inert until `net.SocketFunc` set | networking = a virtio-net driver + a userspace TCP/IP stack |
| Allocator is plan9-style, no `brk` | memory layout is `RamStart`/`RamSize`/`RamStackOffset` (§6, §9) |
| Heap-arena/page-chunk sizes now tunable | "small RAM" is real (`kotama`: 6 MiB); see §9 |
| Interrupts → goroutine wake via `os/signal` | the elegant core of the model (§8) |
| DMA buffers live outside the GC heap | virtio rings/buffers go in a reserved DMA region (§9) |

---

## 3. Alternatives considered

| Option | What it is | Verdict |
|---|---|---|
| **TamaGo `GOOS=tamago`** | maintained vanilla-Go fork; full runtime + stdlib; `riscv64` port in QEMU today | **chosen** — full Go, tiny seam, RV64-proven, upstream-track |
| TinyGo | LLVM-based compiler + its own runtime; partial `reflect`/stdlib | strong, but a *different* Go; 32-bit-leaning RISC-V; bare-metal multicore `virt` only landed in 2025. Good fallback, wrong fit for "modern best-practice Go" |
| embeddedgo (`GOOS=noos`) | another bare-metal Go runtime; real pure-Go interrupt handlers (`//go:interrupthandler`) | MCU-focused (STM32, RP2040, N64/MIPS); not aimed at RV64 application cores / QEMU `virt` |
| Roll our own `GOOS=none` fork | patch the Go runtime ourselves | this *is* TamaGo; re-doing it fails "small/simple/best-practice" |
| Stock Go, hand-stubbed runtime | implement the OS interface inline | undocumented, unstable "runtime Go" subset; you'd reinvent TamaGo and chase every Go release |
| `Biscuit`-style kernel in Go | multiprocess POSIX kernel, Go runtime in S-mode | the (B) path of §1; research-scale, x86-64, frozen — rejected |

**Contested / watch:** if the upstream `GOOS=none` proposal (golang/go#73608)
lands in a future Go release, migrate honk's board package from `GOOS=tamago` to
`GOOS=none`. The seam (§6) is designed to be close to what that proposal
standardizes (`runtime/goos` overlay, `os/signal`-based interrupts), so the
migration should be mechanical.

---

## 4. Target and boot model

RV64.md targets **QEMU `virt` + OpenSBI**, with the kernel loaded as an **S-mode
payload at `0x80200000`** (auto-generated DTB in `a1`, `a0`=hartid, MMU off, SBI
available). This is the modern, real-hardware-like path. TamaGo today ships only
a **`sifive_u`** board (M-mode-style boot, a custom mini-BIOS, a hand-written
DTB, often `GOSOFT=1`).

**Decision: a two-phase target.**

### Phase 0 — borrow `sifive_u` to de-risk the toolchain (days)

Build an unmodified TamaGo app against the existing `sifive_u` board and run it
in QEMU. Goal: see *full Go* — goroutines, `fmt`, `time`, a `net/http` handler —
execute on RV64 **before writing any driver code**. This validates the single
load-bearing dependency. Reference invocation shape (from the TamaGo `riscv64`
examples; exact flags pinned in the Makefile):

```sh
GOOS=tamago GOARCH=riscv64 GOOSPKG=github.com/usbarmory/tamago \
  $TAMAGO build -ldflags "-T 0x80010000 -R 0x1000" -o honk.elf .
qemu-system-riscv64 -machine sifive_u -m 512M -nographic \
  -serial stdio -net none -dtb sifive_u.dtb -bios bios.bin -kernel honk.elf
```

### Phase 1 — the real project: a new `virt` board, S-mode under OpenSBI

Write a new board package for QEMU `virt` that boots the way RV64.md describes:

```sh
qemu-system-riscv64 -machine virt -m 512M -smp 1 -bios default \
  -nographic -monitor none -serial stdio -no-reboot -kernel honk.elf \
  -device loader,file=trampoline.bin,addr=0x8020f000   # see the crux note below
# OpenSBI (M-mode) mret's into the kernel in S-mode:
#   a0 = hartid, a1 = DTB phys addr, satp = 0, sstatus.SIE = 0
```

This is where **all of RV64.md earns its keep** and where the novel,
upstreamable contribution lives. The one genuinely tricky porting task:

> **The crux of Phase 1.** TamaGo's existing `riscv64` reset path (`rt0` /
> `Hwinit0`) assumes an **M-mode** boot — it touches M-level CSRs. Under OpenSBI
> we enter in **S-mode**, so the reset path must be adapted to the S-level CSRs
> (`sstatus`, `stvec`, `sie`, `satp`) and must **skip** the M-mode trap
> delegation OpenSBI already performed (RV64.md §2.3 lists exactly what is
> delegated). Console, timer, poweroff, and reboot then route through SBI
> (RV64.md §2.2) instead of raw CLINT/M-mode, which is *less* code than the
> bare-metal `sifive_u` path, not more.

> **Validated (Milestone 1, see §14).** Two findings from building it: (1) the
> M-mode setup lives in the *board/CPU layer*, not the runtime — TamaGo's
> `rt0_tamago_riscv64.s` exposes both an M-level entry (→ board `cpuinit`) and an
> explicit `_rt0_tamago_start` S/U entry — so honk supplies its own ~12-line
> S-mode `cpuinit` (set `sstatus.FS`, mask `sie`, set `sp`, jump to
> `_rt0_tamago_start`) and the runtime is **never forked**, exactly as hoped.
> (2) `virt` does **not** boot purely on `e_entry`: QEMU 11 hands OpenSBI the
> kernel's *load base*, not `e_entry`, and the Go linker never puts `_rt0` there.
> So honk needs a **20-byte trampoline** (`li t0,_rt0; jr t0`) loaded onto the
> runtime-dead ELF-header page at the load base via `-device loader` — far
> smaller than a firmware BIOS, and OpenSBI stays in place for the SBI
> console/timer/poweroff. honk boots to full Go in S-mode this way.

**Why `virt` over staying on `sifive_u`:** `virt` is the generic, modern board
(auto DTB, 8 virtio-mmio slots, PCIe, the RV64.md memory map), it needs only the
20-byte load-base trampoline above (not a full M-mode firmware, as `sifive_u`
requires), and an S-mode-under-OpenSBI port is directly useful upstream. `sifive_u`
stays as the Phase 0 stepping-stone and a second board to keep the driver
interfaces honest (GO.md §8: don't generalize an interface until there's a real
second implementation).

**Float (gotcha).** `virt`'s default CPU exposes `D`, so `-mabi=lp64d` hardfloat
is fine and we avoid `GOSOFT`. Still set `sstatus.FS=Initial` in the reset stub
or the first FP instruction traps (RV64.md §1.3, Appendix F #8). If a constrained
CPU (`d=off`) is ever selected, switch to `GOSOFT=1` as `kotama` does.

---

## 5. Architecture

```
honk.elf — one Go program (GOOS=tamago GOARCH=riscv64), S-mode payload
└── main(): DTB-driven init, then a shell goroutine + service goroutines

  ── pure Go application layer ────────────────────────────────────────
  shell · net/http server · "tasks" = goroutines · IPC = channels
  net (stdlib)  ─SocketFunc→  userspace TCP/IP (e.g. gVisor netstack)
  io/fs         ─────────────  virtio-blk + a Go filesystem
  ── internal/ : the "kernel" = drivers + board glue ──────────────────
  board/virt   RamStart/Size · Hwinit0/1 · Exit(sifive_test) · Idle(wfi)
  sbi          ecall wrappers: console, set_timer, HSM, RFENCE, SRST   [RV64 §2]
  trap         Go-asm stvec vector + software dispatch → os/signal     [RV64 §3]
  clint/sbi    S-timer via Sstc stimecmp or SBI set_timer             [RV64 §4.1]
  plic         external IRQ claim/complete (S-context = 2*hart+1)     [RV64 §4.2]
  vm           Sv39 paging, W^X, A/D preset                            [RV64 §5]
  fdt          big-endian DTB parser                                   [RV64 §7.1]
  uart         NS16550A driver (io.ReadWriter)                         [RV64 §7.3]
  virtio       virtio-mmio v2: blk, net                                [RV64 §7.4]
  ── thin Go (Plan9) assembly ─────────────────────────────────────────
  _start/rt0 · CSR read/write · nanotime · printk · goroutine wake
  ── below honk: OpenSBI (M-mode) — we call it, we don't write it ──────
```

The dividing line is `internal/`: above it is ordinary, testable, idiomatic Go;
below it is hardware. The seam between the *runtime* and the board (§6) is even
smaller than the seam between the application and `internal/`.

---

## 6. The seam: the runtime shim surface

This is the entire contract between "bare metal" and "all of Go." A board package
fills in the `runtime/goos` overlay; the runtime calls into it. honk's
`internal/board/virt` provides:

**Required:**

| Symbol | Role | Notes |
|---|---|---|
| `RamStart`, `RamSize` | physical RAM window for the allocator (incl. the code segment) | from `-m`, then refined from the DTB |
| `RamStackOffset` | g0 stack carve-out from top of RAM | |
| `Hwinit0()` | pre-runtime init, **Go assembly, must not allocate** | install `stvec`, set `sstatus.FS`, mask interrupts |
| `Hwinit1()` | post-bootstrap init (after the scheduler exists) | bring up UART/PLIC/timer, parse DTB |
| `Nanotime() int64` | monotonic clock, **asm, must not allocate** | read the `time` CSR (`0xC01`); always readable from S-mode |
| `Printk(c byte)` | one byte to the console, **asm, must not allocate** | SBI `console_putchar`/DBCN early; UART later |
| `GetRandomData()`, `InitRNG()` | entropy for `crypto/rand` | SBI/zkr if present, else a documented fallback |

**Optional but used here:**

| Symbol | Role |
|---|---|
| `Exit(code int32)` | clean shutdown — write `0x5555`/`0x3333` to `sifive_test` (RV64.md §1.4) |
| `Idle(pollUntil int64)` | `wfi` until the next timer deadline → real power-down idle |
| `Task(...)` | SMP: start a goroutine-bearing thread on another hart (**nascent**, §12) |
| `net.SocketFunc` | hand the stdlib `net` package a userspace socket implementation |
| heap-tuning consts | `LogHeapArenaBytes`, `LogPallocChunkPages`, … shrink the runtime's RAM floor (§9) |

Everything else in RV64.md — the DTB parse, PLIC math, Sv39 walk, virtqueue
handshake — is *application-level Go* that runs *above* this seam, not part of the
runtime contract.

---

## 7. What "pure Go" means here

Stated precisely so there's no false advertising:

- **Pure Go = no C, no libc, no cgo.** honk links no C objects; the only thing
  beneath it is OpenSBI firmware, which we *call*, not compile. This matches the
  sense in which TamaGo is "unencumbered Go."
- **It is not zero assembly.** A few dozen lines of **Go (Plan9) assembly** are
  irreducible: the reset stub (`_start`/`rt0`), CSR reads/writes (no Go intrinsic
  exposes `csrrw`), the `stvec` trap vector, and the non-allocating
  `nanotime`/`printk`/wake routines. RV64.md's `_start` (§1.3), `kernelvec`
  (§3.2), and SBI wrapper (§2.1) are the templates; the TamaGo `riscv64` port
  already contains adaptable versions of most of them. We *edit*, not author from
  scratch.
- **The ratio is good.** TamaGo overall is ~90% Go / ~10% assembly; honk's own
  code should be even more Go-heavy because the runtime brings its own asm.

---

## 8. The concurrency and interrupt model

This is the most elegant part of the approach and the place where honk looks
least like a C kernel.

**Rule (TamaGo):** a hardware interrupt does **not** run a C-style ISR. A small
Go-assembly trap vector at `stvec` does the minimum, then *wakes a parked
goroutine*. The mechanism is surfaced through the **standard `os/signal` API**:

```go
// An interrupt-service goroutine. Idiomatic Go, not a magic context.
func serviceUART(ctx context.Context) {
    c := make(chan os.Signal, 1)
    signal.Notify(c, irqUART) // a board-defined os.Signal for UART0 (PLIC src 10)
    for {
        plic.EnableUART()       // re-arm before parking
        select {
        case <-ctx.Done():      // GO.md §10: every goroutine has a stop path
            return
        case <-c:               // woken by the asm vector via signal.Relay
            uart.DrainRX()      // claim → drain FIFO → complete (RV64 §4.2)
        }
    }
}
```

**Practice / how honk uses it:**

- Each device gets **one ISR goroutine** that blocks on its signal channel and
  hands work to workers over **buffered channels**. This is GO.md §10 verbatim:
  small goroutines, explicit stop paths, channels for handoff.
- The PLIC claim/complete handshake (RV64.md §4.2) lives in the ISR goroutine:
  forget the *complete* and the source never re-arms (Appendix F #18).
- The S-timer (RV64.md §4.1) drives the runtime's clock and `Idle`. Detect Sstc
  (`stimecmp`) and use it directly from S-mode; **fall back to SBI `set_timer`**
  if absent (it's version-dependent on QEMU). Reading the `time` CSR is always
  allowed.
- **MMIO ordering is explicit** (RV64.md §6.3): a `fence` before ringing a device
  doorbell, an acquire/release discipline around shared flags. Accesses go
  through a small `reg` helper so the compiler can't hoist a device poll out of a
  loop (GO.md §10's race rules don't help across the device boundary — the memory
  model does).

**Nascent — don't over-engineer interrupts.** The upstream discussion on
golang/go#73608 explored compile-time-checked pure-Go ISRs (`//go:interrupt`
analysis) and channel-send-from-ISR. honk uses the **shipping** `os/signal`
mechanism and treats fancier ISR ergonomics as out of scope. The wake path
exists precisely because an IRQ can land mid-GC at an unsafe point; inherit that
solution, don't casually "improve" it.

---

## 9. Memory, paging, and W^X

**Layout.** The TamaGo allocator is plan9-style (no `brk`): it manages a single
contiguous `[RamStart, RamStart+RamSize)` window holding text, data, heap, and
the g0 stack (top, minus `RamStackOffset`). honk sets these from `-m` at boot,
then refines `RamSize` from the DTB `/memory` node (RV64.md §7.1). A **DMA
region** for virtio rings/buffers is carved *outside* the GC heap so the
collector never moves or scans device-shared memory.

**Small RAM is real.** The runtime's heap-arena and page-chunk sizes are now
overlay-tunable (`LogHeapArenaBytes`, `LogPallocChunkPages`, and a small set of
related constants). `kotama` boots a full Go system in **6 MiB**. honk targets a
comfortable default (e.g. `-m 128`) but documents the small-RAM knobs; below
~4 MiB you also need custom size classes.

**Paging (RV64.md §5) — optional but a real "modern OS" feature.** A unikernel
*can* run flat/identity-mapped (TamaGo's default). honk turns Sv39 on anyway, for
**W^X**:

- kernel text + the asm vector: `R|X`, never writable;
- data, heap, stack, and **all MMIO**: `R|W`, never executable;
- no page is ever both W and X; pre-set `A=1, D=1` on every leaf so it's correct
  on Svade silicon, not just QEMU (RV64.md §5.3, Appendix F #13).

This realizes the insight from the `Biscuit` project (MIT PDOS): in a
memory-safe language, the overwhelming majority of memory-corruption bugs become
*panics, not exploits*, at modest cost (RV64.md §9). It's a cheap, modern,
defensible security posture that a C kernel can't get for free — and it costs us
only the one-time `sfence.vma`-bracketed `satp` switch (RV64.md §5.2), run from
identity-mapped code so the next instruction stays mapped (Appendix F #9).

---

## 10. Best practices from GO.md, applied

honk is also a showcase of writing the OS *well*, per GO.md:

- **Layout (§2, §18):** flat root (`main.go`), the kernel under `internal/` to
  enforce the driver boundary. Packages named for what they *provide* —
  `uart`, `plic`, `vm`, `sbi`, `fdt`, `virtio`. **No** `util`/`common`/`drivers`
  junk-drawer.
- **Interfaces, consumer-side and tiny (§8):** the console is an `io.ReadWriter`;
  the disk is `io.ReaderAt`/`io.WriterAt`; the filesystem is `io/fs.FS`. Don't
  invent a `Driver` interface until a second board demands it (the `sifive_u`
  ↔ `virt` pair is exactly that forcing function). "Accept interfaces, return
  concrete types."
- **Concurrency (§10):** the cardinal rule — every ISR/service goroutine has an
  explicit stop path (`ctx.Done()` / closed channel). No fire-and-forget.
- **Errors (§11):** sentinel `Err…` values for device states, `%w` wrapping up
  the stack; `panic` reserved for "the machine is wedged" (then `Exit` via
  `sifive_test`).
- **Testing — the sleeper win.** Because `GOOS=tamago` *also runs in Linux
  userspace*, the **pure logic** of every driver is unit-testable **on the host
  with `go test -race`**: the FDT big-endian decoder, virtqueue index arithmetic,
  PTE pack/unpack (`PA2PTE`/`PTE2PA`), PLIC context math (`2*hart+1`). Only the
  actual MMIO pokes need QEMU. This is the biggest correctness advantage over a C
  kernel and the reason to structure drivers as *pure functions over byte
  buffers* wherever possible (GO.md §16).
- **Tooling (§17):** `gofmt` + `go vet` + `staticcheck` in CI; host `go test
  -race`; a QEMU smoke test that boots honk and exits via `sifive_test`
  (`0x5555`) so CI gets a clean exit code instead of a hang.

---

## 11. Feature roadmap, mapped to RV64.md Appendix G

RV64.md's Appendix G is the dependency-ordered bringup checklist. honk follows it
through device bringup, then **diverges deliberately** at the process layer (the
§1 decision):

| RV64.md Appendix G step | honk implementation | Layer |
|---|---|---|
| 1. Boot to S-mode, print one byte | OpenSBI → S-mode entry; `Printk` via SBI `console_putchar` | board/virt + sbi |
| 2. SBI console + early printf | hand control to the Go runtime; `fmt`/`log` "just work" | runtime |
| 3. Parse the device tree | `fdt` package decodes `a1`: RAM, UART, virtio, timebase | fdt |
| 4. Trap handler | Go-asm `stvec` vector + dispatch; panic cleanly on unexpected `scause` | trap |
| 5. Physical page allocator | **the Go runtime already has one** — skip | runtime |
| 6. Sv39 paging | `vm`: kernel table, W^X, A/D preset, `sfence`-bracketed `satp` | vm |
| 7. Timer | Sstc `stimecmp` or SBI `set_timer`; drives `Nanotime`/`Idle` | clint/sbi |
| 8. PLIC + interrupt-driven UART | `plic` + `uart` ISR goroutine via `os/signal` | plic, uart |
| **9. Processes (U-mode)** | **replaced**: "tasks" = goroutines; isolation (if needed) via GoTEE | application |
| 10. virtio-blk + FS, then fork/exec | virtio-blk + Go `io/fs`; virtio-net + `net.SocketFunc` → `net/http`; **no** fork/exec | virtio, net |

Net effect: steps 5 and 9–10's process machinery evaporate; the rest is RV64.md
transcribed into idiomatic Go. The "OS" UX is a small shell goroutine (à la
`kotama`) that spawns work as goroutines.

---

## 12. Limitations and the hard parts

Pinned up front so there are no surprises:

1. **No hardware isolation between tasks.** Goroutines share one address space
   (§1). The escape hatch is **GoTEE** (PMP/U-mode applet, `riscv64`-capable),
   *not* RV64.md Part 8.
2. **SMP is nascent in TamaGo.** Historically `GOMAXPROCS=1`; the `Task` hook for
   multi-hart is landing. Plan single-hart first; bring up secondaries later via
   SBI HSM `hart_start` + IPIs + RFENCE TLB shootdown (RV64.md §8.4), gated on
   upstream support. Don't design honk to *require* SMP early.
3. **No async preemption.** A CPU-bound goroutine with no function calls can
   starve the scheduler. Acceptable for a unikernel; document it and avoid
   call-free hot loops.
4. **GC vs. interrupts is deep water.** The whole wake-a-goroutine design exists
   because an IRQ can interrupt the runtime at an unsafe point (mid-GC, mid–stack
   growth). honk inherits TamaGo's solution and treats it as load-bearing.
5. **The `sifive_u` → `virt` S-mode port is the real work** (§4). It's a bounded,
   well-specified task (RV64.md is the spec), but it's the part with no existing
   code to copy verbatim.
6. **Float and `sstatus.FS`** (§4) and the rest of RV64.md Appendix F's
   "correct-on-QEMU, broken-on-hardware" traps (A/D bits, `fence.i`, MMIO fences,
   missing `sfence.vma`) apply to honk's drivers exactly as written.
7. **External dependency risk.** honk's existence depends on TamaGo tracking Go
   releases. Mitigation: pin the `tamago-go` version in `go.mod`/CI; keep the
   board seam (§6) close to the upstream `GOOS=none` shape so a future migration
   is mechanical.

---

## 13. Repository skeleton and build

```
honk/
├── GO.md                      # how to write the Go
├── RV64.md                    # how the hardware works
├── DESIGN.md                  # this file — why it's built this way
├── go.mod                     # + tool directive pinning the tamago go command
├── Makefile                   # test / qemu / qemu-gdb / smoke targets
├── main.go                    # honk: DTB init → shell + service goroutines
├── boot/
│   └── virt.ld                # link at 0x80200000 (Phase 1)            [RV64 §1.2]
└── internal/
    ├── board/
    │   ├── virt/              # S-mode-under-OpenSBI board: the runtime seam (§6)
    │   └── sifive_u/          # Phase 0 stepping-stone / 2nd impl for interfaces
    ├── sbi/                   # ecall wrappers                          [RV64 §2]
    ├── trap/                  # stvec vector (asm) + dispatch (Go)      [RV64 §3]
    ├── plic/  clint/          # interrupt controller + timer            [RV64 §4]
    ├── vm/                    # Sv39 + W^X                              [RV64 §5]
    ├── fdt/                   # device-tree parser (host-testable)      [RV64 §7.1]
    ├── uart/                  # NS16550A                                [RV64 §7.3]
    └── virtio/               # virtio-mmio v2: blk, net                 [RV64 §7.4]
```

**Build (Phase 1 shape; pinned in the Makefile):**

```sh
GOOS=tamago GOARCH=riscv64 GOOSPKG=github.com/usbarmory/tamago \
  $TAMAGO build -ldflags "-T 0x80200000 -R 0x1000" -o honk.elf .

# run (CI/non-interactive: clean exit on crash, piped stdin)
qemu-system-riscv64 -machine virt -m 128 -smp 1 -bios default \
  -display none -serial stdio -no-reboot -kernel honk.elf
```

`make test` runs host-side `go test -race ./internal/...` (the pure logic);
`make qemu` boots in QEMU; `make smoke` boots, asserts the banner, and checks the
`sifive_test` exit code; `make qemu-gdb` adds `-s -S` for `riscv64-elf-gdb`.

---

## 14. Milestones

| # | Goal | Proves |
|---|---|---|
| **0** | Install `tamago-go`; run the stock `sifive_u` example in QEMU 11 | the one load-bearing dependency works on this machine — **do this before anything else** |
| 1 | Repo skeleton + `main.go` printing "honk" and spawning a goroutine, on `sifive_u` | the build/test/run loop |
| 2 | `fdt` parser, host-unit-tested; print the discovered map | host-side testing pays off |
| 3 | New `virt` board: S-mode entry under OpenSBI, SBI console, `time`-CSR clock, `sifive_test` exit | the §4 crux |
| 4 | `trap` vector + S-timer + `Idle`; a `time.Sleep` that actually sleeps | the interrupt model (§8) |
| 5 | `plic` + interrupt-driven `uart`; a line-reading shell | async input, ISR-as-goroutine |
| 6 | `vm`: Sv39 + W^X enabled, kernel still runs | the modern security feature (§9) |
| 7 | `virtio-blk` + a Go `io/fs`; `virtio-net` + `net.SocketFunc` → a `net/http` "honk" server | honk is a real, useful unikernel |
| 8 | Upstream the `virt` board package to TamaGo | gives back; widens the RV64 ecosystem |
| 9 *(stretch)* | SMP via `Task` + HSM; or isolation via GoTEE | only when upstream support and need exist (§12) |

---

## 15. References

**This project's siblings**

- [GO.md](./GO.md) — modern idiomatic Go (the rubric for §10).
- [RV64.md](./RV64.md) — RV64 boot/trap/page/device reference (the spec for the
  board package); especially Appendix F (gotchas) and Appendix G (bringup order).

**Foundation**

- TamaGo — bare-metal Go framework and its modified Go distribution:
  `github.com/usbarmory/tamago`, `github.com/usbarmory/tamago-go`; project wiki
  *Internals* and *Compatibility* pages; `runtime/goos` overlay API.
- `kotama` — tiny `GOOS=tamago GOARCH=riscv64` unikernel demonstrator (the
  existence proof for a small RV64 Go system).
- GoTEE — TamaGo TEE (PMP/U-mode applet isolation; `riscv64`-capable) — the
  isolation escape hatch of §1/§12.

**Upstream direction**

- Go proposal *all: add bare metal support* (`GOOS=none`), golang/go#73608 — the
  intended long-term home of the runtime seam.

**Counter-model (why not (B))**

- `Biscuit` — monolithic POSIX-subset OS kernel in Go for x86-64 (MIT PDOS);
  research papers at the PDOS project page. The proof that a Go *multiprocess
  kernel* is possible and the proof that it is not "small and simple."

**Platform**

- QEMU `virt` machine docs (auto-DTB, virtio, boot flow); OpenSBI `qemu_virt`
  platform docs; RISC-V SBI specification (v2.0 / v3.0); RISC-V Privileged ISA
  (Sv39, traps, CSRs) — all as cited throughout RV64.md.

> The single biggest risk is dependency health: honk lives or dies by TamaGo
> tracking Go. Pin the toolchain version, keep the seam upstream-shaped, and
> revisit if/when `GOOS=none` lands. Everything else in this document is a
> bounded engineering task with a working reference.
