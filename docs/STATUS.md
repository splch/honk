# honk - build status

Living record of what is implemented and verified, and what is next. See
`HONK.md` for the full design and roadmap.

## Quickstart

```sh
make run        # build + boot under QEMU virt (Ctrl-A x to quit)
make test       # host race tests of every pure-Go package
make smoke      # build + boot + assert M0-M7 output (CI gate)
make phase-a    # Phase A (M0/M1/M2) acceptance: race tests + QEMU boot matrix
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

### Phase A acceptance test

Phase A is the foundation the whole roadmap stands on, so it has a dedicated
acceptance gate (`make phase-a`, `tools/phase-a-test.sh`) on top of the broad
M0-M5 smoke test. The split is by where the authority for correctness lives:

- **Pure-Go logic → host `-race` unit tests.** The M2 process state machine
  (`kernel/proc`, 23 tests) and the M1 SPSC console-input ring
  (`board/virt/ring`, extracted from the trap path so it is host-testable, 5
  tests incl. an adversarial single-producer/single-consumer race) are exercised
  under `go test -race`, which is authoritative for the concurrency correctness
  the bare-metal trap path can never reach reliably (wraparound, drop-on-full
  backpressure, the killed-vs-finished and panic-overrides-killed state guards).
- **Hardware-contact behavior → QEMU integration.** SMP bring-up across
  `-smp 1/4/8` (every hart becomes a Go M, `GOMAXPROCS=nharts`,
  boot-hart-agnostic), `RamSize` derivation from the DTB across `-m 256M..2G`,
  the UART-RX-IRQ→PLIC→ring→channel→shell input path with line editing, the
  fatal trap path (`fault` → EBREAK → `scause=3`/`sepc`/`stval` → SBI poweroff),
  and the live process model on 4 harts (`run`/`ps`/`kill`/`crash`/`reap`/
  `stress`, with a post-`crash` command proving the `recover()` fault domain
  held). Phase A attaches **no** block device - it boots on the embedded,
  verified core alone - so the foundation is tested in isolation from M3+.

  (Re-deriving the PLIC context math or the memory-size formula in a host test
  would only check the constants against themselves; the hardware is the
  authority, so those are proven in QEMU - the console working at all proves
  PLIC routing; booting and running the GC across RAM sizes proves the arena
  sizing; goroutines spreading across N/N harts proves the SMP hand-off.)
| **M3 block device** | ✅ **complete** | `block.Device` interface with two backends: **NVMe-over-PCIe** (PCIe ECAM enumeration + BAR assignment, controller bring-up, admin+I/O queues, identify, PRP read/write - primary) and **virtio-blk** (virtio-mmio v2, split virtqueue - fallback). Both implement a `Flush` cache barrier. `blk` shell self-test; both verified for detection, read/write round-trip, and on-disk persistence; smoke test gates both. |
| **M4 KV store + VFS** | ✅ **complete** | Crash-safe log-structured KV (`kernel/kv`) over `block.Device` - single-appender group commit, lock-free COW snapshot reads, double-buffered superblock, atomic-checkpoint compaction, replay-to-sequence-floor (safe region reuse). **Durable by default** (each batch flushes before ack; compaction flushes the region before the superblock switch, so the checkpoint is host-crash-safe). **Hybrid value store:** small values inline in memory, larger ones disk-resident (index holds a verified pointer), so the store is not RAM-bounded. Exposed as `io/fs.FS` (`kernel/vfs`) overlaid on the embedded core; `ls`/`cat`/`cp`/`put`/`rm` shell. Host race-tested (torn-tail, region-reuse leftovers, compaction, disk-resident, a 600-op crash-consistency property test, `fstest.TestFS` incl. the overlay), reboot persistence verified in QEMU. |
| **M5 immutable core** | ✅ **complete** | Signed, Merkle-tree'd core image (`kernel/image`): a header with the SHA-256 Merkle root, an anti-rollback security version, and a file-table hash, signed with Ed25519. Boot `Verify`s the signature against an abstract **anchor** (QEMU = embedded dev key; silicon = OTP-fused key + monotonic counter behind the same interface), the version floor, the table hash, the Merkle root, and per-file bounds - failing closed. The device is partitioned (`block.Slice`) into **A/B image slots + the kv region**; boot `Select`s the highest valid version and falls back across the rest, with the embedded factory image as the guaranteed-good last candidate. The verified core is served read-only (`vfs.FilesFS`) under the writable kv overlay. **Stateless reset** (`kv.Reset`, shell `reset --confirm`) clears the writable layer crash-safely. `tools/mkimage` builds/signs images. Host race-tested (build/verify, tamper, anti-rollback, A/B + fallback, slot I/O); QEMU-verified (verification, A/B select + fallback, reset). |

| **M6 networking** | ✅ **complete** | **virtio-net + the gVisor TCP/IP stack**, lighting up the stdlib `net` package. honk's own virtio-net driver (`board/virt/virtionet.go`, on the shared virtio-mmio transport `virtio.go`) is a `go-net` `NetworkDevice`; `gnet.NewGVisorStack` is the stack; `net.SocketFunc = stack.Socket` makes `net.Dial`/`Listen` - and thus `net/http`, `crypto/tls`, etc. - work unchanged. honk serves an HTTP status page on `:80`. QEMU-verified end to end: a host `curl` through SLIRP `hostfwd` reaches honk's `net/http` server (driver -> gVisor -> stdlib `net.Listener`), gated by the smoke test. The gVisor reuse was de-risked by a compile/link spike (gVisor links on `GOOS=tamago GOARCH=riscv64`); `go-net/virtio` is amd64-only, so honk supplies its own driver. |

| **M7 WASM/WASI tier** | ✅ **complete** | **wazero interpreter + WASI Preview 1** - the untrusted/dynamic/any-toolchain isolation tier (`kernel/wasm.go`). A WASM module is a honk **process**: it runs as a goroutine under a `context`, so `kill` (context cancel) + wazero's `WithCloseOnContextDone` terminate even a tight-looping module. **Capability-gated:** honk *implements* the WASI host funcs once but *grants* nothing by default - console (stdout/stderr) and filesystem (read access to the overlay) are passed per module via `ModuleConfig` from the process's `Caps`. Two hand-encoded sample modules ship in the verified core (`hello.wasm`, `loop.wasm`); shell `wasm <file>`. QEMU-verified (run a WASI module -> `honk from wasm`; kill a runaway loop) and smoke-gated. Risk retired by a compile/link spike first: wazero's interpreter builds on `GOOS=tamago GOARCH=riscv64` (the compiler/JIT backend is correctly disabled). |

**Phase A + B complete; Phase C (the everyday networked OS) complete: M6 networking + M7 WASM/WASI.** Next: M8 host files (9p-over-virtio as an `fs.FS`).

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

The trap handler now runs on a **dedicated per-hart trap stack** (trap.go
`trapStacks`, selected via `sscratch`), so a trap never touches the interrupted
goroutine's stack - important because honk has no MMU guard pages and every
roadmap interrupt source (NVMe, virtio-gpu/input/net, the VMM) lands here.

**Deferred hardening (needs a tamago-go runtime fork):** idle harts busy-spin
in the runtime's `semasleep` rather than `WFI`. This is not a honk policy choice
- tamago wakes idle Ms with `semawakeup` (a shared-memory atomic, not an
interrupt), so `WFI` would require teaching `semawakeup` to send an IPI, i.e. a
runtime fork. Tracked as a power-management fork item alongside the (also
sanctioned) SMP work; not a correctness bug.

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

## Block storage (M3) - how it works

`block.Device` (package `honk/block`) is the storage abstraction - four methods
(`BlockSize`/`Blocks`/`ReadBlocks`/`WriteBlocks`), no transport details leaked.
It is pure Go, so the storage stack above it (the M4 KV store) is host-testable.

Two backends implement it. The **virtio-blk** driver (`board/virt/virtioblk.go`)
over virtio-mmio v2 (RV64.md §7.4) is the fallback: probe the 8 mmio slots for `DeviceID==2`, do the
reset/ACK/feature(`VERSION_1`)/`FEATURES_OK` handshake, set up one split
virtqueue, read capacity from config. Each request is a 3-descriptor chain
(header / data / status) published to the available ring; completion is polled
on the used ring (synchronous I/O behind the interface; the IRQ path is a later
optimization). honk is identity-mapped (satp=0) and its GC is non-moving, so a
pinned `[]byte` is DMA-addressable at its own address - no separate DMA arena.

QEMU needs `-global virtio-mmio.force-legacy=false` (its virtio-mmio defaults to
legacy v1; honk targets the modern v2 transport) plus a `-drive` +
`virtio-blk-device`. The `blk` shell command runs a write/read-back self-test;
persistence to the backing file is verified.

The **NVMe-over-PCIe** backend (`board/virt/nvme.go` + `pci.go`) is the roadmap
primary: enumerate PCIe ECAM (0x30000000) for class 01/08/02, assign BAR0 in
the MMIO window (OpenSBI doesn't), enable memory + bus-master, bring the
controller up (reset, admin SQ/CQ, `CC.EN`), create one I/O queue pair, and
identify namespace 1 for capacity/LBA size. Read/Write commands carry data via
PRP pointers; transfers are split to at most one page so PRP1[+PRP2] suffice
(no PRP lists), exploiting the buffer's physical contiguity. Completions are
polled on the CQ phase tag. `ProbeBlock` selects NVMe if present, else
virtio-blk - nothing above storage knows the difference.

## KV store + VFS (M4) - how it works

`kernel/kv` is a crash-safe, log-structured key/value store over `block.Device`,
mapping the design onto Go primitives (HONK.md §1):

- **One appender goroutine** owns all writes; callers send requests on a channel
  and the appender drains them in a batch - the drain *is* the group commit
  (one device write per batch, no write locks).
- **Lock-free reads:** the index is an immutable map published via an atomic
  pointer; the appender copies-on-write and swaps it, so `Get` never blocks.
- **Durability is the log:** records are CRC-checksummed and carry a strictly
  increasing sequence number; `Open` replays the active region, stopping at the
  first torn/absent record *or* the first record below the region's recorded
  sequence floor. The floor (`startSeq`, in the superblock) is what makes region
  reuse safe: compaction and reset reuse a region without erasing it, so its
  tail still holds **valid** records from a previous life - all with sequence
  numbers below the floor, so replay stops at the true log end instead of
  resurrecting them. A crash leaves at most an unacknowledged tail, discarded
  and overwritten.
- **Compaction is an atomic checkpoint:** the live set is rewritten into the
  other of two log regions (under a fresh sequence floor), then a
  *double-buffered* superblock is switched with a single (atomic) block write. A
  crash mid-compaction leaves the old superblock and region intact.

**Durability is real, not just guest-crash-safe.** `block.Device` has a `Flush`
barrier (NVMe Flush; virtio-blk `VIRTIO_BLK_T_FLUSH` when negotiated; a no-op on
the memory device). Every commit batch flushes before the Put is acknowledged
(durable by default; group-commit amortizes the flush under concurrency), and
compaction flushes the rewritten region *before* the superblock points at it -
so a host power loss can never leave the atomic checkpoint referencing unflushed
data. (An async/loss-window mode with an explicit `Sync()` is a future opt-in;
the correct, simpler default is durable-per-batch.)

**Values are disk-resident above a threshold.** The index holds small values
inline (lock-free, no I/O) but spills larger values to the log and keeps only a
pointer, so steady-state memory is bounded by keys + small values, not total
value bytes. `Get` reads a spilled value from its record and *verifies*
magic+CRC+key before returning, so a location recycled by two compactions is
detected and the read retried (correct, not just probable). `Has`/`Size` answer
from the index with no I/O; compaction streams disk-resident values one at a
time as it rewrites them. The on-disk format is unchanged - residency is purely
an in-memory index decision.

`kernel/vfs` exposes the store as a nested `io/fs.FS` (directories synthesized
from slash-separated keys) and a union `Overlay`: the writable kv FS over the
read-only embedded core (`//go:embed`), with the upper layer shadowing the
lower. The shell's `ls`/`cat` read through the overlay; `cp`/`put`/`rm` write
through the kv store. All of kv/vfs is pure Go: `go test -race ./kernel/kv
./kernel/vfs` covers put/get/delete, replay, torn-tail, region-reuse leftovers,
compaction, disk-resident corruption, concurrency, and `fstest.TestFS` (incl.
the overlay). A **crash-consistency property test** drives 600 randomized
ops and, after every acknowledged operation, simulates power loss and reopens
from the durable image, asserting the recovery matches the model exactly;
reboot persistence is also verified in QEMU by the smoke test.

