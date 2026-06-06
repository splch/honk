// honk - QEMU virt board: the H-extension hypervisor, the only place honk uses
// RISC-V paging. honk boots in HS-mode with the H-extension enabled (RV64.md
// §9; the QEMU cpu is rv64,h=true), so it can host a guest in VS-mode: RunGuest
// sets up G-stage (guest-physical -> supervisor-physical) translation via
// hgatp, world-switches into the guest, and trap-and-emulates the guest's SBI
// calls and timer.
//
// M11 proved the bare mechanism (enable H-ext, two-stage paging, trap-and-
// emulate a console+shutdown) against a straight-line payload. M12 adds the
// pieces a real guest needs and proves them against a payload honk controls:
//
//   - A timer. honk emulates SBI TIME set_timer and delivers the guest's timer
//     by injecting a VS-timer interrupt (hvip.VSTIP) when the deadline passes.
//   - Preemption. honk runs with its own HS timer enabled, so it regains the
//     hart from a running guest every quantum (and on a wall-clock budget) -
//     the safety property that keeps a runaway guest from hanging the machine.
//   - A broader SBI surface (Base probe + TIME, alongside M11's legacy console
//     and shutdown).
//
// The encodable logic - the guest programs and the G-stage tables - is the
// host-tested kernel/vmm package; the world switch, CSR programming, and SBI
// effects are bare metal here and in vmm_riscv64.s.
//
// Scope (honest): one guest, one vCPU (a goroutine pinned to its hart for the
// run, preemptible via the timer); a single 2 MiB G-stage megapage; SBI Base/
// TIME/console/shutdown. A real third-party guest image, vsatp paging, virtio
// device backends, and time-sharing one hart between a vCPU and other honk
// goroutines land in M13.

//go:build tamago && riscv64

package virt

import (
	"fmt"
	"runtime"

	"honk/kernel/vmm"
)

// vcpu is the guest register file plus saved honk (HS) context. Its field
// offsets are hard-coded in vmm_riscv64.s (guestEnter/guestVec) - keep them in
// sync: gpr at 0, pc at 256, then the five HS specials at 264..296.
type vcpu struct {
	gpr  [32]uint64 // guest x0..x31 (x0 unused)
	pc   uint64     // guest sepc: entry value in, faulting/ecall pc out
	hsRA uint64     // saved honk RA/SP/GP/TP/g for the round trip
	hsSP uint64
	hsGP uint64
	hsTP uint64
	hsG  uint64
}

// guest layout: the guest sees its RAM at this base (like a real RISC-V machine
// at 0x80000000); honk backs it with a 2 MiB-aligned host buffer via the
// single G-stage megapage.
const guestBase = 0x8000_0000

// sstatus / hstatus bit positions honk toggles around the world switch.
const (
	sstatusSIE  = 1 << 1 // S-mode interrupt enable
	sstatusSPIE = 1 << 5 // prior SIE (restored into SIE by sret)
	sstatusSPP  = 1 << 8 // prior privilege: 1=S, selecting VS-mode on sret
	hstatusSPV  = 1 << 7 // prior virtualization mode: 1 selects V=1 on sret
)

