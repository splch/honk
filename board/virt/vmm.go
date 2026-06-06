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
// run, preemptible via the timer); a sized G-stage map (a run of 2 MiB
// megapages); the guest may enable its own VS-stage Sv39 paging (vsatp); SBI
// Base/TIME/console/shutdown. A real third-party guest image, virtio device
// backends, and time-sharing one hart between a vCPU and other honk goroutines
// land in M13.

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

// sstatus / hstatus bit positions honk toggles around the world switch.
const (
	sstatusSIE  = 1 << 1 // S-mode interrupt enable
	sstatusSPIE = 1 << 5 // prior SIE (restored into SIE by sret)
	sstatusSPP  = 1 << 8 // prior privilege: 1=S, selecting VS-mode on sret
	hstatusSPV  = 1 << 7 // prior virtualization mode: 1 selects V=1 on sret
)

// Interrupt-environment bits the VMM programs for the timer (M12).
const (
	sieSTIE     = 1 << 5  // sie: HS supervisor-timer interrupt enable
	sieSEIE     = 1 << 9  // sie: HS supervisor-external (UART) interrupt enable
	hidelegVSTI = 1 << 6  // hideleg: delegate the VS-timer interrupt to VS-mode
	hidelegVSEI = 1 << 10 // hideleg: delegate the VS-external interrupt to VS-mode
	hvipVSTIP   = 1 << 6  // hvip: inject a pending VS-timer interrupt
	hvipVSEIP   = 1 << 10 // hvip: inject a pending VS-external interrupt (a device IRQ)
	hcounterTM  = 1 << 1  // hcounteren: let VS-mode read the time CSR

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

// vmRun is one guest run: the vCPU, the guest's RAM (so SBI emulation can follow
// guest pointers, e.g. the DBCN console buffer), and the guest's requested timer
// deadline (in time-CSR ticks; 0 = none pending). It owns the SBI emulation and
// the HS-timer arming policy so the run loop stays a clean dispatch.
type vmRun struct {
	v        vcpu
	mem      []byte // the guest's RAM (gpa GuestBase -> mem[0]); see vmm.GuestRange
	deadline uint64

	// Emulated-device interrupt state. armed is set by the guest's doorbell
	// store (MMIORegArm); while armed honk raises an external interrupt each
	// quantum, pending until the guest acks it by reading MMIORegIRQ.
	irqArmed   bool
	irqPending bool
}

// dbcnMaxWrite caps one DBCN console_write so a guest cannot make honk spin
// un-preemptibly inside the nosplit guest-run region on a huge buffer; a guest
// asking for more gets a spec-compliant partial write and calls again.
const dbcnMaxWrite = 4096

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
	case vmm.SBIExtDBCN:
		v.gpr[10], v.gpr[11] = r.sbiDBCN()
	default:
		v.gpr[10], v.gpr[11] = sbiErr(vmm.SBIErrNotSupported), 0
	}
	v.pc += 4 // step past the ecall (always 4 bytes; there is no c.ecall)
	return false, ""
}