## Phase B - deliberately deferred (honest scope)

The storage stack is a crash-safe, durable foundation, but it is not yet a full
modern-OS storage stack. By design (HONK.md) or as tracked future work:

- **Read-only / whole-value FS semantics.** `io/fs.FS` composition, not POSIX:
  no partial writes, append, truncate, rename, mmap, metadata (timestamps,
  permissions), or empty directories. Writes are whole-value `Put`.
- **`Open` materializes the whole file** (no streaming), so reading a file
  needs RAM proportional to its size. Fine for config + modest files; large-file
  streaming reads are future work.
- **Polled, serialized block I/O** (one request at a time, single NVMe queue,
  no I/O scheduler, no page cache) - the IRQ-driven path is deferred per the
  HONK.md async-I/O model.
- **Stop-the-world compaction** and ~2x space (A/B regions); a value/dataset is
  bounded by a region (~half the device).
- **Target-specific:** minimal PCIe (bus 0, fixed BAR, no MSI-X), thin NVMe
  error/timeout handling, and reliance on QEMU's coherent DMA (no cache
  maintenance for non-coherent real hardware).

## Immutable core (M5) - how it works

`kernel/image` is honk's root-of-trust for the read-only core, mapped onto
stdlib crypto (HONK.md §2):

- **Format.** `tools/mkimage` packs `kernel/core/` into a blob: a fixed,
  Ed25519-signed header (magic, format/security version, geometry, SHA-256
  **Merkle root** over `LeafSize` data blocks, and a hash of the file table)
  followed by the table and the data. The layout is deterministic (sorted
  names), so builds are reproducible.
