# Modern Computing: What Machines Can Do and What People Actually Do With Them

*A synthesis of two deep-research reports — the **capability** side (modern operating systems) and the **demand** side (everyday human usage) — with the gap between them analyzed explicitly.*

---

> ⚠️ **Provenance & caveat.** This document combines two autonomously generated deep-research
> reports. It synthesizes web sources and may contain errors, omissions, or hallucinated
> content. **Independently verify every load-bearing claim before relying on it.** Per
> ICMJE/COPE/WAME consensus, AI cannot be an author; attribute this to the human who
> initiated and verified it.
>
> **Source report A — "Modern operating systems' capabilities"** · 30 sources · 0 dead links · citation audit clean · effort=complex.
> **Source report B — "How people use their computers everyday"** · 25 sources · 4 dead links (2 load-bearing) · citation audit clean · effort=complex.

**Confidence markers used throughout:**

| Marker | Meaning |
|---|---|
| ✓ | Corroborated by a fetched primary/strong source |
| ◐ | Single-source; plausible but not cross-checked |
| ? | Flagged uncertain / no primary source fetched |
| 💀 | Cited URL failed link verification |

**Citation scheme:** `[O#]` = OS-capabilities report sources; `[U#]` = usage report sources. Both lists appear at the end.

---

## Executive summary

Two truths sit in tension.

1. **The operating-system frontier has never been more capable.** Linux replaced its 16-year-old fair scheduler with EEVDF (6.6, Oct 2023) ◐, made full schedulers loadable as BPF programs (`sched_ext`, 6.12) ✓, is rebuilding memory management around folios ✓, ships a production async-I/O engine (`io_uring`) with zero-copy send and receive ✓, and promoted **Rust to a canonical kernel language** alongside C and assembly (Dec 2025) ✓. Hardware-backed isolation (SEV-SNP, TDX, ARM CCA) and minimal-TCB designs (Firecracker microVMs, the formally verified seL4 microkernel) push the security envelope hard [O1][O5][O8][O14][O15][O17][O20][O21].

2. **Almost none of that frontier is visible to the people using computers.** For most humans "using a computer" now means **using a phone**: smartphones reach ~96.5% of internet users and serve 63%+ of web page views ◐, and daily life is dominated by video (~3h13m/day), social media (~2h21m/day), and messaging [U1]. The single fastest-moving behavioral shift — generative AI — went from near-zero to **34% of U.S. adults having ever used ChatGPT** by early 2025 ◐ [U7].

The connective tissue between the two: **the capability frontier mostly serves the cloud and the developer, while end-user behavior is mediated through phones and a handful of apps.** The places the two genuinely meet are async I/O (which powers the servers behind every app), confidential computing (which underpins the cloud serving consumer AI), mobile-first kernel design, and the brand-new generative-AI workload. The rest of the frontier is infrastructure most users will never name.

A shared methodological warning runs through both halves: **the headline numbers don't reconcile.** OS feature claims hinge on exact kernel versions that secondary sources frequently get wrong, and usage figures from self-report vs. passive metering correlate only **r=0.38** [U12]. Treat every single number below as an estimate.

---

# Part I — The capability frontier: what modern operating systems can do

## 1. CPU scheduling: EEVDF, real-time, and the Windows contrast

Linux's fair-scheduling class was reworked around **EEVDF** (Earliest Eligible Virtual Deadline First, from a 1995 Stoica/Abdel-Wahab paper), merged for **6.6** [O1] ◐. EEVDF keeps CFS's weighted-fair-queuing goal but discards its fragile interactivity heuristics: each task accrues *lag* (entitled minus received CPU time), is *eligible* only when lag ≥ 0, and among eligible tasks the scheduler runs the one with the earliest *virtual deadline*. Latency-sensitive tasks request **shorter slices** (via latency-nice / `sched_setattr`) for faster wakeups **without** more total CPU [O1] ◐. *(Secondary reporting that CFS was fully removed and EEVDF became sole fair scheduler in 6.12 could not be confirmed against a primary source ?.)*

Above the fair class sit POSIX real-time policies and **`SCHED_DEADLINE`** (`sched_dl`): global Earliest Deadline First plus a **Constant Bandwidth Server** for temporal isolation. A task declares `(runtime, deadline, period)`; CBS throttles it once its per-period budget depletes. Admission control caps deadline utilization (~95% default), guaranteeing other tasks ≥5%; optional GRUB reclaiming and GRUB-PA frequency scaling exist [O2] ◐.

