# honk

```
   __     honk
 >(o )___   pure-Go RISC-V64 OS
  (  ._> /  HS-mode under OpenSBI
   '---'
```

**honk is a modern, multiprocess, SMP RISC-V 64 operating system written entirely in Go** — and it is also a Type-1 hypervisor. It boots in HS-mode under OpenSBI on QEMU `virt`, brings every hart up as a real Go scheduler `M`, runs first-party apps as goroutines and untrusted apps as WebAssembly, serves a verified immutable core over a writable log-structured store, talks TCP/IP and HTTP, drives a framebuffer GUI, and hosts guest VMs through the RISC-V H-extension.

The design rule is one sentence:

> **Map operating-system concepts onto Go language primitives, and write low-level code only where the hardware genuinely requires it.**

The Go runtime is the scheduler and memory manager, the type system is the isolation boundary, WebAssembly is the untrusted-code sandbox, and channels / interfaces / `io/fs.FS` are the IPC, driver, and filesystem models. What is left to write by hand is small: hart bring-up, device drivers, and — only for hosting full guests — paging.

A second rule follows: **where the choice is invent-vs-plug-in, plug in.** WASI over a custom ABI, Linux guests over toy guests, signed image updates over plugins — because honk's capability and immutability discipline is exactly what makes the ecosystem-friendly choice *safe*.

## Why this works (it's proven, not speculative)