// Interrupt-environment bits the VMM programs for the timer (M12).
const (
	sieSTIE     = 1 << 5 // sie: HS supervisor-timer interrupt enable
	sieSEIE     = 1 << 9 // sie: HS supervisor-external (UART) interrupt enable
	hidelegVSTI = 1 << 6 // hideleg: delegate the VS-timer interrupt to VS-mode
	hvipVSTIP   = 1 << 6 // hvip: inject a pending VS-timer interrupt
	hcounterTM  = 1 << 1 // hcounteren: let VS-mode read the time CSR

	// scause values for the HS interrupts the run loop sees (bit 63 = interrupt).
	sTimerInterrupt = uint64(1)<<63 | 5

	// vmQuantumTicks bounds how long the guest runs before honk regains the
	// hart; vmBudgetTicks caps a whole run (preemption of a runaway guest).
	// The QEMU virt time CSR is 10 MHz, so a tick is 100 ns. The quantum is
	// kept under the runtime's ~10 ms async-preempt window so the vCPU's
	// per-quantum Gosched (below) keeps sysmon from retaking the hart - honk has
	// no async preemption and a fixed M per hart, so a long-running, non-yielding
	// goroutine would make the runtime try to start an M beyond the parked harts.
	vmQuantumTicks = 80_000     // ~8 ms
	vmBudgetTicks  = 50_000_000 // ~5 s

	// disarmTimer is an SBI set_timer value that never arrives, used to stop
	// honk's HS timer at teardown so a stray STIP cannot storm the trap path.
	disarmTimer = ^uint64(0)
)

// world switch + CSR accessors (vmm_riscv64.s).
func guestEnter(v *vcpu)
func guestVecPC() uintptr
func hfenceGVMA()
func writeHgatp(v uint64)
func writeVsatp(v uint64)
func writeHstatus(v uint64)
func readHstatus() uint64
func writeSstatus(v uint64)
func readSstatus() uint64
func writeSepc(v uint64)
func writeStvec(v uint64)
func readStvec() uint64
func writeSscratch(v uint64)
func readSscratch() uint64
func readHtval() uint64
func writeHvip(v uint64)
func readHvip() uint64
func writeHideleg(v uint64)
func readHideleg() uint64
func writeHcounteren(v uint64)
func readHcounteren() uint64
func writeSie(v uint64)
func readSie() uint64

// setTimerSBI arms (or, with disarmTimer, stops) honk's own HS timer through
// the firmware's SBI TIME extension - honk is HS and cannot write mtimecmp.
//
//go:nosplit
func setTimerSBI(t uint64) { sbiCall(vmm.SBIExtTime, vmm.SBITimeSetTimer, t, 0, 0) }

// sbiErr converts a (negative) SBI error code to its two's-complement a0 value.
//
//go:nosplit
func sbiErr(code int) uint64 { return uint64(int64(code)) }

// vmRun is one guest run: the vCPU plus the guest's requested timer deadline
// (in time-CSR ticks; 0 = none pending). It owns the SBI emulation and the
// HS-timer arming policy so the run loop stays a clean dispatch.
type vmRun struct {
	v        vcpu
	deadline uint64
}

// armHSTimer programs honk's HS timer for the sooner of the next preemption
// quantum and the guest's pending deadline, so honk both delivers the guest's
// timer on time and never cedes the hart for longer than a quantum.
//
//go:nosplit
func (r *vmRun) armHSTimer() {
	next := readTime() + vmQuantumTicks
	if r.deadline != 0 && r.deadline < next {
		next = r.deadline
	}
	setTimerSBI(next)
}

// sbi emulates the guest's SBI call (ecall-from-VS). It reads the call in
// a7/a6/a0, writes the result into a0(error)/a1(value), advances past the ecall,
// and reports halt only when the guest asks to stop. honk implements the host
// functions once; what a guest may do is still just these calls. It is nosplit
// because it runs inside the non-descheduling guest-run region (see runGuest).
//
//go:nosplit
func (r *vmRun) sbi() (halt bool, reason string) {
	v := &r.v
	switch v.gpr[17] { // a7 = extension id
	case vmm.SBIConsolePutchar:
		sbiPutchar(byte(v.gpr[10])) // a0 = character
	case vmm.SBIShutdown:
		return true, "guest halted (SBI shutdown)"
	case vmm.SBIExtBase:
		v.gpr[10], v.gpr[11] = r.sbiBase()
	case vmm.SBIExtTime:
		v.gpr[10], v.gpr[11] = r.sbiSetTimer()
	default:
		v.gpr[10], v.gpr[11] = sbiErr(vmm.SBIErrNotSupported), 0
	}
	v.pc += 4 // step past the ecall (always 4 bytes; there is no c.ecall)
	return false, ""
}