The **Windows dispatcher** is architecturally different: fixed-priority, preemptive over 32 levels (0–31), highest runnable thread always wins, quantum-based, with temporary priority boosts and DPCs/ISRs outranking all threads [O3] ◐. Linux concentrates "always-runs-first" semantics in RT/DEADLINE, not its default class.

## 2. Memory management: folios, huge pages, tiering, compressed swap

**Folios** (5.16, early 2022) are the central refactor: a `struct folio` is guaranteed to be the head of a power-of-two compound page, turning the page cache into a variably-sized "folio cache" and aiming to shrink the 64-byte `struct page` (~1.6% of RAM) to an 8-byte descriptor [O5] ✓◐. **Transparent huge pages** give 2MB PMD pages; newer **mTHP** lets one TLB entry cover 8 (x86) or 16 (Arm) pages [O5] ◐. **MGLRU** reclaim (reportedly 6.1, default in major distros) replaces active/inactive lists with timestamped generations ?.

**CXL memory tiering** treats CXL-attached memory as a CPU-less NUMA node; cold pages demote instead of swapping, hot pages promote back ?. The performance reality is sobering: CXL.mem is **~2.02× DDR5 load-to-use latency**, with CXL-only configs causing up to **58% slowdown** [O25][O26] ✓.

**Compressed swap:** **zswap** (compressed cache in front of a real swap device, merged 3.11; switched to zsmalloc in 6.18) and **zram** (standalone compressed RAM block device, 3.14) [O4] ◐.

## 3. Filesystems, storage, and async I/O

The cross-OS trend is **copy-on-write with integrity checking**, at different maturity:

- **ReFS** (Windows): allocation-on-write, always checksums metadata, optional data integrity streams, and **block cloning native to ordinary Windows 11 24H2 / Server 2025 copies** [O7] ✓.
- **OpenZFS 2.3** (branched 2024-10-04): RAIDZ single-disk expansion ("reflow"), Fast Dedup (~90% smaller tables), Direct I/O (>30% NVMe bandwidth in HPC checkpointing) [O11] ✓.
- **APFS**: CoW metadata, constant-time `clonefile(2)` clones, snapshots underpinning Time Machine — but checksums metadata only ?.
- **bcachefs**: merged 6.7 (Jan 2024), removed ~6.17 amid maintainer disputes ?.

**Zoned storage / ZNS** moves the FTL to the host (append-only zones, no on-device GC). Linux timeline: F2FS 4.10 → dm-zoned 4.13 → zonefs 5.6 → Zone Append 5.8 → NVMe ZNS 5.9 → btrfs 5.12 → Zone Write Plugging 6.10 → native XFS zoned 6.15 [O6] ✓.

**`io_uring`** is the Linux async-I/O standard: a submission/completion ring pair shared with the kernel, few/no syscalls, registered buffers/files. Created by Jens Axboe, first merged in **5.1** (2019) ◐. **Zero-copy send** (`IORING_OP_SENDZC`, 6.0) shows >200% throughput vs `MSG_ZEROCOPY` ✓; **zero-copy receive** (`IORING_OP_RECV_ZC`, ~6.15) delivers payloads into registered userspace memory via NIC header/data split while the kernel still processes headers — unlike DPDK's full bypass [O8][O9][O10] ✓.

## 4. Kernel programmability and safety: eBPF, sched_ext, Rust, DPDK

- **`sched_ext`** (6.12): a complete scheduling policy as BPF programs, hot-loadable, auto-reverting to the default class on any verifier error, stalled task, or SysRq-S; unstable ABI [O15] ✓.
- **eBPF** safety work in 2024–25 shifted to **hardening**: an NCC Group verifier review (Nov 2024), a $228,200 Alpha-Omega grant funding KASAN on JIT'd eBPF and x86-64/arm64/riscv64 JIT audits [O12] ◐.
- **Rust for Linux**: infrastructure in **6.1** (Dec 2022); first production drivers in **6.8** (Mar 2024); RISC-V support in 6.10; promoted from experimental to **canonical kernel language** at the Dec 2025 Kernel Developers Summit. Footprint now spans Binder, `rnull`, an NVMe driver, Apple AGX, the **Nova** NVIDIA GPU driver, and the **Tyr** Arm Mali driver [O13][O14] ✓.
- **DPDK**: userspace kernel-bypass (EAL + poll-mode drivers, hugepages, lockless rings, zero-copy mbufs). Provides only packet I/O — TCP/IP must come from userspace stacks (mTCP, F-Stack, VPP/FD.io). ~10 Gbps/core, ~100 Gbps with 1–2 cores, at ~100% CPU and loss of kernel observability — the trade-off keeping XDP attractive [O16] ✓◐.

