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
make test             # host race tests of every pure-Go package
make phase-a          # Phase A (M0/M1/M2) acceptance: race tests + QEMU boot matrix
make smoke            # build + boot + assert M0-M12 output (CI gate)
```

Needs a host Go toolchain and `qemu-system-riscv64`. The TamaGo Go distribution
is downloaded and built automatically on first use. While `make run` is up,
`curl http://localhost:8080/` reaches honk's HTTP server (QEMU forwards the host
port to honk's `:80`).

## Layout

```
kernel/        the HS-mode Go program (the OS): boot, SMP demo, shell
kernel/net.go  networking: virtio-net + gVisor (go-net) -> stdlib net + net/http
kernel/wasm.go untrusted-code tier: wazero (WASI preview 1), capability-gated
kernel/display.go  framebuffer: virtio-gpu -> image.RGBA + a test pattern (M9)
kernel/vm.go   hypervisor demo: `vm` (M11 guest) + `vm timer` (M12 timer guest) + `vm paging` (M13 two-stage-paging guest)
kernel/vmm/    pure VMM logic: guest payloads + RV64 encoders + SBI numbers + Sv39x4 G-stage tables (host-tested)
board/virt/vmm*  H-extension VMM: G-stage paging, the HS<->VS world switch, SBI emulation, VS-timer injection + preemption
kernel/ui.go   GUI demo: virtio-input events -> the image/draw toolkit (M10)
kernel/gui/    minimal retained-mode widget toolkit over image/draw (host-tested)
kernel/proc/   process model: goroutine + context + capabilities (host-tested)
kernel/p9/     read-only 9P2000.L client for host files -> io/fs.FS (host-tested)
board/virt/ring/  SPSC byte ring for the IRQ->channel console path (host-tested)
block/         block-device interface + in-memory device (host-tested)
kernel/kv/     crash-safe log-structured key/value store, disk-resident (host-tested)
kernel/vfs/    io/fs.FS over the kv store + overlay on the verified core (host-tested)
kernel/image/  signed, Merkle-tree'd immutable core image; A/B + anti-rollback (host-tested)
board/virt/    QEMU virt board: startup, SMP, traps, PLIC, UART, PCIe/NVMe, virtio-blk/net/9p/gpu/input, SBI
tools/         build.sh, vet.sh, run-qemu.sh, smoke-test.sh, phase-a-test.sh, screendump.py, uitest.py, mkboot + mkimage
HONK.md        full design and roadmap
docs/STATUS.md what works today and what's next
GO.md RV64.md OS.md   language / hardware / domain references
```

Status: **Phase A-D complete (M0-M10), Phase E under way (M11-M12)** - HS-mode boot under
OpenSBI, SMP across all harts, an interrupt-driven UART console + shell, a
process model (goroutine + context + capabilities, `recover()` fault domains), a
persistent block device (NVMe-over-PCIe + virtio-blk fallback), a crash-safe
log-structured kv store, an immutable, Ed25519-signed + Merkle-verified core
image (A/B slots with fallback, anti-rollback, stateless reset) served read-only
under the writable kv overlay, **networking** - honk's own virtio-net driver +
the gVisor TCP/IP stack (`go-net`) lighting up the stdlib `net` package, with a
`net/http` server on `:80` - a **WASM/WASI tier** (wazero interpreter) that
runs untrusted, any-toolchain modules as capability-gated, killable honk
processes, and **host files** - a hand-rolled read-only 9P2000.L client over
honk's own virtio-9p driver, mounting a QEMU-shared host directory as an
`io/fs.FS` unioned into the overlay, and a **framebuffer** - honk's own
virtio-gpu driver presenting the scanout as a stdlib `image.RGBA` it draws a
test pattern into (output-first), and **GUI + input** - a polled virtio-input
driver (keyboard + tablet) feeding a minimal pure-Go `image/draw` toolkit
(`kernel/gui`: a focus-routing `UI`, a `Button`, a `TextField`, bitmap-font
text), with an interactive demo you can click and type into, and the start of
**Phase E - the hypervisor** (M11): honk hosts a VS-mode guest via the RISC-V
H-extension, building an Sv39x4 G-stage page table (`hgatp`; the only paging in
honk), world-switching HS↔VS, and trap-and-emulating the guest's SBI calls -
proven by a tiny hand-rolled guest that prints via emulated SBI and halts, and a
**timer + preemptible vCPU** (M12): honk emulates a broader SBI surface (Base
`probe_extension` + TIME `set_timer`) and delivers a guest's timer by injecting
a VS-timer interrupt (`hvip.VSTIP`) when the deadline passes, while its own HS
timer makes the running guest preemptible - proven by a hand-rolled guest that
arms a timer, handles each injected interrupt in its own VS trap vector, and
halts. `make run` drops you at a `honk>` prompt; try `help`, `mount`, `ls`,
`cat motd`, `net`, `wasm hello.wasm`, `fb`, `ui`, `vm`, `vm timer`, `vm paging`,
`reset --confirm`, and `curl http://localhost:8080/`. **Phase D (display + GUI) is
complete** and **Phase E is under way** - all verified headlessly (QMP
screendump + input injection for the GUI; H-extension guest runs for the VMM);
next: M13 - a real guest (Linux) with virtio device backends. See
`docs/STATUS.md`.
