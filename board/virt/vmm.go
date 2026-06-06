// honk - QEMU virt board: the H-extension hypervisor (M11), the only place
// honk uses RISC-V paging. honk boots in HS-mode with the H-extension enabled
// (RV64.md §9; the QEMU cpu is rv64,h=true), so it can host a guest in VS-mode:
// RunGuest sets up G-stage (guest-physical -> supervisor-physical) translation
// via hgatp, world-switches into the guest, and trap-and-emulates the guest's
// SBI calls.
//
// M11 proves the mechanism in isolation against a tiny VS-mode payload honk
// fully controls (kernel/vmm.DemoGuest): it enables the H-extension, two-stage
// paging (hgatp/Sv39x4), and trap-and-emulate. The encodable logic - the guest
// program and the G-stage tables - is the host-tested kernel/vmm package; the
// world switch and CSR programming are bare metal here and in vmm_riscv64.s.
//
// Scope (honest): one guest, one vCPU (a goroutine), a single 2 MiB G-stage
// megapage, an emulated SBI console + shutdown, run with HS interrupts masked
// (no preemption needed - the payload prints and halts). Real guests (M12/M13)
// add a timer-driven exit, vsatp paging, PLIC/virtio backends, and SMP vCPUs.

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

// RunGuest loads code into a fresh guest, runs it in VS-mode under H-extension
// two-stage translation, and emulates its SBI calls until it halts (or faults),
// returning a human-readable reason. Guest console output appears inline on
// honk's console. It must run pinned to one hart (CSRs and sscratch are
// per-hart), so it locks the OS thread for the duration.
func RunGuest(code []byte) string {
	if len(code) > vmm.MegapageSize {
		return fmt.Sprintf("guest too large (%d bytes > %d)", len(code), vmm.MegapageSize)
	}

	// Pin to this hart: every CSR below (hgatp, stvec, sscratch, ...) is
	// hart-local, and the world switch parks state in this hart's sscratch.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Build the G-stage map: one 2 MiB megapage backing the guest's RAM. honk
	// is identity-mapped, so each DMA buffer's address is its physical address.
	root := dmaAlloc(vmm.RootSize, vmm.RootSize) // 16 KiB, 16 KiB-aligned
	l1 := dmaAlloc(vmm.L1Size, vmm.L1Size)       // 4 KiB
	ram := dmaAlloc(vmm.MegapageSize, vmm.MegapageSize)
	copy(ram, code)
	vmm.WriteGStage(root, l1, ptr(root), ptr(l1), ptr(ram), guestBase)
	fence()

	// Snapshot the HS CSRs the run repurposes, so normal HS operation (the
	// console trap path, the scheduler) is undisturbed afterwards. hstatus must
	// be restored to clear SPV - otherwise honk's own trap-return sret would
	// re-enter V=1.
	savedStvec := readStvec()
	savedScratch := readSscratch()
	savedSstatus := readSstatus()
	savedHstatus := readHstatus()
	defer func() {
		writeStvec(savedStvec)
		writeSscratch(savedScratch)
		writeSstatus(savedSstatus)
		writeHstatus(savedHstatus)
	}()

	writeVsatp(0)                  // VS-stage Bare: guest virtual == guest physical
	writeHgatp(vmm.Hgatp(ptr(root)))
	hfenceGVMA() // order the new G-stage map before any guest translation
	writeStvec(uint64(guestVecPC()))

	v := vcpu{pc: guestBase}

	// Trap-and-emulate loop. A correct payload terminates in a handful of
	// iterations; the cap turns a runaway/buggy guest into a clean error
	// instead of a hang.
	const maxTraps = 1 << 20
	for i := 0; i < maxTraps; i++ {
		// Enter the guest: SPP=1 (-> VS), SPV=1 (-> V=1), SPIE=0 so HS
		// interrupts stay masked while the guest runs (no preemption in M11).
		writeHstatus(readHstatus() | hstatusSPV)
		writeSstatus((readSstatus() | sstatusSPP) &^ sstatusSPIE)
		writeSepc(v.pc)

		guestEnter(&v)

		cause := readScause()
		if cause == vmm.EcallFromVS {
			switch v.gpr[17] { // a7 = SBI extension id
			case vmm.SBIConsolePutchar:
				sbiPutchar(byte(v.gpr[10])) // a0 = character
				v.pc += 4                   // step past the ecall (always 4 bytes)
				continue
			case vmm.SBIShutdown:
				return "guest halted (SBI shutdown)"
			default:
				v.pc += 4 // unimplemented SBI call: return error to guest (a0=0) by skipping
				continue
			}
		}
		return fmt.Sprintf("guest fault: scause=%#x sepc=%#x stval=%#x htval=%#x",
			cause, v.pc, readStval(), readHtval())
	}
	return "guest did not halt"
}