## 5. Virtualization, containers, and isolation

Modern isolation is a spectrum of trust boundaries trading attack surface against compatibility:

- **MicroVMs:** AWS **Firecracker** is a minimalist Rust VMM over KVM exposing only ~5 virtio devices, treating every vCPU thread as hostile, with seccomp + jailer defense-in-depth [O17] ✓. The thesis (shared with unikernels): the **hardware VM boundary is smaller and better-understood than a general-purpose syscall boundary** [O17][O19] ✓.
- **Landlock** (5.13+): a stackable LSM letting unprivileged processes irrevocably sandbox themselves with deny-by-default filesystem/TCP rules; ABI-versioned through v9 [O18] ✓.
- **Unikraft**: modular library-OS for single-address-space unikernels; KPTI and SMAP become unnecessary in a single-app unikernel [O19] ✓.

## 6. Hardware-backed confidential computing

- **AMD SEV-SNP**: memory-integrity via a Reverse Map Table, VM Privilege Levels (paravisor/vTPM), separate on-die Secure Processor [O21] ✓.
- **Intel TDX**: Trust Domains via Secure Arbitration Mode (`SEAMCALL`), signed ACM module, SGX-based attestation [O21] ✓.
- **ARM CCA**: "Realms" via a Granule Protection Table + Realm Management Monitor at EL3; least mature [O21] ✓.

**Overhead is workload-dependent and contested:** ~2–16% on many cloud workloads, but up to **~431%** on sleep-heavy (NPB) and **~60%** on network I/O, plus operational penalties (limited live migration, degraded introspection). Vendor benchmarks cite 2–8%; peer-reviewed studies report worst-case 60–431% [O27][O28] ✓.

## 7. Specialized architectures and Darwin/XNU

- **seL4**: ~10K SLOC C with ~500K lines of Isabelle/HOL proof; machine-checked functional correctness, integrity, and information-flow confidentiality. Drivers run in user mode outside the verified TCB. The **Microkit** (v2.0, Mar 2025) enables "push-button" verification, underpinning LionsOS. Trade-off: tiny verifiable TCB vs. ~50 lines of proof per line of C [O20][O30] ✓.
- **Apple XNU**: hybrid Mach + FreeBSD-derived BSD layer + C++ I/O Kit; Unix syscalls run as in-kernel calls. Shifting toward microkernel-style isolation (DriverKit dexts, Exclaves in macOS 14.4/iOS 17) and heterogeneity-aware scheduling on Apple Silicon [O29] ✓◐.

## 8. The frontier's documented counter-evidence

- **`io_uring`:** Google reported **60% of winning kernel-exploit submissions** targeted it; consequently disabled it in ChromeOS, for Android apps, and on production servers — "safe only for trusted components" [O22] ✓.
- **eBPF:** the verifier itself recurs as an LPE source (CVE-2021-3490, CVE-2023-2163, …), and in-kernel programs create an EDR blind spot exploited by BPFDoor [O24] ✓◐.
- **Rust-for-Linux friction is organizational, not technical:** the Feb 2025 Asahi maintainer resignation cited "nontechnical nonsense" and stonewalling [O23] ✓ (one-sided account, but resignation facts widely reported).

---

# Part II — The demand side: how people actually use computers

## 1. Time and device: "computer" mostly means phone

The most-quoted global figure: the typical internet user 16+ spends **6h38m/day online** (GWI Q3 2024, via DataReportal *Digital 2025*) — **self-reported, not measured** [U1] ◐.

Two metrics tell different stories and are routinely conflated:

- **By reach/web traffic:** mobile dominates — smartphones reach ~96.5% of users; laptop/desktop reach fell to 61.5% (Q3 2024) from 72.3% (Q1 2020); mobile is 63%+ of global page views [U1] ◐.
- **By self-reported time:** much closer — ~3h46m mobile vs. ~2h52m "computers" (a GWI category that **includes tablets**), i.e. roughly **57% mobile / 43% computers** [U1] ◐. *(DataReportal's prose says "computers account for just under 57%," which contradicts its own minute figures — the defensible reading is mobile ~57%.)*

The strongest **passively measured** counterpart: Ofcom's *Online Nation* (Ipsos iris metered panel, UK adults) put daily online time at **4h20m in 2024** and **4h30m in 2025**, ~75% on smartphones [U3][U4] ✓ — well below GWI's self-reported global figure, but measuring different things on a different population [U1][U3] ?.

## 2. The activity mix: video and social own attention

Ranked by daily time: video, then social, then audio and gaming. GWI puts TV at ~3h13m/day and social at ~2h21m/day (down from ~2h31m in 2022); music ~1h25m, console gaming ~1h03m, podcasts ~52m [U1] ◐. Short-form clips (~6h42m/wk) now beat long-form online video (~4h57m/wk), and online video has overtaken all TV in weekly time [U5][U8] ◐.

Streaming hit a milestone: Nielsen's metered panel found streaming reached **44.8% of US TV in May 2025**, beating broadcast + cable combined for the first time, with YouTube the largest single streamer (12.5%) [U6] ✓. *(The ~15-point gap between GWI's self-reported US streaming share (59.4%) and Nielsen's metered 44.8% cleanly illustrates that survey and panel data aren't interchangeable.)*

Communication/search are the top *reasons* to go online but consume less measured time than passive scrolling: "finding information" (62.8%) is the leading motivation; WhatsApp is the most-used messenger (~54% monthly); email is broadly used (75% monthly) [U1][U5] ◐. Online shopping is frequent but low-duration — and **computers still account for nearly half of ecommerce purchases** [U1] ◐.

## 3. What's genuinely new in 2023–2025

- **Generative AI** — the fastest-moving shift, essentially absent before late 2022. Pew (n=5,123, Feb–Mar 2025): **34% of U.S. adults have ever used ChatGPT**, ~2× the 2023 share, with a steep age gradient (58% under-30 → 10% of 65+) and education gradient [U7] ◐. GWI Q1 2025 (240k+ adults): **22.3% of global online adults** used ChatGPT in the past month [U8] ◐. ChatGPT reported ~500M weekly active users (Mar 2025) [U8] ✓. *(Pew's 34% "ever" and GWI's 22.3% "past month" are complementary — different denominators.)*
- **Short-form video** tilted consumption decisively online; the skew is generationally extreme (women 16–24: ~19h46m/wk on social/short-video feeds) [U8] ◐.
- **Remote/hybrid work plateaued** above pre-2020 levels: BLS ATUS 2024 shows **33% of employed Americans worked at home** on work days (vs. 35% in 2023), sharply class-stratified — **50% of degree-holders vs. 18% high-school-only** [U2] ◐. *(A separate BLS survey (CPS) put the April 2025 telework rate at ~21.6% — a measurement gap, not a real disagreement.)*

## 4. Differences by age, gender, income, region

- **Age** is the strongest axis: daily online time declines steeply with age (GWI: women 16–24 ~7h32m vs. 55–64 ~5h17m); streaming exceeds linear TV only among 16–24s; men 65+ are the only cohort more likely to use a computer than a phone [U1][U10] ◐.
- **Gender** gaps are modest but consistent: women spend more on social; men skew to news/sports; AI use skews male (UK: 50% of men vs. 33% of women had used genAI by 2024) [U1][U3] ◐. US smartphone ownership is at parity (men 90%, women 91%) [U9].
- **Income/education** drive *how*, not *whether*: smartphone ownership 91% overall but 82% (<$30k) → 97% ($100k+). **16% of U.S. adults are "smartphone-dependent"** (no home broadband), rising to 34% of those under $30k — the phone is their only computer [U9] ◐.
- **Region** varies widest: daily online time from South Africa (9h24m)/Brazil (9h13m) down to Japan (<4h); computers remain common in wealthy markets (Europe 75.5%, US 72.7% of users) but reach <50% of Indian internet users [U1][U10] ◐.

## 5. Counterpoint: the PC is not in terminal decline

