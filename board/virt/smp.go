// honk - QEMU virt board: symmetric multiprocessing (SMP).
//
// honk brings every hart up as a real Go runtime M, so GOMAXPROCS = nharts and
// the Go scheduler spreads goroutines across cores. The mechanism mirrors
// tamago/amd64 and needs NO runtime fork: secondary harts are started via SBI
// HSM into a park loop (secondaryEntry, boot_riscv64.s), and the runtime drives
// M creation through the goos.Task hook, which hands a parked hart its
// runtime-allocated stack + g0 and releases it into runtime.mstart.

//go:build tamago && riscv64

package virt

import (
	"runtime"
	"runtime/goos"
	"sync/atomic"
	"unsafe"
)

// maxHarts bounds the per-hart hand-off tables. QEMU virt supports far more,
// but this is ample for development; raise it (and re-test) as needed.
const maxHarts = 8

// SBI HSM (Hart State Management) function IDs.
const (
	sbiHSMHartStart     = 0
	sbiHSMHartGetStatus = 2
)

// Per-hart hand-off slots, shared with the secondaryEntry park loop. honkTask
// writes taskSP/taskGP then publishes taskPC (release); the park loop reads
// taskPC (acquire via FENCE) then adopts the stack and g. Indexed by hartid.
var (
	taskSP    [maxHarts]uint64
	taskGP    [maxHarts]uint64
	taskPC    [maxHarts]uint64
	readyFlag [maxHarts]uint32
)

var (
	parkedHarts []uint64 // secondary harts parked and ready for a task
	taskIdx     int32    // next parkedHarts index handed out by honkTask
	numHarts    int      // total harts the runtime is using (set by InitSMP)
)

// NumHarts returns the number of harts the runtime is using (1 if SMP is off).
func NumHarts() int {
	if numHarts == 0 {
		return 1
	}
	return numHarts
}

// implemented in boot_riscv64.s
func readtp() uint64
func secondaryEntryPC() uintptr

// CurrentHart returns the hart id the calling goroutine is currently running
// on (read from tp, which honk sets at every hart's entry).
func CurrentHart() uint64 { return readtp() }

func hartGetStatus(hart uint64) (state int64, exists bool) {
	err, val := sbiCall(sbiExtHSM, sbiHSMHartGetStatus, hart, 0, 0)
	return val, err == 0
}

func hartStart(hart, addr, opaque uint64) (err int64) {
	err, _ = sbiCall(sbiExtHSM, sbiHSMHartStart, hart, addr, opaque)
	return
}

// InitSMP starts every secondary hart as a Go runtime M and raises GOMAXPROCS
// to the total hart count. It returns the number of harts now usable by the
// runtime (1 if SMP could not be enabled). Must be called after the runtime is
// up (i.e. from main), since it allocates and calls runtime.GOMAXPROCS.
func InitSMP() (nharts int) {
	boot := readtp()
	addr := uint64(secondaryEntryPC())

	for h := uint64(0); h < maxHarts; h++ {
		if h == boot {
			continue
		}
		if _, exists := hartGetStatus(h); !exists {
			continue // no such hart
		}
		if hartStart(h, addr, h) != 0 {
			continue // firmware refused to start it
		}
		// Wait until the hart reaches its park loop before counting it.
		for atomic.LoadUint32(&readyFlag[h]) == 0 {
		}
		parkedHarts = append(parkedHarts, h)
	}

	nharts = 1 + len(parkedHarts)
	numHarts = nharts
	if nharts > 1 {
		goos.ProcID = CurrentHart
		goos.Task = honkTask
		runtime.GOMAXPROCS(nharts)
	}
	return
}

// honkTask is the runtime's goos.Task hook (runtime.newosproc). It hands a
// newly created M - its runtime-allocated stack (sp), g0 (gp), and entry
// (runtime.mstart, fn) - to the next parked hart and releases it. The runtime
// calls this exactly len(parkedHarts) times: one M per secondary hart, and Ms
// are never dropped on tamago.
func honkTask(sp, _, gp, fn unsafe.Pointer) {
	i := atomic.AddInt32(&taskIdx, 1) - 1
	if int(i) >= len(parkedHarts) {
		panic("honk: SMP Task exceeds parked harts")
	}
	h := parkedHarts[i]

	taskSP[h] = uint64(uintptr(sp))
	taskGP[h] = uint64(uintptr(gp))
	atomic.StoreUint64(&taskPC[h], uint64(uintptr(fn))) // release: wakes park loop
}