- **Verify, fail closed.** `Verify` checks, in order: format, the signature over
  the header (`crypto/ed25519`), the anti-rollback floor, the file-table hash,
  the Merkle root recomputed over the data, and every file's bounds. Any tamper
  anywhere is caught before a byte is served.
- **The anchor.** The trust root is the `Anchor` interface. QEMU uses
  `SoftwareAnchor` (the dev public key embedded as `image.DevPublicKey`, matched
  by the fixed dev seed in `mkimage`); real silicon backs the same interface
  with an OTP-fused key hash and a monotonic security-version counter. The dev
  key signs only local QEMU images and is committed-safe.
- **A/B slots.** The block device is partitioned with `block.Slice` into image
  slot A, slot B, then the kv region. Boot reads both slots, considers them plus
  the embedded factory image, and `Select`s the valid candidate with the highest
  security version - invalid/corrupt candidates are skipped (verify-then-switch
  with fallback), and the factory image is the guaranteed-good last resort. A
  device too small to host slots runs kv-only on the embedded core.
- **Served read-only.** The verified files become an `io/fs.FS` via
  `vfs.FilesFS`, overlaid under the writable kv store (`vfs.Overlay`), so user
  writes shadow the immutable core without altering it.
- **Stateless reset.** `kv.Reset` (shell `reset --confirm`) publishes a fresh
  empty region and switches the double-buffered superblock with one atomic
  write - the same crash-safe checkpoint as compaction - so a reset clears all
  writable state and the immutable core shows through unshadowed.