Even on mobile's best metric, computers still hold ~43% of self-reported online *time* and reach 61.5% of online adults [U1] ◐. They retain the longer-form, transactional, and work tasks (nearly half of ecommerce; anchored by sustained remote work) [U1][U2] ◐. The honest framing is **"essential and stable for productivity," not "rebounding into growth"** — and whether mobile or desktop "dominates" depends entirely on the metric (reach vs. time vs. page views) and method.

## 6. Methodology: why the numbers don't add up

The single most important caveat: **self-reports and device logs agree only weakly.** Parry et al. (47 studies, ~50,000 people) found a correlation of **r=0.38**, with self-reports accurately reflecting logs in only ~5% of studies; the *direction* of error is inconsistent (R=1.21 but CI crosses 1:1) [U12] ✓. Heavier users under-report more, and accuracy improves with shorter recall windows [U13][U14] ✓. Passive measurement isn't ground truth either — single-device logging undercounts cross-device activity [U13].

The cited sources can't be reconciled into one number because they measure different things:

| Source | Headline | Method | Key caveat |
|---|---|---|---|
| GWI/DataReportal [U1][U15] | 6h38m/day (global) | Self-report survey, 16+ | Wave-to-wave non-comparable after question changes ◐ |
| Ofcom/Ipsos iris [U3][U4][U17] | 4h20–4h30m/day (UK) | Passive metered panel | UK-only, 18+ ✓ |
| BLS ATUS [U2] | ~34 min/day "computer for leisure" | 24-hour time diary | Counts only *primary* activities ? |
| eMarketer [U19]💀[U20]💀 | ~12h37m total media | **Modeled meta-analysis** | **Double-counts multitasking; not independent measurement** ◐ |
| Nielsen [U6][U18][U23][U25] | TV/streaming shares | Passive metered panel | Big Data + Panel currency (2025) ✓ |

**Takeaway for product/UX readers:** prefer figures that state sample, fieldwork date, geography, and measured-vs-reported; keep global GWI averages distinct from US/UK specifics; treat any single self-reported screen-time headline as an estimate with wide error [U12][U15].

---

# Part III — The capability–usage gap (synthesis)

The two reports were commissioned separately, but read together they describe a **supply** side (what OSes can do) and a **demand** side (what people do). The interesting findings are where they connect — and where they conspicuously don't.

## 1. Most of the frontier is invisible to end users

EEVDF, folios, CXL tiering, `sched_ext`, confidential VMs, seL4 — none of these change what a person *does* on a device. They are **infrastructure for the cloud and the developer**. The end-user experience is mediated through a phone and ~a dozen apps (Part II §1–2), where 63%+ of activity is browsing-adjacent and <6% of phone time is even in a browser [U1]. The OS frontier optimizes the **server room and the supply chain**, not the home screen.

## 2. Where supply and demand genuinely meet

- **Async I/O ↔ the apps people scroll.** Video at ~3h13m/day [U1] and streaming overtaking cable [U6] are served by backends whose throughput depends on exactly the kernel work in Part I §3 — `io_uring` zero-copy send/receive [O8][O9][O10], NVMe, DPDK/XDP [O16]. Users never see `io_uring`; they see TikTok loading instantly.
- **Confidential computing ↔ consumer AI.** The same generative-AI wave that reached 34% of U.S. adults [U7] runs on cloud GPUs increasingly wrapped in SEV-SNP/TDX trust domains [O21]. The ~2–16% (worst-case much higher) CVM overhead [O27][O28] is a tax paid invisibly on behalf of every ChatGPT query.
- **Mobile-first reality ↔ OS design.** Because daily life is phone-dominated [U1], the most consequential OS work is increasingly mobile: XNU's Exclaves and Secure Enclave [O29], Android's Rust Binder rewrite [O14], and the energy-aware scheduling that keeps batteries alive. The "smartphone-dependent" 16% of Americans [U9] make robust mobile isolation a equity issue, not just a performance one.
- **Generative AI is a brand-new workload for both halves.** It is simultaneously the top behavioral story (Part II §3) and a driver of the memory/accelerator frontier (CXL tiering, NPU stacks) the OS report flagged as under-covered. This is the one place where new user behavior is *pulling* new OS capability in real time.

## 3. The shared discipline: distrust single numbers

Both reports independently converged on the same lesson from opposite directions:

