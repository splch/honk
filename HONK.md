# honk - a modern pure-Go multiprocess RISC-V 64 operating system

honk is an everyday RISC-V 64-bit OS written entirely in Go. The design rule is
simple: **map operating-system concepts onto Go language primitives, and only
write low-level code where the hardware genuinely requires it.** The Go runtime
is the scheduler and memory manager, the type system is the isolation boundary,
WebAssembly is the untrusted-code sandbox, and channels/interfaces/`io.FS` are
the IPC, driver, and filesystem models. What remains to write by hand is small:
hart bring-up, device drivers, and (only for hosting full guest VMs) paging.

A second rule follows from the first: where the choice is between inventing
something honk-specific and plugging into an existing ecosystem, honk plugs in -
WASI over a custom ABI, Linux guests over toy guests, image-based updates over
plugins - because the capability and immutability discipline below is exactly
what lets honk make the ecosystem-friendly choice *safely*. That is a usability
argument as much as a code-size one.

> Companion docs: `GO.md` (the language + idioms we exploit and the standards we
> hold the code to), `RV64.md` (the hardware reference - consulted only for the
> parts that truly touch silicon: SMP, traps, MMIO, and the H-extension),
> `OS.md` (what a modern OS is for; the small-TCB and memory-safe-kernel thesis).

## This approach is proven, not speculative

