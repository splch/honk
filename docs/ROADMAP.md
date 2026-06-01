# Honk OS — Vision & Roadmap

> **Honk is xv6 you can *read, test, and inspect* — in a memory-safe language where
> the terrifying parts (memory corruption) are gone, so you spend your attention on
> *concepts*, and where the language runtime itself becomes the final advanced lesson.**

Honk's goal is to be the **modern successor to [xv6](https://pdos.csail.mit.edu/6.1810/)**:
a small, readable, hackable teaching kernel for self-directed learners. The design
rule is xv6's: *small enough to read end-to-end*, adding modern ideas but ruthlessly
resisting feature bloat — clarity over completeness.

This isn't speculative. MIT's own [**Biscuit**](https://pdos.csail.mit.edu/papers/biscuit.pdf)
already proved a full pure-Go Unix-style kernel — mmap, copy-on-write fork, a journaled
file system, a TCP/IP stack, Go-written drivers — works in **~28k lines, zero C**. That
number is both Honk's feasibility proof and its readability ceiling.

## The spine — the curriculum every OS course covers

xv6 earns its status from two things: a book **read alongside the source** (the
Lions *Commentary on UNIX* tradition) and a fixed, dependency-ordered lab progression.
Honk reproduces that spine, done the Go way:

| Concept (xv6 lab order) | The Honk / Go way |
|---|---|
| Syscalls | a real U-mode **syscall boundary** for *user processes* (distinct from kernel goroutines) |
| Page tables | Sv39 you build and can *visualize live* |
| Traps | a user trap/syscall layer over the existing S-mode trap path |
| Copy-on-write fork | fault → `kalloc`+copy+set `PTE_W`; mark COW via RISC-V RSW bits |
| Locking | Go channels/mutexes for the *kernel*; a lock *you build* for user-space |
| Scheduler | a context switch + scheduler *you write* for user processes |
| File system | inodes, directories, a crash-recovery log |
| mmap / demand paging | on the same page-table machinery |
| Network driver | a minimal virtio-net capstone |

## The modern delta — close xv6's *self-documented* gaps

xv6's authors list its simplifications in "Real world" sections. Those are the upgrades:

| xv6's documented gap | Honk's minimal teaching form | Verdict |
|---|---|---|
| Round-robin scheduler, no priorities | a priority / EEVDF-lite scheduler as a *lab variant* | include (variant) |
| Inefficient logging FS (whole-block, synchronous) | **ordered/metadata journaling** (fsck is O(disk); journaling is O(log)) | the headline "do it better" |
| No COW / demand paging / mmap in base | include all three | include |
| No users / protection (all run as root) | a minimal uid + **W^X** on user pages | include (small, high value) |
| No signals, not POSIX | — | omit (complexity, low value/line) |

## Go's three superpowers — lean into these

1. **Memory safety changes *what you teach.*** Biscuit found Go would have improved the
   outcome of **~61% of 2017 Linux code-execution CVEs** — use-after-free eliminated by
   the GC, out-of-bounds → a clean panic instead of an exploit. Honk's curriculum drops
   C's pointer-bug hunts and teaches *concepts*, typed errors, and nil/bounds reasoning.
2. **A kernel you can `go test` and fuzz.** The differentiator xv6 cannot match: unit-test
   the file system, the allocator, the scheduler — even run subsystems in userspace on
   your laptop. Build this in from day one.
3. **Readable concurrency + type-safe hardware.** Goroutines/channels replace hand-rolled
   context switches and spinlocks for *kernel* code; type-safe MMIO register structs move
   driver bugs to compile time.

### The honesty rule (non-negotiable)

Go's runtime *hides* the GC, goroutine scheduler, and stack growth — exactly what xv6
teaches by hand. Honk must (a) **document** what's hidden and (b) teach the cost *honestly*
with Biscuit's real numbers (5–15% slower paths; up to ~13% kernel CPU for GC + stack
checks; ~115 µs worst-case GC pause). [seL4](https://cacm.acm.org/research/sel4-formal-verification-of-an-operating-system-kernel/)'s
explicit "still-trusted" list (compiler, assembler, boot code, caches, hardware) is the
perfect artifact for "a safe language doesn't make the hardware go away."

## The defining tension — and the fix

You can't teach scheduling and context-switching *the xv6 way* if the runtime hands you
goroutines. Honk turns that tension into its best feature — a **three-layer concurrency story**:

1. **You USE it** — the kernel's own concurrency is goroutines/channels (readable, safe).
2. **You BUILD it** — the curriculum has you implement real **user processes** with their
   own page tables, traps, context switch, and a scheduler *you write* — the classic
   internals, learned by building them *on top of* Honk, exactly as xv6 does.
3. **You LIFT THE CURTAIN** — an advanced module that instruments and visualizes the Go
   runtime's *own* scheduler and GC: "here's how the layer beneath you does the job you
   just built." No other teaching OS can offer this.

## Deliberately omitted (to stay minimal)

Signals · full POSIX · a full TCP stack (a minimal UDP/ping capstone is plenty) ·
io_uring-style async · containers/namespaces · hypervisor/virtualization · multi-arch ·
GUI · **full formal verification** (teach the seL4 *idea* and its assumption list; never
attempt the proof). Each is high line-count, low concept-per-line.

## Pedagogy for self-directed learners — where Honk wins

- **A companion book read against the source** — xv6's single highest-leverage asset.
  Hyperlink book ↔ code.
- **Observability as a first-class feature** — `ps`, live page-table and scheduler
  visualization, a `dmesg`/trace ring, a clean gdb story. A kernel you can *inspect* is a
  kernel you can *understand*.
- **Self-serve labs with built-in tests** — xv6's lab structure, but Go's testing makes
  "did I get it right?" self-checking, no autograder needed.
- **Gorgeous crash diagnostics** — Go panics + stack traces already dwarf xv6's.
- Minimalism bar to respect: [egos-2000](https://github.com/yhzhang0128/egos-2000) is a
  *2,000-line* readable RISC-V teaching OS. Honk's "readable surface" is *the kernel logic
  you read*; the runtime is a separate, instrumentable layer.

## The roadmap (Honk is at boot + shell)

0. **Sv39 paging** + a kernel page table → **U-mode trap/syscall layer**  *(unlocks everything)*
1. **User processes** — load + run a user program in U-mode behind syscalls  *(the "goroutines ≠ processes" leap)*
2. **fork / exec / wait** + a **virtio-blk** block driver
3. **File system** — inodes, directories, a log → the shell does real file I/O
4. **Copy-on-write fork · demand paging · mmap** (the memory labs)
5. **A scheduler you build** for user processes + a user-space lock
6. **Modern deltas** — ordered-journaling FS, minimal protection (uid / W^X), virtio-net capstone
7. **"Lift the curtain"** — instrument the runtime scheduler + GC

…with the **companion book, observability, and `go test` labs growing alongside every step.**

## References

- [xv6 book (RISC-V)](https://pdos.csail.mit.edu/6.828/2025/xv6/book-riscv-rev5.pdf) · [MIT 6.1810 labs](https://pdos.csail.mit.edu/6.1810/2025/)
- [Biscuit: a POSIX kernel in Go (OSDI'18)](https://pdos.csail.mit.edu/papers/biscuit.pdf)
- [Theseus: safe-language OS (OSDI'20)](https://www.usenix.org/system/files/osdi20-boos.pdf)
- [seL4: formal verification (CACM)](https://cacm.acm.org/research/sel4-formal-verification-of-an-operating-system-kernel/)
- [OSTEP — journaling / crash consistency](https://pages.cs.wisc.edu/~remzi/OSTEP/file-journaling.pdf)
- [egos-2000](https://github.com/yhzhang0128/egos-2000) · [Writing an OS in Rust](https://os.phil-opp.com/)