- **OS report:** load-bearing version claims (CFS removed in 6.12?, MGLRU in 6.1?, CXL framework versions?) are routinely asserted by secondary sources but unconfirmed by primaries ?.
- **Usage report:** the flagship "hours per day" numbers swing 50%+ depending on self-report vs. metering, with a meta-analytic correlation of just r=0.38 [U12].

The same skeptical posture — *state the source, date, population, and method; treat headlines as estimates* — applies to both the kernel changelog and the screen-time survey.

## 4. A note for systems builders (e.g., minimal/unikernel OSes)

For anyone building a **minimal-footprint OS** (the original context for the capability report): Part I validates the small-TCB thesis (Firecracker [O17], seL4 [O20], Unikraft [O19] — KPTI/SMAP unnecessary in a single-app unikernel), while Part II is a reminder that **the workloads worth optimizing are network- and I/O-bound services**, not interactive desktops. Async-I/O models (`io_uring`-style ring buffers [O8]) and event-driven, idle-friendly designs map directly onto the bursty, mostly-quiet traffic patterns that real usage data implies.

---

# Claims worth verifying (consolidated)

Carried forward from both reports' spot-check flags and link audits:

### From the OS report (0 dead links, but several unverified versions)
1. **"CFS fully removed; EEVDF sole fair scheduler in 6.12"** — flagged ?; the 6.6 *rework* is sourced [O1], the 6.12 *removal* is not. Verify against a kernel changelog.
2. **MGLRU "merged 6.1, default in distros"** — flagged ? with an empty source list. Strongly corroborated secondarily but no primary pulled.
3. **Confidential-VM "up to ~431% overhead"** — workload-specific (HLT/vCPU-sleep NPB); vendor benchmarks say 2–8%. Read [O27] for the exact conditions before quoting.

### From the usage report (4 dead links 💀)
4. **The 57% mobile / 43% computer split** — *the* shakiest claim: DataReportal's prose contradicts its own minutes, and the research workers split on how to read it. Verify directly in *Digital 2025* [U1].
5. **eMarketer ~12h37m total media / digital 63.7%** — **both backing URLs are dead ([U19]💀, [U20]💀)**, *and* it's modeled data that double-counts multitasking. Do not cite without a live primary; it's not an independent measurement.
6. **The headline 6h38m/day global figure** — single-source ◐, self-reported, and metered UK data (4h20m) suggests it runs high. Confirm the GWI wave/definition before quoting as fact.

**Link-audit status:** OS report — 30/30 URLs verified, no 💀. Usage report — 4 failed verification: `[U19]`💀 and `[U20]`💀 are **cited and load-bearing** (eMarketer); `[U11]` (AP-NORC) and `[U24]` (eMarketer chart) failed but are **uncited** in the body. No cost-cap warnings on either run ($14.56 and $11.03 respectively; the second ran under a $15 guardrail).

**Source-concentration risk:** the usage report's global figures lean heavily on a single source family — GWI/DataReportal ([U1][U5][U8][U10][U15][U21]) — triangulated against Ofcom, Nielsen, BLS, and Pew where possible. The OS report's scheduling section similarly rests on single sources (LWN [O1], kernel.org [O2]).

---

# Sources

## A — Modern operating systems' capabilities