- **Biscuit** (MIT, OSDI '18): a full multicore POSIX kernel in Go - VM, journaled
  FS, TCP/IP, AHCI + Intel NIC drivers, ~30k LoC, no C. GC cost <=13% of kernel
  CPU, ~115us pauses. Proves Go drivers, Go SMP, and Go-as-kernel-language work.
- **eggos** (2.3k stars): a Go x86 unikernel - "no processes, only goroutines and
  channels," netstack, framebuffer GUI. Proves the goroutine-as-process model.
- **TamaGo** ([usbarmory/tamago](https://github.com/usbarmory/tamago)): runs
  unmodified Go on bare-metal RV64 today, with the full stdlib. This is honk's
  foundation - we do not re-create the runtime/GC/scheduler.
- **wazero**: a pure-Go, zero-dependency WebAssembly runtime whose interpreter is
  GOARCH/GOOS-agnostic and is designed to embed "in an application that doesn't
  use an operating system." This is honk's untrusted sandbox - memory-safe, no MMU.

honk's novelty is combining these into the first pure-Go, RISC-V, SMP,
genuinely-multiprocess, immutable OS that is also a hypervisor.

## Shape of the system (decisions locked in)

1. **Target QEMU `virt`** (the standard board; virtio-mmio + PCIe + H-extension).
2. **Boot in HS-mode from M0** under OpenSBI, so the hypervisor works later
   without re-homing. The appliance is a single HS-mode Go program.
3. **Three isolation tiers** (trust matched to mechanism): trusted **goroutines**
   (type-safety + capabilities), untrusted **WASM** modules (wazero sandbox),
   full **VMs** (H-extension). No per-process page tables for native code.
4. **Storage**: immutable, integrity-verified core image (Merkle/verity + A/B
   slots) + physical **NVMe** for state (a Go-native log-structured KV store) +
   **virtio-fs** so guest VMs see host files seamlessly (9p interim).
5. **A display/GUI path** eventually: virtio-gpu framebuffer + a minimal custom
   toolkit over the stdlib `image`/`image/draw` packages.
6. **SMP from M0** - all harts up via SBI HSM, the Go runtime scheduler across
   harts.
7. **honk is also a Type-1 hypervisor** (RISC-V H-extension): a guest VM runs as
   a real guest (H-ext = how the guest CPU runs); virtio-fs is how it sees shared
   folders (orthogonal layers).

**Consequence:** native programs are memory-safe Go, isolated by the type system,
so they need no MMU. **Paging is not part of the appliance at all** - it appears
only inside the hypervisor (Phase E), to give guests their virtualized (two-stage)
memory.

---

## 1. The mapping: OS concept -> Go primitive

This table is the whole design. Each row is something a traditional kernel
writes thousands of lines for, replaced by a language feature honk already has.

| OS concept | honk uses | Notes |
|---|---|---|
| Process / thread | **goroutine** | the Go scheduler runs them M:N across harts |
| Process lifecycle / kill | **`context.Context`** | cancel = kill; `Done()` = reaped; deadlines = timeouts |
| Scheduler | **the Go runtime scheduler** | `GOMAXPROCS = nharts`; we write none of it |
| Per-process address space | **the type system + capabilities** | a goroutine can only touch what it holds a reference to |
| Fault containment | **`recover()`** at the goroutine boundary | a panicking program is reaped; kernel survives (Biscuit: corruption -> panic, not exploit) |
| IPC | **channels**, `io.Pipe`, stdlib `net` | typed, race-checked message passing |
| Syscalls (native code) | **direct calls on capability interfaces** | no ECALL, no ABI, no trap for trusted code |
| Untrusted code sandbox | **WASM via wazero** | linear-memory isolation + capability-gated host funcs + forced termination; no MMU |
| Driver model | **Go interfaces** (`BlockDevice`, `NIC`, `Framebuffer`, `Console`) | discovery from the device tree; no registry framework |
| Filesystem / VFS | **`io/fs.FS`** composition | KV store + `embed.FS` core + host mount all behind one interface |
| Async device I/O | **a goroutine blocks on a channel the IRQ handler signals** | no callback hell, no bottom halves |
| 2D rendering / GUI | **`image`, `image/draw`, `x/image/font`** | the stdlib is the graphics engine |
| Integrity / verity | **`crypto/sha256`, `crypto/ed25519`** + a small Merkle tree | stdlib does the cryptography |
| Config / persistent state | **Go structs** persisted via the KV store | no bespoke config format |
| Concurrency correctness | **`go test -race`, the Go memory model** | every report is a real race |

What is *not* in this table - and therefore is the only hand-written low-level
code - is: **hart bring-up (SMP), device drivers (MMIO/DMA/IRQ), and the
hypervisor (H-extension + two-stage paging for guests).** Everything else is the
language.

---

## 2. Architecture

honk is a single Go program that boots in HS-mode under OpenSBI and never leaves
it for its own work. There is no kernel/user split for native code; isolation is
by trust tier, not privilege ring.

```
 M-mode  : OpenSBI firmware (TCB; we do not write it)
 HS-mode : the entire honk program - the Go runtime + GC + scheduler, drivers,
           net stack, services, trusted apps, the WASM host, the VMM
   ├─ trusted goroutines  : kernel services + first-party apps (type-safe + caps)
   ├─ WASM modules        : untrusted/dynamic apps in wazero (in-process sandbox)
   └─ (Phase E) guest VMs : VS/VU-mode, two-stage paging - the only paged memory
```

- **Memory**: flat (identity, VA=PA) for honk itself, like TamaGo/eggos/Biscuit's
  shim today; this also makes DMA trivial. Optional kernel W^X hardening can come
  later. **Page tables exist only for guest VMs** (Phase E, `RV64.md` §5 + the
  H-extension's second stage).
- **Processes**: a `proc` is a goroutine (or tree) + a `context.Context` + a
  capability set + metadata. The "process table" is a `map[PID]*proc`; `kill` is
  `cancel()`; `ps` iterates the map; fault containment is `recover()` at the
  goroutine root. Trusted apps are compiled into the (immutable, signed) image -
  which is exactly the unikernel-appliance model and dovetails with decision #4.
- **Drivers** are interfaces discovered from the device tree (`a1` at boot,
  `RV64.md` §7.1). Async I/O: the device's IRQ handler (hooked into TamaGo's
  riscv64 trap vector) does the minimum and signals a channel; a driver goroutine
  wakes and does the work (the Biscuit interrupt model).
- **Filesystem** is `io/fs.FS` composition: the immutable core as a verified
  `embed.FS` (read-only), the KV store as a writable `fs.FS` over NVMe, and the
  host share as an `fs.FS` over 9p/virtio-fs. A tiny overlay FS unions them.
- **SMP**: secondary harts started via **SBI HSM `hart_start`** (`RV64.md`
  §2.2/§8.4) into HS-mode, each given a real Go `M` (the one **forked
  `tamago-go`** change); then `GOMAXPROCS = nharts` and the runtime spreads
  goroutines. CLINT/SBI IPIs for wakeups. This is the single hardest piece (§9).

### Isolation tiers and the trust model (decision #3)

| Tier | Mechanism | For | Escape barrier |
|---|---|---|---|
| **Goroutine** | Go type safety + capability scoping + `recover()` | first-party kernel services and apps (in the signed image) | memory safety (no `unsafe`, no cgo); bugs panic, not exploit |
| **WASM (WASI)** | wazero sandbox; every WASI call (`fd_write`, sockets, ...) routed through a capability check, so *implementing* a host function is separate from *granting* it; epoch/`context` termination | untrusted / dynamic / third-party apps from any toolchain (Rust, Go `wasip1`, C/C++ via wasi-sdk, Zig) | the WASM VM - bounds-checked, can only reach granted WASI funcs, force-killable |
| **VM** | H-extension, two-stage paging, virtio backends | full guest OSes (Linux) and the strongest sandbox | hardware virtualization |

Capability discipline (the `OS.md` lethal-trifecta mitigation at the OS
boundary): a program receives only the interface handles its manifest grants - a
network tool gets a `Dialer`, never the `BlockDevice` or the VM manager. This is
ordinary Go: capabilities are just interface values you do or do not pass.

### Storage (decision #4)

- **Immutable core**: `tools/mkimage` builds a Merkle tree over the image; boot
  verifies it through an abstract **anchored-boot interface** and serves it
  read-only via `embed.FS`. The QEMU baseline is a software chain (an embedded
  `ed25519` pubkey verifies a signed header -> Merkle root -> verity); real
  boards anchor the key *hash* in OTP fuses, add an anti-rollback security-version
  counter, and let the embedded key sign a rotatable subordinate (rotate keys
  without bricking). **A/B slots** = verify-then-switch, fall back on failure.
  Stateless by construction (the Silverblue/ChromeOS/Workers image model).
- **State on NVMe**: a Go-native **log-structured KV store** that is itself a §1
  mapping - a single appender goroutine owns the log and drains write requests
  off a channel, and *that drain is the group-commit* (collect a batch, one
  fsync, ack all); readers run lock-free against an atomically-swapped index
  snapshot. Group-commit by default (a few-ms loss window); an explicit `Sync()`
  / durable flag pays the fsync for save/commit operations (the SQLite
  `synchronous` pattern). Crash-consistency is separate and non-negotiable: a WAL
  of checksummed records with atomic checkpoint switching, replaying to the last
  valid record and discarding a torn tail. Implements `io/fs.FS`; virtio-blk is
  the fallback block transport.
- **Host files**: honk is a **9p client** - its own read-only 9P2000.L client
  (`kernel/p9`) over its virtio-mmio transport mounts a QEMU-shared host
  directory as an `io/fs.FS`, unioned into the overlay (M8). The reverse - honk
  *serving* its FS to guests via a **virtio-fs device backend** (FUSE-over-virtio)
  - lands in Phase E.

---

## 3. What we reuse vs. write

### Reuse (the bulk of the system)

| Concern | Reuse |
|---|---|
| Runtime, GC, goroutine scheduler, full stdlib, S-mode support | TamaGo (`tamago-go`, `tamago/riscv64` incl. `InitSupervisor`) |
| TCP/IP (gVisor) -> stdlib `net` | `usbarmory/go-net` (`gnet`; links on riscv64) |
| virtqueue framework + mmio/pci transports | `tamago/kvm/virtio` is amd64-only on this version, so honk uses its **own** virtio-mmio transport (`board/virt/virtio.go`); the virtio-net + virtio-blk **drivers** are honk's |
| Untrusted-code WASM sandbox | `wazero` (interpreter mode) |
| TLS/HTTP/SSH/DNS/crypto/post-quantum KEM/JSON/image | Go stdlib + `golang.org/x/{crypto,image}` |
| Shell/terminal | `tamago-example/shell` |
| 9P host filesystem | `Harvey-OS/ninep` (read; its client wants a streaming `net.Conn`, not a virtqueue, so honk's read-only 9P2000.L client `kernel/p9` is its own) |
| Driver / VMM hardware references (read, don't import) | Biscuit (AHCI/NIC in Go), go-nvme, salus/hypocaust-2 |

### Write (only where hardware is intrinsic)

| Concern | Package | ~LoC | Difficulty |
|---|---|---|---|
| **SMP hart bring-up**: SBI HSM + per-hart Go `M` | `kernel/smp` + `tamago-go` fork | 600-1200 | **hard (#1)** |
| `board/virt`: mem map, NS16550 UART, CLINT, PLIC, DTB parse | `board/virt` | 400 | medium |
| IRQ plumbing (trap vector hook -> channel signals) | `kernel/irq` | 250 | medium |
| Process model: `proc` table, context lifecycle, capabilities, `recover` domains | `kernel/proc`, `kernel/cap` | 500 | easy-medium |
| PCIe ECAM enumeration | `kernel/pci` | 300 | medium |
| **NVMe** driver (queues, identify, R/W) implementing `BlockDevice` | `kernel/nvme` | 800 | hard |
| virtio-blk fallback | `kernel/virtio/blk` | 300 | medium |
| **Log-structured KV store** (single-writer appender + WAL + group-commit + `Sync()`) -> `io/fs.FS` | `kernel/kv` | 1000 | medium-hard |
| Immutable image: Merkle verity + A/B + anchored-boot iface (ed25519 / OTP fuses) + anti-rollback | `kernel/image` + `tools/mkimage` | 750 | medium |
| WASI Preview 1 host (every call capability-gated) + module manifests | `kernel/wasm` | 500 | medium |
| Net glue (go-net + `net.SocketFunc`) | `kernel/net` | 200 | easy |
| Read-only **9P2000.L client** -> `io/fs.FS` + virtio-9p transport | `kernel/p9` + `board/virt/virtio9p` | 600 | medium |
| virtio-gpu framebuffer -> `draw.Image` | `kernel/virtio/gpu` | 500 | hard |
| virtio-input (evdev key/rel/abs events) -> input channel | `kernel/virtio/input` | 200 | easy-medium |
| Minimal GUI toolkit over `image/draw` + font | `user/gui` | 800 | medium |
| Trusted apps: `init`, shell, httpd, a tool | `user/cmd/*` | 500 | easy |
| **H-ext VMM** (Phase E): two-stage paging, vCPU goroutines, trap-and-emulate, SBI/PLIC emul, guest DT | `kernel/vmm` | 4000-7000 | **hard (research)** |
| virtio device **backends** for guests (fs/blk/net) incl. virtio-fs | `kernel/vmm/virtio` | 1500 | hard |
| Build, linker shim, QEMU run, CI, tests | `tools/`, `*_test.go` | 400 | easy |

**Absent** versus a conventional kernel: no per-process Sv39, no
trampoline/trapframe, no syscall ABI, no ELF loader, no PMP, no separate
supervisor monitor - each replaced by a row in §1.

---

## 4. Everyday-use capabilities

- **Networking**: gVisor via `go-net`; `net.SocketFunc = stack.Socket` lights up
  stdlib `net` -> `net/http(s)`, `crypto/tls`, DNS, NTP, SSH server.
- **Apps**: first-party Go apps (compiled into the signed image) + portable
  **WASI** apps from any toolchain loaded at runtime (the Workers/Spin model) +
  full Linux VMs. wazero is interpreter-only, so WASM suits app/glue/service
  workloads; push compute-heavy code into a VM or compile it in.
- **Storage**: NVMe + KV store; immutable verified core; virtio-fs/9p host share.
- **Crypto**: full stdlib `crypto/*` incl. post-quantum `crypto/mlkem`.
- **GUI**: virtio-gpu framebuffer + a minimal toolkit over `image/draw`.
- **Observability**: `log/slog`, `net/http/pprof`, `statsviz`, `ps`/`top`.

---

## 5. Targeting and toolchain

- **Compiler**: forked TamaGo Go distribution (`go1.26.4` base), auto-fetched by
  the `tamago` tool; `rv64gc`/`lp64d` (hardware float, for SMP). The fork's only
  job is the per-hart `M` bring-up hook. **Cadence**: rebase each upstream
  release, pin the exact base tag + patch set for reproducible builds, and work to
  upstream the SMP hook so the fork eventually goes to zero.
- **Build**:
  ```
  GOOS=tamago GOARCH=riscv64 GOOSPKG=github.com/usbarmory/tamago \
    go run github.com/usbarmory/tamago/cmd/tamago build -tags virt \
    -trimpath -ldflags "-T 0x80200000 -R 0x1000" ./kernel
  ```
- **QEMU `virt`** (OpenSBI in M, honk as S-mode payload; H, SMP, NVMe, GPU, 9p):
  ```
  qemu-system-riscv64 -machine virt -cpu rv64,h=true -smp 4 -m 2G \
    -nographic -bios default -kernel kernel.elf \
    -drive file=data.img,if=none,id=nvm,format=raw -device nvme,serial=honk,drive=nvm \
    -device virtio-net-device,netdev=n0 -netdev user,id=n0,hostfwd=tcp::2222-:22 \
    -device virtio-gpu-device \
    -virtfs local,path=$PWD/share,mount_tag=host,security_model=none,id=host
  ```
- **Provenance caveat** (`OS.md`): validate SMP races, drivers, and (Phase E) VM
  exits on ET-SoC-1 `sys_emu` (64 harts) and real silicon, not just QEMU.

---

## 6. Repository layout (`GO.md` §18: flat first)

```
honk/
├── go.mod
├── kernel/                 # the HS-mode Go program (the whole OS)
│   ├── main.go  smp.go  irq.go  proc.go  cap.go
│   ├── net.go  pci.go  nvme.go  kv.go  vfs.go  image.go  wasm.go
│   ├── virtio/             # blk, gpu (+ Phase E: fs/blk/net guest backends)
│   └── vmm/                # Phase E: H-extension VMM (the only paging in honk)
├── board/virt/             # mem map, NS16550 UART, CLINT, PLIC, DTB
├── user/
│   ├── gui/                # toolkit over image/draw + font
│   └── cmd/{init,sh,httpd,hello}/   # first-party apps (compiled into the image)
├── apps-wasm/              # sample untrusted WASM apps (any language)
├── tools/                  # mkimage (verity/A-B), linker shim, run-qemu.sh
├── HONK.md  RV64.md  GO.md  OS.md
└── *_test.go
```

---

## 7. Implementation roadmap

Each milestone runs in QEMU before the next. Hardware-contact work (SMP, drivers)
is front-loaded; the OS logic on top of it is small because it is Go.

**Phase A - foundation**
1. **M0 Boot in HS + SMP + hello.** Boot as the OpenSBI S-mode payload; bring up
   all harts with a per-hart Go `M` (the `tamago-go` fork); banner; clean exit.
   The #1 risk is retired first.
   - *Status:* **COMPLETE + verified.** Boots HS-mode under OpenSBI on QEMU virt;
     **all harts run Go Ms** (`GOMAXPROCS=nharts`), the scheduler spreads
     goroutines across every hart; clean SBI shutdown. Tested at `-smp 1/4/8`,
     boot-hart-agnostic. Boots via a 24-byte trampoline (`tools/mkboot`) around
     QEMU's fw_dynamic load-base entry; honk supplies its own `cpuinit` +
     `runtime/goos` overlay in `board/virt`. **SMP needed NO `tamago-go` fork** -
     the per-hart `M` bring-up uses the runtime's `goos.Task` hook (as tamago's
     amd64 SMP does), so the #1 risk is retired and the fork stays at zero for
     M0. See `docs/STATUS.md`.
2. **M1 IRQ + console + shell.** Trap-vector hook routes IRQs to channels;
   `tamago-example/shell` over UART; clean panic with `scause`/`sepc`/`stval`.
   - *Status:* **COMPLETE + verified.** honk installs its own S-mode trap
     vector on every hart (TamaGo's riscv64 handler is M-mode and never does a
     real trap return). UART RX interrupt -> PLIC -> trap -> lock-free ring ->
     reader goroutine -> `virt.Console()` channel; a small line shell
     (`help`/`harts`/`uptime`/`mem`/`echo`/`fault`/`exit`) runs over it.
     S-mode exceptions print `scause`/`sepc`/`stval` and halt (the `fault`
     command exercises this via `EBREAK`). Interrupts run on the boot hart only
     (single PLIC consumer, no claim races); secondaries set `stvec` for
     exception safety. See `docs/STATUS.md`.
3. **M2 Process model.** `proc` table = goroutines + `context` + capabilities;
   `run`/`ps`/`kill`; `recover()` fault domains (a panicking app is reaped, kernel
   and siblings live); race-tested under `-smp 4`.
   - *Status:* **COMPLETE + verified.** `kernel/proc` (pure Go, host
     race-tested) maps process = goroutine + `context.Context` (cancel = kill) +
     `Caps`. Shell `run`/`ps`/`kill`/`crash`/`reap`/`stress`; `recover()` fault
     domains contain panics (the kernel and siblings survive); capabilities are
     shown by `ps` and queried via `proc.Self(ctx).Can(...)`. Verified with
     `go test -race ./kernel/proc` and a live `stress` across 4 harts.
     **Phase A complete.** See `docs/STATUS.md`.

**Phase B - storage**
4. **M3 PCIe + NVMe** (virtio-blk fallback) implementing `BlockDevice`.
   - *Status:* **COMPLETE + verified.** `block.Device` interface (`honk/block`)
     with two backends: **NVMe-over-PCIe** (PCIe ECAM enumeration + BAR
     assignment, controller bring-up, admin+I/O queues, identify, PRP
     read/write - primary) and **virtio-blk** over virtio-mmio v2 (fallback);
     `ProbeBlock` selects NVMe if present. `blk` shell self-test; both verified
     for detection, read/write round-trip, and on-disk persistence; the smoke
     test gates both. See `docs/STATUS.md`.
5. **M4 KV store + VFS.** Log-structured KV -> `io/fs.FS`; overlay with the
   `embed.FS` core; `ls`/`cat`/`cp`.
   - *Status:* **COMPLETE + verified.** `kernel/kv` = crash-safe log-structured
     KV over `block.Device` (single-appender group commit, lock-free COW
     snapshot reads, double-buffered superblock, atomic-checkpoint compaction,
     replay-to-last-valid). Hybrid value residency: small values inline in
     memory, larger ones disk-resident (verified pointer), so the store is not
     RAM-bounded. `kernel/vfs` = nested `io/fs.FS` over the KV +
     union `Overlay` of the writable KV over the read-only `//go:embed` core.
     Shell `ls`/`cat`/`cp`/`put`/`rm`. Host race-tested (torn-tail, compaction,
     concurrency) + `fstest.TestFS`; reboot persistence verified in QEMU. See
     `docs/STATUS.md`.
6. **M5 Immutable core.** `mkimage` verity + A/B; boot verifies + serves R-O;
   stateless reset.
   - *Status:* **COMPLETE + verified.** `kernel/image` (pure Go, host
     race-tested) is a signed, Merkle-tree'd (`crypto/sha256`) core image: a
     fixed header carrying the Merkle root, an anti-rollback security version,
     and a file-table hash is signed with `crypto/ed25519`; `Verify` checks the
     signature against an abstract **anchor**, the version floor, the table
     hash, the Merkle root, and per-file bounds, failing closed. The QEMU
     anchor (`SoftwareAnchor`) embeds the dev public key; silicon swaps in an
     OTP-fused key + monotonic counter behind the same interface. The block
     device is partitioned (`block.Slice`) into **A/B image slots + the kv
     region**; boot `Select`s the valid slot with the highest version and falls
     back across the rest (the embedded factory image is the guaranteed-good
     last candidate). The verified core is served read-only (`vfs.FilesFS`)
     under the writable kv overlay. **Stateless reset** (`kv.Reset`, shell
     `reset --confirm`) clears the writable layer crash-safely so the immutable
     core shows through. `tools/mkimage` builds/signs images; `make vet` now
     runs under the tamago toolchain. Host race-tested (build/verify, tamper
     detection, anti-rollback, A/B + fallback, slot I/O) and smoke-tested in
     QEMU (verification, A/B select + fallback, reset). **Phase B complete.**
     See `docs/STATUS.md`.

**Phase C - the everyday networked OS**
7. **M6 Networking.** virtio-net + gVisor; SSH/HTTP/HTTPS, `/pprof`, `/statsviz`;
   DNS/NTP; `crypto/mlkem` demo. *Networked appliance complete.*
   - *Status:* **COMPLETE + verified.** honk's own virtio-net driver
     (`board/virt/virtionet.go`, on a shared virtio-mmio v2 transport
     `board/virt/virtio.go` now used by both virtio-blk and virtio-net) is a
     `go-net` `NetworkDevice`; `gnet.NewGVisorStack` is the stack; `net.SocketFunc
     = stack.Socket` lights up the stdlib `net` package, and honk serves
     `net/http` on `:80`. Verified end to end in QEMU (a host `curl` through
     SLIRP `hostfwd` reaches the server: driver -> gVisor -> stdlib
     `net.Listener`), gated by the smoke test. A compile/link **spike retired the
     #1 risk first**: gVisor links on `GOOS=tamago GOARCH=riscv64`. The spike
     also found `go-net/virtio`'s device driver is amd64-only (its PCI transport
     imports `tamago/amd64`/`soc/intel/pci`), so honk supplies its own driver on
     its existing virtio-mmio transport - no `tamago/dma`, one `dmaAlloc`. The
     stdlib HTTP/TLS pull-in grows the image ~3 MB -> ~11 MB (the cost of the
     reuse; LoC stays small). SSH, DNS/NTP, `/pprof`, `crypto/mlkem` build on
     this. See `docs/STATUS.md`.
8. **M7 WASM/WASI tier.** Embed wazero; run WASI Preview 1 modules from any
   toolchain; route every WASI call through a capability check (implementing !=
   granting); epoch/`context` termination. `run app.wasm`. (Path to WASI Preview
   2 / the Component Model as wazero matures - a module's WIT world becomes its
   manifest.)
   - *Status:* **COMPLETE + verified.** `kernel/wasm.go` embeds wazero
     (interpreter mode) + WASI Preview 1. A WASM module is a honk **process**
     (`runWASM` -> the M2 table): it runs as a goroutine under a `context`, and
     the runtime's `WithCloseOnContextDone` makes `kill` terminate even a
     tight-looping module (verified: `wasm loop.wasm` + `kill`). **Capability
     discipline:** honk *implements* the WASI host funcs once but *grants*
     nothing by default - console (stdout/stderr) and filesystem (read) are
     passed per module via `ModuleConfig` from the process `Caps`. Two
     hand-encoded samples ship in the verified core (`hello.wasm`, `loop.wasm`),
     validated under wazero on the host before commit; shell `wasm <file>`.
     QEMU-verified (`honk from wasm`) and smoke-gated. A compile/link **spike
     retired the #1 risk first**: wazero's interpreter builds on `GOOS=tamago
     GOARCH=riscv64` (its compiler/JIT backend is gated off for unknown GOOS).
     **Phase C complete.** See `docs/STATUS.md`.
9. **M8 Host files.** 9p-over-virtio as an `fs.FS` (the virtio-fs device backend
   lands in Phase E).
   - *Status:* **COMPLETE + verified.** honk's own virtio-9p driver
     (`board/virt/virtio9p.go`, on the shared virtio-mmio v2 transport) carries
     whole 9P messages for a hand-rolled, read-only **9P2000.L client**
     (`kernel/p9`) that presents QEMU's host-shared directory as a standard
     `io/fs.FS` and is unioned into the overlay (writable kv → host share →
     verified core). The client owns the wire format + fid lifecycle behind a
     two-method `Transport` and `Mount() (fs.FS, error)` - a deep module. The
     stated reuse (`Harvey-OS/ninep`) is built around a streaming `net.Conn`,
     not a message-framed virtqueue (the same fit problem that made honk write
     its own virtio-net driver), so honk hand-rolls the small read subset over
     its existing transport. Pure Go: `go test -race ./kernel/p9` runs against an
     in-process 9P server incl. `fstest.TestFS`; QEMU-verified end to end
     (`-fsdev local` + `virtio-9p-device`) and smoke-gated. Shell `mount` lists
     the layers; `ls`/`cat` read host files through the overlay. **Phase C
     complete.** See `docs/STATUS.md`.

**Phase D - display/GUI**
10. **M9 Framebuffer.** virtio-gpu -> `draw.Image`; compositor; draw a test
    pattern (output first, so rendering is solid before events).
    - *Status:* **COMPLETE + verified.** honk's own virtio-gpu driver
      (`board/virt/virtiogpu.go`, on the shared virtio-mmio v2 transport) hides
      the 2D control protocol behind a tiny interface - an `*image.RGBA` to draw
      into (`Image()`) and `Flush()` - so `kernel/display.go` brings up a single
      full-screen scanout and draws a four-quadrant test pattern with the stdlib
      `image`/`image/draw` packages (output-first; input + toolkit are M10). The
      resource format `R8G8B8A8` matches `image.RGBA` byte-for-byte (no
      swizzle), and honk's identity map lets the framebuffer slice be the
      resource's backing at its own address. QEMU-verified **end to end and
      headless**: the smoke test boots under `-display none`, captures the
      scanout over QMP `screendump` (`tools/screendump.py`), and asserts the
      rendered quadrant colors - proving real pixels reached the host
      framebuffer (and that the format is correct), not just that the control
      commands succeeded. Shell `fb`. See `docs/STATUS.md`.
11. **M10 GUI + input.** Toolkit over `image/draw` + font; **virtio-input** (IRQ
    -> channel -> dispatch goroutine -> focused widget); an interactive demo app
    (Go or WASM) you can click and type into.
    - *Status:* **COMPLETE + verified.** honk's virtio-input driver
      (`board/virt/virtioinput.go`) surfaces raw evdev events from QEMU's
      virtio-keyboard + virtio-tablet; `kernel/gui` is a pure-Go retained-mode
      toolkit over `image/draw` + `x/image/font/basicfont` (a focus-routing
      `UI`, a `Button`, a `TextField`); `kernel/ui.go` translates evdev ->
      `gui.Event` (keycode->rune, tablet axes->pixels) and pumps an interactive
      demo onto the M9 framebuffer. **Refinement vs. the plan:** input is
      *polled*, not IRQ-driven - a virtio-input device buffers events in its
      eventq, so polling is lossless (unlike the UART FIFO that forced the M1
      console onto the IRQ path), which keeps input off the nosplit trap path
      and consistent with honk's other (polled) virtio drivers; true IRQ-wakeup
      stays the shared deferred async-I/O item. The toolkit is host race-tested
      including the rendered pixels; the input path is QEMU-verified end to end
      and headless - the smoke test injects a click + the keystrokes `honk` over
      QMP `input-send-event` and asserts honk dispatched them (serial) and that
      the click reached the host framebuffer (screendump, `tools/uitest.py`).
      Shell `ui`. **Phase D complete.** See `docs/STATUS.md`.

**Phase E - hypervisor (the only paging in honk; first pure-Go RISC-V VMM)**
12. **M11 H-ext bring-up.** A trivial hand-rolled VS-mode payload that spins and
    prints via an emulated SBI console - proves H-ext enable, two-stage paging
    (`hgatp`), and trap-and-emulate in isolation, against code you fully control.
    - *Status:* **COMPLETE + verified.** honk hosts a VS-mode guest under the
      H-extension - **the first pure-Go RISC-V hypervisor.** `board/virt/vmm.go`
      builds an **Sv39x4 G-stage** map (`hgatp`, guest-physical -> supervisor-
      physical; a 16 KiB root + one 2 MiB megapage backing guest RAM at
      `0x80000000`), world-switches HS<->VS via a dedicated guest trap vector +
      `sscratch` trampoline (`board/virt/vmm_riscv64.s`: `guestEnter`/`guestVec`
      save/restore the full guest GPR file; honk's own trap path is untouched),
      and trap-and-emulates the guest's SBI (`ecall` from VS = scause 10,
      delegated to HS by OpenSBI's `medeleg`): legacy `console_putchar` ->
      honk's console, `shutdown` -> end the run. The guest is pinned to one hart
      (`runtime.LockOSThread`; CSRs/`sscratch` are hart-local) and runs with HS
      interrupts masked (no preemption needed for M11). G-stage leaf PTEs set
      the **U bit** (G-stage accesses are checked as U-mode) and pre-set A/D
      (Svade-portable); `HFENCE.GVMA` orders the MODE change. The encodable
      logic - the hand-rolled guest program and the Sv39x4 tables - is the pure,
      host race-tested `kernel/vmm` package (decodes the guest instruction-by-
      instruction; checks PTE bits/indices); the world switch is QEMU-verified
      end to end and smoke-gated (`-cpu rv64,h=true`, run `vm`, assert the
      guest's output + SBI-shutdown exit). The Go assembler already knows the
      H/VS CSR names, so no `tamago-go` fork was needed. Shell `vm`. **This is
      the only paging in honk.** See `docs/STATUS.md`.
13. **M12 Small guest.** rCore or RT-Thread: exercise SBI, a timer, and a simple
    driver path. vCPU = goroutine.
    - *Status:* **COMPLETE + verified** (the M12 *mechanisms*, against a payload
      honk controls - the M11 discipline; a real third-party guest image rides
      on M13's device backends). honk now delivers a guest a **timer** and is
      **preemptible**: the guest arms an SBI `set_timer`; honk records the
      deadline and arms its *own* HS timer; when that fires *during* the guest
      it **injects a VS-timer interrupt** (`hvip.VSTIP`, delegated to VS via
      `hideleg`), which the guest takes in its own VS trap vector (it also sets
      `sie.STIE`/`sstatus.SIE`, and honk sets `hcounteren.TM` so the guest can
      read `time`). honk keeps its HS timer armed for at most a quantum, so it
      regains the hart from a running guest periodically (a wall-clock budget
      bounds the whole run) - timer-driven preemption, the safety net against a
      runaway guest. The SBI surface grew to **Base `probe_extension` + TIME
      `set_timer`** alongside M11's legacy console + shutdown (numbers in
      `kernel/vmm/sbi.go`, effects in `board/virt`). **vCPU = goroutine:** the
      whole world-switch + trap-and-emulate loop is one `//go:nosplit`,
      allocation-free, non-yielding region - on tamago (no async preemption) it
      is never descheduled, so the vCPU goroutine cannot migrate off its hart
      (keeping the hart-local CSRs valid) and never trips the fixed-M-per-hart
      SMP model. (An initial `runtime.LockOSThread` design deadlocked exactly
      that way: a deschedule of the pinned vCPU under GC made the runtime try to
      start an M beyond the parked harts.) `kernel/vmm` gained a tiny RV64
      assembler (`encode.go`) and `TimerGuest`, host race-tested by decoding the
      generated program; the run is QEMU-verified (`vm timer` → `*****` from five
      injected timer interrupts, then SBI shutdown) and smoke-gated. Shell `vm
      timer`. See `docs/STATUS.md`.
14. **M13 Linux + virtio-fs.** Full device tree, PLIC/AIA emulation,
    virtio-fs/blk/net backends; boot a Linux guest that transparently mounts
    honk's files over virtio-fs - the escape hatch for the long tail of real
    software.
    - *Groundwork (in progress):* the mechanisms M12 deferred are landing one at
      a time, each proven against a hand-rolled guest. (1) A sized,
      multi-megapage G-stage map and the guest running its **own** VS-stage Sv39
      paging (`vsatp`) for genuine two-stage translation - shell `vm paging`.
      (2) honk reading a guest-supplied buffer from guest memory through the
      G-stage (`vmm.GuestRange` + SBI DBCN `console_write`) - the keystone for
      device backends - shell `vm dbcn`. Remaining for M13: the interrupt
      controller and virtio device backends themselves, and time-sharing a
      hart. See `docs/STATUS.md`.

---

## 8. Coding standards and quality gates (`GO.md`)

- `gofmt`/`goimports`; `MixedCaps`; small consumer-side interfaces; return the
  `error` interface (typed-nil trap); errors wrapped with `%w`.
- Concurrency is now the core of the OS, so it is held hard: every goroutine has
  a stop path via its `context`; mutex zero-values, never copy locks;
  capabilities are passed explicitly, never reached through globals; `go test
  -race` on all host-testable packages; stress tests for the multi-hart
  scheduler, IRQ-to-channel paths, KV crash-consistency.
- Modern: `log/slog`, `errors.Join`, generics for typed containers, range-over-
  func iterators where they read clearly.
- CI: `go vet`, `staticcheck`, `govulncheck`, the TamaGo `virt` build, host-side
  unit tests (proc/cap, KV, verity, WASI host bindings, NVMe/virtqueue math via
  the `GOOS=tamago` user-linux overlay), QEMU smoke tests for driver/IRQ. A
  `tamago-go` runtime bump is treated like any dependency bump and gated behind
  full CI plus the multi-hart scheduler and IRQ stress tests - the exact paths
  the fork touches.
- Footprint: every dependency earns its place; track binary size and original LoC.

---

## 9. Risks, limitations, decisions

| Risk / limit | Decision / mitigation |
|---|---|
| **No real RISC-V SMP in the runtime; needed at M0.** #1 risk. | SBI HSM + per-hart Go `M` via a minimal `tamago-go` fork. Front-loaded at M0. Biscuit proves Go multicore is achievable. |
| **GC pauses / CPU tax** in a kernel | Biscuit measured <=13% CPU and ~115us pauses - acceptable. Use `debug.SetMemoryLimit` (kotama already does), size the heap with headroom, keep hot driver paths allocation-light. |
| **No async preemption of tight loops** (TamaGo, like js/wasm) | Trusted code must yield (it is first-party); WASM modules run under epoch/`context` termination; per-hart timer ISR is the watchdog. |
| **Trusted tier relies on memory safety** | Acceptable for first-party signed code (the unikernel trust model); anything untrusted goes to the WASM or VM tier. No `unsafe` in app code; bugs panic (Biscuit). |
| **wazero is interpreter-only on riscv64** (no JIT) and unproven on `GOOS=tamago` | **Retired (M7):** a compile/link spike confirmed wazero's interpreter builds *and runs* on `GOOS=tamago GOARCH=riscv64` (its compiler backend is gated off for unknown GOOS, the arch asm excluded on riscv64). Positioning, not just risk: keep WASM for app/glue/service workloads and route compute-heavy code to a VM or compile it in. |
| **No bare-metal pure-Go NVMe/PCIe/virtio-blk/gpu** | Write them on the `kvm/virtio` framework; Go interfaces + channel-driven IRQs make them cleaner than C (Biscuit's AHCI/NIC are the model). |
| **First pure-Go RISC-V VMM** (prior art all Rust) | Phase E research; the only place honk writes paging; reuse design of salus/hypocaust-2; device backends on `kvm/virtio`. |
| **Forking `tamago-go`** | Keep it to the SMP `M`-bring-up hook; rebase each release; pin base tag + patch set for reproducible builds; gate bumps on scheduler/IRQ stress tests; upstream the hook so the fork goes to zero. |
| **Immutability / root of trust** | Software chain (signed `ed25519` header -> Merkle -> verity) is the QEMU baseline; on silicon anchor the key *hash* in OTP fuses (PMP/`mseccfg` are runtime-configured by already-trusted code, so not the true root), add an anti-rollback security-version counter (monotonic fuse) against downgrade to old validly-signed images, and key delegation (embedded key signs a rotatable subordinate). Expose as an abstract anchored/measured-boot interface, implemented per platform. |

---

## 10. Footprint and "fewest lines" accounting

Original Go we maintain (gVisor, wazero, and the stdlib are upstream and
unmodified; the runtime carries only the SMP-hook fork, counted separately):

- **Networked appliance** (M0-M8: SMP + process model + NVMe + KV + immutable +
  networking + WASM tier): **~4,000-5,000 lines** - and most of that is device
  drivers and the SMP hook, not OS logic. The pure OS-mechanism code (process
  table, capabilities, IPC, VFS) is ~1.5k, because it is the §1 table.
- **+ GUI** (M9-M10): **+ ~1,300 lines** (the stdlib `image` packages do the
  rendering).
- **+ Hypervisor** (M11-M13): **+ ~6,000-8,500 lines** - irreducible; it is the
  one component where hardware virtualization cannot be expressed as a language
  feature.
- Plus the `tamago-go` SMP fork delta: ~hundreds of lines.

The headline: for a complete OS, the "operating system" you write is small
because the language already is one; what is left is the hardware. A C/Rust
equivalent is six figures.
```