All of `kernel/image` is pure Go: `go test -race ./kernel/image` covers the
build/verify round-trip, signature/Merkle/table tamper detection, anti-rollback,
A/B selection + fallback, and slot read/write. The QEMU smoke test seeds two
slots to verify selection of the newer image and fallback when it is corrupted,
and exercises `reset`.

## Networking (M6) - how it works

`kernel/net.go` brings up TCP/IP by *reusing* a stack, not writing one (HONK.md
§1): the gVisor netstack via `usbarmory/go-net` (`gnet`), driven by honk's own
virtio-net driver, exposed through the stdlib `net` package.

- **Driver (`board/virt/virtionet.go`).** A split-virtqueue virtio-net device on
  honk's shared virtio-mmio v2 transport (`board/virt/virtio.go`, now the single
  owner of the register map + handshake for both virtio-blk and virtio-net). It
  exposes exactly what `gnet.NetworkDevice` needs - `Receive`/`Transmit` of raw
  Ethernet frames - plus the device MAC. The receive queue is pre-filled with
  device-writable buffers and polled (recycled on each `Receive`); transmit is
  synchronous and serialized. Polled, not IRQ-driven, matching honk's deferred
  async-I/O model; no `tamago/dma` - honk's one `dmaAlloc` mechanism serves it.