[O1] An EEVDF CPU scheduler for Linux — LWN.net (2023-03-09) — https://lwn.net/Articles/925371
[O2] Deadline Task Scheduling — The Linux Kernel documentation — https://docs.kernel.org/scheduler/sched-deadline.html
[O3] CPU Analysis — Microsoft Learn (2021-03-25) — https://learn.microsoft.com/en-us/windows-hardware/test/wpt/cpu-analysis
[O4] zswap — Wikipedia — https://en.wikipedia.org/wiki/Zswap
[O5] On pages and folios — LWN.net (2026-04-24) — https://lwn.net/Articles/1064861
[O6] Linux Kernel Zoned Storage Support — Western Digital — https://zonedstorage.io/docs/linux/overview
[O7] Block cloning on ReFS — Microsoft Learn (2024-11-01) — https://learn.microsoft.com/en-us/windows-server/storage/refs/block-cloning
[O8] io_uring and networking in 2023 — axboe/liburing Wiki (2023-02-15) — https://github.com/axboe/liburing/wiki/io_uring-and-networking-in-2023
[O9] io_uring zero copy Rx — The Linux Kernel documentation — https://docs.kernel.org/networking/iou-zcrx.html
[O10] Zero-copy network transmission with io_uring — LWN.net (2021-12-30) — https://lwn.net/Articles/879724
[O11] TrueNAS Delivers the Industry's First Integration of OpenZFS 2.3 (2024-10-21) — https://www.truenas.com/blog/electric-eel-openzfs-23
[O12] The eBPF Foundation's 2025 Year in Review (2025-12-18) — https://ebpf.foundation/the-ebpf-foundations-2025-year-in-review
[O13] Quick Start — The Linux Kernel documentation (Rust) — https://docs.kernel.org/rust/quick-start.html
[O14] Rust for Linux — Wikipedia — https://en.wikipedia.org/wiki/Rust_for_Linux
[O15] Extensible Scheduler Class — The Linux Kernel documentation (v6.12, 2024-11-17) — https://www.kernel.org/doc/html/v6.12/scheduler/sched-ext.html
[O16] Overview — Data Plane Development Kit documentation (26.03) — https://doc.dpdk.org/guides/prog_guide/overview.html
[O17] Firecracker Design — https://github.com/firecracker-microvm/firecracker/blob/main/docs/design.md
[O18] Landlock: unprivileged access control — The Linux Kernel documentation (2026-03-01) — https://docs.kernel.org/userspace-api/landlock.html
[O19] Security — Unikraft — https://unikraft.org/docs/concepts/security
[O20] seL4 — Wikipedia — https://en.wikipedia.org/wiki/SeL4
[O21] Confidential computing platform-specific details — Red Hat Blog (2023-06-16) — https://www.redhat.com/en/blog/confidential-computing-platform-specific-details
[O22] Re: Our learnings from 42 Linux kernel exploits, we are limiting io_uring — oss-security (2023-07-14) — https://www.openwall.com/lists/oss-security/2023/07/14/2
[O23] Resigning as Asahi Linux project lead — Hector Martin (2025-02-14) — https://marcan.st/2025/02/resigning-as-asahi-linux-project-lead
[O24] Linux eBPF Security Advisory: Critical Visibility Concerns (2025-10-10) — https://linuxsecurity.com/features/ebpf-abuse-linux-kernel-visibility-gap
[O25] Managing Memory Tiers with CXL in Virtualized Environments — OSDI 2024 (2024-07-10) — https://www.microsoft.com/en-us/research/wp-content/uploads/2024/03/2024-FlatMemoryMode-Memstrata-OSDI2024.pdf
[O26] Exploring and Evaluating Real-world CXL — arXiv (2024-05-23) — https://arxiv.org/html/2405.14209v3
[O27] An Empirical Analysis of AMD SEV-SNP and Intel TDX — SIGMETRICS 2025 — https://dse.in.tum.de/wp-content/uploads/2024/11/sigmetrics25summer-CVM-Explained.pdf
[O28] A comprehensive performance evaluation of TEEs for confidential computing — UniTo — https://iris.unito.it/retrieve/c4c635a3-4b06-4b75-9bdb-c36abb43ae76/1-s2.0-S0167739X25003267-main.pdf
[O29] Apple's Darwin OS and XNU Kernel (2025-04-04) — https://tansanrao.com/blog/2025/04/xnu-kernel-and-darwin-evolution-and-architecture
[O30] The seL4 Microkit — Trustworthy Systems (2025-03-21) — https://trustworthy.systems/projects/microkit

## B — How people use their computers everyday