// sbiBase answers the Base extension: only probe_extension, reporting the calls
// honk actually emulates as present.
//
//go:nosplit
func (r *vmRun) sbiBase() (err, val uint64) {
	if r.v.gpr[16] != vmm.SBIBaseProbeExtension { // a6 = function id
		return sbiErr(vmm.SBIErrNotSupported), 0
	}
	switch r.v.gpr[10] { // a0 = the extension id being probed
	case vmm.SBIExtTime, vmm.SBIConsolePutchar, vmm.SBIShutdown:
		return vmm.SBISuccess, 1 // present
	default:
		return vmm.SBISuccess, 0 // absent
	}
}

// sbiSetTimer records the guest's requested deadline, withdraws any timer
// interrupt it is now acknowledging, and re-arms honk's HS timer.
//
//go:nosplit
func (r *vmRun) sbiSetTimer() (err, val uint64) {
	if r.v.gpr[16] != vmm.SBITimeSetTimer {
		return sbiErr(vmm.SBIErrNotSupported), 0
	}
	r.deadline = r.v.gpr[10]           // a0 = absolute time the guest wants
	writeHvip(readHvip() &^ hvipVSTIP) // the guest acked the prior tick
	r.armHSTimer()
	return vmm.SBISuccess, 0
}

// reportGuestFault prints an unexpected guest trap's CSRs straight to the SBI
// console. It is nosplit (no allocation, no fmt) so it is safe inside the
// guest-run region; the run then returns a constant reason.
//
//go:nosplit
func reportGuestFault(cause, pc uint64) {
	putStr("\nvm: guest fault scause=0x")
	putHex(cause)
	putStr(" sepc=0x")
	putHex(pc)
	putStr(" stval=0x")
	putHex(readStval())
	putStr(" htval=0x")
	putHex(readHtval())
	putStr("\n")
}

// RunGuest loads code into a fresh guest, runs it in VS-mode under H-extension
// two-stage translation, delivers its timer, and emulates its SBI calls until
// it halts (or faults, or exceeds its time budget), returning a human-readable
// reason. Guest console output appears inline on honk's console.
//
// The G-stage tables and guest RAM are allocated here (which may GC, hence
// before the run); their backing must outlive the run, which the trailing
// KeepAlive guarantees against the GC, since the run reaches them only through
// physical addresses it cannot see. The run itself is runGuest.
func RunGuest(code []byte) string {
	if len(code) > vmm.MegapageSize {
		return fmt.Sprintf("guest too large (%d bytes > %d)", len(code), vmm.MegapageSize)
	}

	// One 2 MiB megapage backs the guest's RAM; honk is identity-mapped, so each
	// buffer's address is its physical address.
	root := dmaAlloc(vmm.RootSize, vmm.RootSize) // 16 KiB, 16 KiB-aligned
	l1 := dmaAlloc(vmm.L1Size, vmm.L1Size)       // 4 KiB
	ram := dmaAlloc(vmm.MegapageSize, vmm.MegapageSize)
	copy(ram, code)
	vmm.WriteGStage(root, l1, ptr(root), ptr(l1), ptr(ram), guestBase)
	fence()

	var r vmRun
	r.v.pc = guestBase
	reason := runGuest(&r, vmm.Hgatp(ptr(root)))

	// The guest reached root/l1/ram only via physical addresses (in hgatp), so
	// keep their Go backing alive until the run is done.
	runtime.KeepAlive(root)
	runtime.KeepAlive(l1)
	runtime.KeepAlive(ram)
	return reason
}