- **Stack + stdlib bridge.** `gnet.NewGVisorStack(1)` over a `gnet.Interface`
  whose receive pump (`Receive` -> `RecvInboundPacket`) runs as a goroutine.
  honk configures the QEMU SLIRP address statically (`10.0.2.15/24`, gw
  `10.0.2.2`) and sets `net.SocketFunc = stack.Socket`, so `net.Dial`/`Listen`,
  `net/http`, and `crypto/tls` work unchanged. `EnableICMP` answers pings.
- **Proof.** honk runs a stdlib `net/http` server on `:80`. The smoke test boots
  with a virtio-net device whose `:80` is `hostfwd`'d to a host port and `curl`s
  it - hermetic (no external network), exercising driver -> gVisor -> stdlib
  `net` -> `net/http` end to end. Shell: `net` reports the interface.

**De-risking note.** gVisor on `GOOS=tamago GOARCH=riscv64` was the milestone's
#1 risk (it is large and arch-specific). A compile/link spike confirmed it links
on riscv64 *before* any honk code was written; the spike also found `go-net`'s
own `virtio` device driver is amd64-only (its PCI transport imports
`tamago/amd64`), which is why honk implements the `NetworkDevice` on its own
virtio-mmio transport instead.

**Honest caveat.** Pulling in `net/http` + `crypto/tls` + gVisor grows the kernel
image substantially (~3 MB -> ~11 MB) - the cost of the stdlib + gVisor reuse
(the point of the milestone); LoC stays tiny. Outbound TLS/HTTP, SSH, DNS/NTP,
`/pprof`, `/statsviz`, and a `crypto/mlkem` demo are the obvious next additions
on this foundation.

## WASM/WASI tier (M7) - how it works

`kernel/wasm.go` embeds **wazero** (interpreter mode - no JIT on riscv64; the
interpreter is OS-agnostic) as honk's tier-2 sandbox for untrusted, dynamic,
any-toolchain code (HONK.md §1). A compile/link spike retired the milestone's #1
risk first: wazero builds on `GOOS=tamago GOARCH=riscv64` (its compiler backend
is gated off for unknown GOOS, so the interpreter is used; the arch-specific
compiler asm is excluded on riscv64).

- **A WASM module is a honk process.** `runWASM` spawns it via the M2 process
  table (goroutine + `context`). The runtime is built with
  `WithCloseOnContextDone(true)` and the module runs under the process context,
  so `kill <pid>` (context cancel) aborts even an uncooperative tight loop -
  which is exactly why uncooperative code belongs in this tier, not as a trusted
  goroutine. Verified: `wasm loop.wasm` then `kill` reaps it `killed`.
- **Capability discipline (implementing != granting).** honk instantiates the
  WASI Preview 1 host module *once* on the shared runtime, but a module is
  *granted* nothing by default: its `ModuleConfig` is built from the process's
  `Caps` - `CapConsole` grants stdout/stderr (routed to honk's console),
  `CapBlock` grants read access to the overlay filesystem (`WithFS(root)`). A
  module with no caps can compute but cannot observably touch the outside.
- **Proof.** Two tiny hand-encoded modules ship in the verified core image:
  `hello.wasm` (calls `fd_write` -> `honk from wasm`) and `loop.wasm` (an
  infinite loop, for the kill demo). Both were validated under wazero on the
  host before being committed. Shell: `wasm <file.wasm>`. The smoke test runs
  `wasm hello.wasm` and asserts the output.

**Honest scope.** Interpreter-only (no JIT on riscv64), so WASM suits
app/glue/service workloads; compute-heavy code should be compiled in or pushed
to a VM (Phase E). Capability *grants* are coarse (console / fs read); a
per-call capability check and a richer manifest are future work, as is the path
to WASI Preview 2 / the Component Model (a module's WIT world becomes its
manifest) as wazero matures.

## Next: M8

Host files: 9p-over-virtio exposed as an `io/fs.FS` and unioned into the overlay
(the virtio-fs device backend lands in Phase E). That completes Phase C's file
story.