[U1] Digital 2025: Global Overview Report — DataReportal / GWI (2025-02-05) — https://datareportal.com/reports/digital-2025-global-overview-report
[U2] American Time Use Survey — 2024 Results — U.S. BLS (2025-06-26) — https://www.bls.gov/news.release/atus.nr0.htm
[U3] Time spent online by UK adults jumped nearly an hour in 2024 — TechCrunch (Ofcom Online Nation 2024, 2024-11-27) — https://techcrunch.com/2024/11/27/time-spent-online-by-adults-in-the-uk-jumped-by-nearly-an-hour-in-2024-says-ofcom
[U4] People spending even more time online now than during the pandemic — BBC News (Ofcom 2025, 2025-12-10) — https://www.bbc.com/news/articles/c39prelx2mxo
[U5] Digital 2026: Global Overview Report — DataReportal / GWI (2025-10-15) — https://datareportal.com/reports/digital-2026-global-overview-report
[U6] Streaming Reaches Historic TV Milestone — Nielsen (The Gauge, 2025-06-17) — https://www.nielsen.com/news-center/2025/streaming-reaches-historic-tv-milestone-eclipses-combined-broadcast-and-cable-viewing-for-first-time
[U7] 34% of U.S. adults have used ChatGPT, about double the share in 2023 — Pew (2025-06-25) — https://www.pewresearch.org/short-reads/2025/06/25/34-of-us-adults-have-used-chatgpt-about-double-the-share-in-2023
[U8] Digital 2025 July Global Statshot Report — DataReportal (2025-07-23) — https://datareportal.com/reports/digital-2025-july-global-statshot
[U9] Mobile Fact Sheet — Pew Research Center (2025-11-20) — https://www.pewresearch.org/internet/fact-sheet/mobile
[U10] Digital 2024: Global Overview Report — DataReportal / GWI (2024-01-31) — https://datareportal.com/reports/digital-2024-global-overview-report
[U11] Young adults are leading the way in AI adoption — AP-NORC (2025-07-29) — https://apnorc.org/projects/young-adults-leading-the-way-in-ai-adoption  💀 *(failed verification; uncited)*
[U12] Discrepancies between logged and self-reported digital media use (Parry et al.) — Nature Human Behaviour (2021-05-17) — https://www.nature.com/articles/s41562-021-01117-5
[U13] Measurement discrepancies in adolescent screen media activity, ABCD study (Zhao et al.) — Nature (2025-05-10) — https://www.nature.com/articles/s44184-025-00131-z
[U14] Discrepancies Between Self-reported and Objectively Measured Smartphone Screen Time (Júdice et al.) — PMC (2023-01-24) — https://pmc.ncbi.nlm.nih.gov/articles/PMC9872730
[U15] Notes on Data — DataReportal — https://datareportal.com/notes-on-data
[U16] Warnings on the dangers of screen time are ill founded — University of Bath (2021-05-17) — https://www.bath.ac.uk/announcements/warnings-on-the-dangers-of-screen-time-are-ill-founded-new-study
[U17] Ofcom Finds UK Adults Now Spend Over 4.5 Hours Online Each Day — ISPreview (2025-12-10) — https://www.ispreview.co.uk/index.php/2025/12/ofcom-finds-uk-adults-now-spend-over-4-5-hours-online-each-day.html
[U18] Nielsen begins updated era of TV ratings with Big Data + Panel (2025-09-02) — https://www.nielsen.com/news-center/2025/nielsen-begins-updated-era-of-tv-ratings-with-big-data-panel-for-this-falls-tv-season
[U19] Digital media makes up nearly two-thirds of consumers' total time — EMARKETER (2024-08-13) — https://www.emarketer.com/content/digital-media-makes-up-nearly-two-thirds-of-consumers-total-time-spent-with-media  💀 *(failed verification; cited)*
[U20] US Time Spent With Media 2025 — EMARKETER (2025-02-27) — https://www.emarketer.com/content/us-time-spent-with-media-2025  💀 *(failed verification; cited)*
[U21] Digital 2025 Global Overview Report PDF — We Are Social / Meltwater / GWI (2025-02) — https://wearesocial.com/wp-content/uploads/2025/02/GDR-2025-v2.pdf
[U22] Digital vs. Traditional Media Consumption — GWI — https://www.gwi.com/hubfs/Digital_vs_Traditional_Media_Consumption.pdf
[U23] The Gauge — Nielsen (methodology/FAQ) — https://www.nielsen.com/data-center/the-gauge
[U24] Most Major Media-Consuming Activities Are Digital — EMARKETER chart (2025-05-01) — https://www.emarketer.com/chart/271889  💀 *(failed verification; uncited)*
[U25] Nielsen Media Research — Wikipedia — https://en.wikipedia.org/wiki/Nielsen_Media_Research

---

*Compiled from two pi-deep-research runs (2026-06-05). Capability data current to ~2023–2026 kernel releases; usage data current to GWI Q3 2024–Q1 2025, Ofcom 2024–2025, Pew/BLS 2024–2025. Verify load-bearing claims — especially the six flagged above — before relying on them.*