// sbiDBCN emulates the debug-console extension. console_write follows the
// guest's (guest-physical) buffer pointer through the G-stage - via
// vmm.GuestRange, which refuses a pointer outside the guest's RAM - and prints
// the bytes the guest placed there; console_write_byte prints one byte. This
// is honk's first read of guest memory at a guest-supplied address, the
// mechanism every virtio device backend builds on.
//
//go:nosplit
func (r *vmRun) sbiDBCN() (err, val uint64) {
	switch r.v.gpr[16] { // a6 = function id
	case vmm.SBIDBCNWrite:
		n := r.v.gpr[10] // a0 = num_bytes
		if n > dbcnMaxWrite {
			n = dbcnMaxWrite // bounded partial write (keeps this region preemptible)
		}
		gpa := r.v.gpr[12]<<32 | r.v.gpr[11] // a2:a1 = base_hi:base_lo
		start, end, ok := vmm.GuestRange(gpa, n, vmm.GuestBase, uint64(len(r.mem)))
		if !ok {
			return sbiErr(vmm.SBIErrInvalidParam), 0
		}
		for i := start; i < end; i++ {
			sbiPutchar(r.mem[i])
		}
		return vmm.SBISuccess, n // bytes written
	case vmm.SBIDBCNWriteByte:
		sbiPutchar(byte(r.v.gpr[10])) // a0 = byte
		return vmm.SBISuccess, 0
	default:
		return sbiErr(vmm.SBIErrNotSupported), 0
	}
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
	case vmm.SBIExtTime, vmm.SBIExtDBCN, vmm.SBIConsolePutchar, vmm.SBIShutdown:
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

// emulateMMIO handles a guest load/store that missed the G-stage map by
// emulating an honk device register: it reads the faulting instruction from
// guest RAM at the guest pc (the demo guest runs paging-off, so its pc is a
// guest-physical address; a VS-paged guest would need a page-table walk here),
// decodes it (vmm.DecodeMMIO), takes the faulting address from htval, and reads
// or writes the addressed register. It returns false for an access it does not
// recognize, so the run loop reports a real fault. It is nosplit (it runs in
// the non-descheduling guest-run region) and calls only nosplit/assembly code.
//
//go:nosplit
func (r *vmRun) emulateMMIO() bool {
	start, _, ok := vmm.GuestRange(r.v.pc, 4, vmm.GuestBase, uint64(len(r.mem)))
	if !ok {
		return false // pc not in guest RAM: not an MMIO access we can decode
	}
	insn := uint32(r.mem[start]) | uint32(r.mem[start+1])<<8 |
		uint32(r.mem[start+2])<<16 | uint32(r.mem[start+3])<<24
	acc, ok := vmm.DecodeMMIO(insn)
	if !ok {
		return false
	}
	gpa := readHtval() << 2 // htval holds the faulting guest-physical address >> 2
	if acc.Store {
		val := uint64(0) // x0 reads as zero (a guest may `sw x0, reg`)
		if acc.Reg != 0 {
			val = r.v.gpr[acc.Reg]
		}
		if !r.mmioStore(gpa, val) {
			return false
		}
	} else {
		val, ok := r.mmioLoad(gpa)
		if !ok {
			return false
		}
		if acc.Reg != 0 { // x0 stays hardwired zero
			r.v.gpr[acc.Reg] = vmm.SignExtend(val, acc.Width, acc.Signed)
		}
	}
	r.v.pc += 4 // the demo guest uses only 32-bit load/store
	return true
}

// mmioLoad / mmioStore are the demo device honk emulates behind the MMIO
// trap-and-emulate path: a magic register, a console-out register, and an
// interrupt doorbell + status register (numbers in kernel/vmm). A real
// virtio/PLIC backend replaces these. They are methods so they can touch the
// run's device-interrupt state and withdraw an injected IRQ.
//
//go:nosplit
func (r *vmRun) mmioLoad(gpa uint64) (uint64, bool) {
	switch gpa {
	case vmm.MMIORegMagic:
		return vmm.MMIOMagic, true
	case vmm.MMIORegIRQ:
		if r.irqPending { // reading the status acks the IRQ: the device de-asserts
			r.irqPending = false
			writeHvip(readHvip() &^ hvipVSEIP)
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

//go:nosplit
func (r *vmRun) mmioStore(gpa, val uint64) bool {
	switch gpa {
	case vmm.MMIORegOut:
		sbiPutchar(byte(val))
		return true
	case vmm.MMIORegArm:
		r.irqArmed = true // doorbell: honk now raises the device IRQ each quantum
		return true
	}
	return false
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

// RunGuest runs an M11/M12 payload: one 2 MiB megapage of RAM, no VS-stage
// paging. It runs the guest in VS-mode under H-extension two-stage translation,
// delivers its timer, and emulates its SBI calls until it halts (or faults, or
// exceeds its time budget). Guest console output appears inline on honk's
// console.
func RunGuest(code []byte) string {
	return runGuestImage(code, vmm.MegapageSize, nil)
}

// RunPagingGuest runs a guest that enables its own VS-stage Sv39 paging. It gets
// two megapages of RAM (so honk's G-stage maps more than one megapage) and an
// identity VS-stage page table honk seeds into that RAM for the guest to load
// into vsatp - proving two-stage (VS + G) translation end to end.
func RunPagingGuest(code []byte) string {
	const ramSize = 2 * vmm.MegapageSize
	return runGuestImage(code, ramSize, func(ram []byte) {
		vmm.WriteVSStage(
			ram[vmm.VSRootOff:vmm.VSRootOff+vmm.PageTableSize],
			ram[vmm.VSL1Off:vmm.VSL1Off+vmm.PageTableSize],
			vmm.GuestBase+vmm.VSL1Off, vmm.GuestBase, ramSize)
	})
}

// runGuestImage allocates ramSize bytes of guest RAM (2 MiB-aligned so its
// megapages are leaf-aligned), copies code to its base, lets seed (if any)
// initialize the rest of guest RAM (e.g. seed VS-stage page tables), builds the
// G-stage map over the whole RAM, and runs the guest.
//
// The G-stage tables and RAM are allocated here (which may GC, hence before the
// run) and are reached during the run only through physical addresses it cannot
// see, so the trailing KeepAlive pins their Go backing against the GC. honk is
// identity-mapped, so each buffer's address is its physical address.
func runGuestImage(code []byte, ramSize int, seed func(ram []byte)) string {
	if len(code) > ramSize {
		return fmt.Sprintf("guest too large (%d bytes > %d)", len(code), ramSize)
	}
	root := dmaAlloc(vmm.RootSize, vmm.RootSize) // 16 KiB, 16 KiB-aligned
	l1 := dmaAlloc(vmm.L1Size, vmm.L1Size)       // 4 KiB
	ram := dmaAlloc(ramSize, vmm.MegapageSize)
	copy(ram, code)
	if seed != nil {
		seed(ram)
	}
	vmm.WriteGStage(root, l1, ptr(l1), ptr(ram), vmm.GuestBase, uint64(ramSize))
	fence()

	var r vmRun
	r.mem = ram // so SBI emulation can follow guest pointers into guest RAM
	r.v.pc = vmm.GuestBase
	reason := runGuest(&r, vmm.Hgatp(ptr(root)))

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
	writeHideleg(savedHideleg | hidelegVSTI | hidelegVSEI) // VS timer + device IRQ are the guest's
	writeHcounteren(savedHcnt | hcounterTM)                // let the guest read `time`
	writeHvip(savedHvip &^ hvipVSTIP)                      // nothing injected yet
	writeSie((savedSie &^ sieSEIE) | sieSTIE)              // honk takes the HS timer, not UART

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
			if r.irqArmed && !r.irqPending { // raise the emulated device's IRQ
				r.irqPending = true
				writeHvip(readHvip() | hvipVSEIP)
			}
			r.armHSTimer()
			continue
		}
		if cause == vmm.LoadGuestPageFault || cause == vmm.StoreGuestPageFault {
			if r.emulateMMIO() { // a guest access to an emulated device register
				continue
			}
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