- **[Biscuit](https://github.com/mit-pdos/biscuit)** (MIT, OSDI '18) — a full multicore POSIX kernel in Go, GC cost ≤13% of kernel CPU. Proves Go drivers, Go SMP, and Go-as-kernel-language.
- **[eggos](https://github.com/icexin/eggos)** — a Go x86 unikernel: "no processes, only goroutines and channels." Proves the goroutine-as-process model.
- **[TamaGo](https://github.com/usbarmory/tamago)** — runs unmodified Go on bare-metal RV64 with the full stdlib. honk's foundation; honk does not re-create the runtime/GC/scheduler.
- **[wazero](https://github.com/tetratelabs/wazero)** — a pure-Go, zero-dependency WebAssembly interpreter designed to embed where there is no OS. honk's untrusted sandbox.

honk's novelty is combining these into the first pure-Go, RISC-V, SMP, genuinely-multiprocess, immutable OS that is also a hypervisor.

## Status

honk is built milestone by milestone, each verified in QEMU before the next. **M0–M12 are complete and verified; every M13 mechanism is proven** against hand-rolled guests — the remaining M13 work is integrating a real Linux/rCore guest.

| Phase | Milestones | State |
|---|---|---|
| **A — foundation** | M0 boot + SMP · M1 IRQ/console/shell · M2 process model | ✅ complete |
| **B — storage** | M3 NVMe/virtio-blk · M4 KV store + VFS · M5 immutable signed core (A/B) | ✅ complete |
| **C — networked OS** | M6 virtio-net + gVisor → stdlib `net`/`net/http` · M7 WASM/WASI (wazero) · M8 host files (9p) | ✅ complete |
| **D — display/GUI** | M9 virtio-gpu framebuffer · M10 virtio-input + `image/draw` toolkit | ✅ complete |
| **E — hypervisor** | M11 H-ext bring-up · M12 timer + preemptible vCPU · M13 real Linux guest | ✅ M11–M12; **M13 mechanisms proven** |

See [`docs/STATUS.md`](docs/STATUS.md) for the living, per-milestone record of what is implemented and exactly how each piece is verified.

## Quickstart

**Requirements:** a host Go toolchain and `qemu-system-riscv64`. The forked Go distribution (`tamago-go`) is auto-built on first use into your OS cache dir. (`python3` enables the headless framebuffer/GUI pixel checks; `curl` is used by the networking smoke test.)

```sh
make run        # build + boot under QEMU virt (quit with Ctrl-A x)
make test       # host -race tests of every pure-Go package
make smoke      # build + boot + assert M0–M12 behavior (the CI gate)
make phase-a    # M0/M1/M2 acceptance: race tests + a QEMU boot matrix
make vet        # go vet under the tamago toolchain
```

`make run` defaults to `-smp 4 -m 512M`, attaches NVMe + virtio-blk + virtio-net (`:80` forwarded to host `:8080`) + virtio-gpu + keyboard/tablet, and shares `./share` into honk over 9p. Booting drops into an interactive shell:

```
honk: HS-mode boot ok  hart=0  dtb=0x9fe00000
honk: SMP up  harts=4  GOMAXPROCS=4
honk: SMP OK - goroutines ran on 4/4 harts [0 1 2 3]

honk: shell ready (type 'help')
honk>
```

Shell commands:

```
help  harts  uptime  mem  echo <text>
run [name]   ps   kill <pid>   crash   reap   stress [n]
ls [dir]   cat <file>   cp <src> <dst>   put <key> <text>   rm <file>
blk  net  mount  wasm <file.wasm>  fb  ui
vm [timer|paging|dbcn|mmio|irq|virtio]   reset --confirm   fault   exit
```

With the defaults, `curl http://localhost:8080/` reaches honk's HTTP server, and files in `./share` appear inside honk (`mount`, `ls`, `cat`).

## The design in one table

This is the whole system. Each row is something a traditional kernel writes thousands of lines for, replaced by a language feature honk already has.

| OS concept | honk uses |
|---|---|
| Process / thread | **goroutine** (M:N across harts by the Go scheduler) |
| Lifecycle / kill | **`context.Context`** — cancel = kill, `Done()` = reaped |
| Scheduler | **the Go runtime** — `GOMAXPROCS = nharts` |
| Per-process address space | **the type system + capabilities** (no MMU for native code) |
| Fault containment | **`recover()`** at the goroutine boundary |
| IPC | **channels**, `io.Pipe`, stdlib `net` |
| Syscalls (native code) | **direct calls on capability interfaces** — no ECALL, no ABI |
| Untrusted-code sandbox | **WASM via wazero** — bounds-checked, capability-gated, force-killable |
| Drivers | **Go interfaces** (`block.Device`, `NetworkDevice`, framebuffer…) |
| Filesystem / VFS | **`io/fs.FS`** composition (KV store + embedded core + 9p host) |
| Async device I/O | **a goroutine blocks on a channel the IRQ handler signals** |
| 2D / GUI | **`image`, `image/draw`, `x/image/font`** |
| Integrity / verity | **`crypto/sha256`, `crypto/ed25519`** + a small Merkle tree |
| Concurrency correctness | **`go test -race`**, the Go memory model |

**Three isolation tiers**, trust matched to mechanism:

1. **Goroutines** — first-party signed code. Memory safety + capabilities + `recover()`; bugs panic, not exploit.
2. **WASM modules** — untrusted/dynamic/any-toolchain code in wazero. Every WASI call is capability-gated (implementing a host function ≠ granting it); force-killable via `context`.
3. **Guest VMs** — full OSes under the H-extension with two-stage paging. **The only place honk uses page tables.**

The full table, the trust model, and the reuse-vs-write accounting are in [`docs/HONK.md`](docs/HONK.md).

## Repository layout

```
honk/
├── block/          # block.Device interface, Slice partitioning, Memory test device (pure Go)
├── board/virt/     # the only hardware-touching code (QEMU virt):
│   │               #   HS-mode cpuinit + runtime/goos overlay, SMP (SBI HSM),
│   │               #   S-mode traps, PLIC/UART console, NVMe/PCIe,
│   │               #   one virtio-mmio transport under blk/net/9p/gpu/input,
│   │               #   and the H-extension VMM (world switch + G-stage paging)
│   └── ring/       #   SPSC console-input ring (host-tested)
├── kernel/         # the HS-mode Go program — the OS logic, mostly pure Go:
│   │               #   main/shell/net/wasm/display/ui/vm/fsmount
│   ├── proc/       #   process model (goroutine + context + caps + recover)
│   ├── kv/         #   crash-safe log-structured key/value store
│   ├── vfs/        #   io/fs.FS synthesis + union overlay
│   ├── image/      #   signed, Merkle-tree'd immutable core + A/B slots
│   ├── p9/         #   read-only 9P2000.L client → io/fs.FS
│   ├── gui/        #   retained-mode image/draw toolkit
│   ├── vmm/        #   pure, host-tested VMM core (RV64 assembler, guest payloads, page tables)
│   └── core/       #   files baked into the verified core image
├── tools/          # build.sh, mkboot (boot trampoline), mkimage (sign images),
│                   #   run-qemu.sh, smoke-test.sh, phase-a-test.sh, screendump/uitest
└── docs/           # HONK.md  STATUS.md  OS.md  GO.md  RV64.md
```

## How it boots

honk targets QEMU `virt` and boots as an **HS-mode payload under OpenSBI** (`-bios default`) — the mainline RISC-V model and the prerequisite for the hypervisor. Three things TamaGo doesn't do out of the box are solved in `board/virt`:

1. **HS-mode startup, no M-mode CSRs.** honk supplies its own `cpuinit` (`boot_riscv64.s`) and the full `runtime/goos` overlay, so OpenSBI keeps the M-mode CSRs it owns.
2. **The fw_dynamic entry quirk.** QEMU enters the payload at the load *base*, not the ELF entry. `tools/mkboot` emits a 24-byte trampoline at `0x80200000` that jumps to honk's real entry; honk links at `0x80400000` to clear it.
3. **SMP without a runtime fork.** Secondary harts start via SBI HSM `hart_start` into a park loop; the runtime's `goos.Task` hook hands each one a stack + g0 + `mstart`. Then `GOMAXPROCS = nharts`.

`RamSize` is discovered from the DTB pointer (OpenSBI/QEMU place the DTB at the top of usable RAM), so any `-m` works with no FDT parsing. Hardware details consulted only where they touch silicon are collected in [`docs/RV64.md`](docs/RV64.md).

## Testing & quality gates

honk splits correctness by where its authority lives:

- **Pure-Go logic → host `go test -race`.** The process state machine, the SPSC console ring, the KV store (incl. a 600-op crash-consistency property test), the VFS overlay, image verity, the 9P client, the GUI toolkit (rendered pixels and all), and the VMM's encoders/page-tables/guest payloads are all exercised on the host — authoritative for concurrency and the "works-on-QEMU/silent-fault" bug class.
- **Hardware-contact behavior → QEMU.** `make smoke` boots honk and asserts M0–M12 end to end across both block backends, networking (a real `curl`), the framebuffer and GUI (captured headlessly over QMP), and every `vm` demo. `make phase-a` is a dedicated foundation gate (SMP 1/4/8, RAM 256M–2G, the fatal-trap path, the live process model).

Coding standards and the idioms honk holds itself to are in [`docs/GO.md`](docs/GO.md).

## Scope & honest non-goals

honk is a focused appliance, not a general-purpose UNIX. By design:

- **`io/fs.FS` composition, not POSIX** — whole-value writes, no partial write/append/rename/mmap/metadata; `Open` materializes a whole file.
- **Polled, serialized device I/O** — the IRQ-driven fast path is the same deferred async-I/O item across all virtio drivers.
- **QEMU `virt` first** — minimal PCIe/NVMe, reliance on coherent DMA; real-silicon caveats (A/D bits, fences, cache maintenance) are tracked, not yet exercised.
- **The hypervisor's M13 deliverable** — loading a real third-party kernel image, a full guest device tree, PLIC/AIA, virtio-fs, and time-sharing a hart — is the remaining integration work; the mechanisms it stands on are done.

## Footprint — the "fewest lines" accounting

The original Go honk maintains is small because the language already is an OS (gVisor, wazero, and the stdlib are upstream and unmodified):

- **Networked appliance (M0–M8):** ~4,000–5,000 lines, most of it device drivers and the SMP hook — the pure OS-mechanism code (process table, caps, VFS) is ~1.5k.
- **+ GUI (M9–M10):** ~+1,300 lines.
- **+ Hypervisor (M11–M13):** ~+6,000–8,500 lines — the one component where hardware virtualization can't be a language feature.

A C/Rust equivalent is six figures.

## Documentation

| Doc | What it is |
|---|---|
| [`docs/HONK.md`](docs/HONK.md) | The full design: the mapping, architecture, reuse-vs-write, roadmap, risks. |
| [`docs/STATUS.md`](docs/STATUS.md) | Living build status — what is implemented and verified, milestone by milestone. |
| [`docs/RV64.md`](docs/RV64.md) | Build-oriented RISC-V 64 reference (boot, SBI, traps, paging, virtio, H-ext). |
| [`docs/GO.md`](docs/GO.md) | Go language rules + modern idioms and the quality bar honk holds. |
| [`docs/OS.md`](docs/OS.md) | What a modern OS is for; the small-TCB, memory-safe-kernel thesis. |