// runGuest is the world-switch + trap-and-emulate loop, run as one nosplit,
// allocation-free, non-yielding region. That is load-bearing, not incidental:
// tamago has no async preemption, so a region with no morestack/alloc/yield is
// never descheduled - the goroutine cannot migrate off this hart (keeping the
// hart-local CSRs below valid) and never trips honk's fixed-M-per-hart SMP
// model (a deschedule of a pinned vCPU would make the runtime try to start an M
// beyond the parked harts). Every function it calls is nosplit or assembly.
//
// hgatp selects the caller's G-stage root. The HS timer both delivers the
// guest's SBI timer (by injecting hvip.VSTIP when the deadline passes) and
// bounds the run, so even a guest that never yields is preempted - the safety
// property that keeps it from hanging the machine.
//
//go:nosplit
func runGuest(r *vmRun, hgatp uint64) string {
	// Snapshot every HS CSR the run repurposes, to restore at the end so honk's
	// normal operation (its trap path, console interrupts, scheduler) resumes
	// undisturbed.
	savedStvec := readStvec()
	savedScratch := readSscratch()
	savedSstatus := readSstatus()
	savedHstatus := readHstatus()
	savedHideleg := readHideleg()
	savedHvip := readHvip()
	savedHcnt := readHcounteren()
	savedSie := readSie()

	// Mask honk's own HS interrupts for the run. Once stvec points at the guest
	// trap vector, an HS interrupt taken while V=0 (in this region) would wrongly
	// vector into guestVec and corrupt honk. At the lower VS privilege HS
	// interrupts ignore sstatus.SIE, so the HS timer still preempts the guest;
	// this only fences honk's own code.
	writeSstatus(savedSstatus &^ sstatusSIE)

	// Program two-stage translation and the guest's interrupt environment.
	writeVsatp(0) // VS-stage Bare: guest virtual == guest physical
	writeHgatp(hgatp)
	hfenceGVMA() // order the new G-stage map before any guest translation
	writeStvec(uint64(guestVecPC()))
	writeHideleg(savedHideleg | hidelegVSTI)  // the VS timer is the guest's own
	writeHcounteren(savedHcnt | hcounterTM)   // let the guest read `time`
	writeHvip(savedHvip &^ hvipVSTIP)         // nothing injected yet
	writeSie((savedSie &^ sieSEIE) | sieSTIE) // honk takes the HS timer, not UART

	start := readTime()
	r.armHSTimer()

	reason := "guest did not halt"
	const maxTraps = 1 << 20
	for i := 0; i < maxTraps; i++ {
		// Enter the guest: SPP=1 (-> VS), SPV=1 (-> V=1). HS interrupts are
		// always enabled at the lower (VS) privilege, so the HS timer fires
		// during the guest; the loop body runs with SIE=0 (set by the trap), so
		// no HS interrupt hits the guest trap vector.
		writeHstatus(readHstatus() | hstatusSPV)
		writeSstatus((readSstatus() | sstatusSPP) &^ sstatusSPIE)
		writeSepc(r.v.pc)

		guestEnter(&r.v)

		cause := readScause()
		if cause == vmm.EcallFromVS {
			if halt, why := r.sbi(); halt {
				reason = why
				break
			}
			continue
		}
		if cause == sTimerInterrupt {
			if readTime()-start > vmBudgetTicks {
				reason = "guest preempted (time budget exceeded)"
				break
			}
			if r.deadline != 0 && readTime() >= r.deadline {
				writeHvip(readHvip() | hvipVSTIP) // deliver the guest's timer
				r.deadline = 0
			}
			r.armHSTimer()
			continue
		}
		reportGuestFault(cause, r.v.pc)
		reason = "guest fault (see console)"
		break
	}

	// Restore HS state. The HS timer is disarmed first: a stray STIP afterward
	// would storm honk's trap path, which does not service timer interrupts.
	setTimerSBI(disarmTimer)
	writeSie(savedSie)
	writeHcounteren(savedHcnt)
	writeHvip(savedHvip)
	writeHideleg(savedHideleg)
	writeStvec(savedStvec)
	writeSscratch(savedScratch)
	writeSstatus(savedSstatus)
	writeHstatus(savedHstatus)
	return reason
}
