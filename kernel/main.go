// honk - a modern pure-Go multiprocess RISC-V 64 operating system.
//
// This is the HS-mode Go program that is the whole OS. M0 brings up the boot
// foundation (HS-mode under OpenSBI, SMP across all harts); M1 adds the
// interrupt-driven UART console and an interactive shell.

//go:build tamago && riscv64

package main

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"honk/board/virt"
	"honk/kernel/proc"
)

const banner = `
   __     honk
 >(o )___   pure-Go RISC-V64 OS
  (  ._> /  HS-mode under OpenSBI
   '---'
`

func main() {
	// Tripwire via the runtime's print builtin (routes through goos.Printk):
	// if only this line appears, fmt's fd routing is the suspect.
	println("honk: entered main")

	fmt.Print(banner)
	fmt.Printf("honk: HS-mode boot ok  hart=%d  dtb=%#x\n", virt.BootHart(), virt.DTB())

	// Bring every hart up as a Go runtime M; GOMAXPROCS becomes the hart count.
	nharts := virt.InitSMP()
	fmt.Printf("honk: SMP up  harts=%d  GOMAXPROCS=%d\n", nharts, runtime.GOMAXPROCS(-1))

	// Bring up the interrupt-driven UART console (M1) early, so input typed
	// (or piped) during the demo below is captured into the channel.
	virt.InitConsole()

	// Discover the block device (M3) and mount the filesystem (M4): the kv
	// store over it, overlaid on the immutable embedded core.
	virt.InitStorage()
	mountFS()

	// Prove the scheduler actually runs goroutines across multiple harts.
	harts := smpDemo(nharts)
	if len(harts) > 1 {
		fmt.Printf("honk: SMP OK - goroutines ran on %d/%d harts %v\n", len(harts), nharts, harts)
	} else {
		fmt.Printf("honk: SMP single-hart - goroutines ran on %v\n", harts)
	}

	// PID 1: the init process (M2). It holds the proc capability and lives
	// until shutdown, so `ps` always shows a root process.
	procs.Spawn("init", proc.Caps{proc.CapProc: true}, func(ctx context.Context) { <-ctx.Done() })

	// Hand off to the interactive shell over the UART (M1). It returns only on
	// EOF; commands `exit`/`fault` power the machine off via SBI.
	runShell(virt.Console())
}

// smpDemo runs many CPU-bound goroutines for a short window and returns the
// sorted set of distinct harts they were observed running on. With GOMAXPROCS
// harts and >GOMAXPROCS runnable goroutines, the scheduler spreads them across
// every hart; runtime.Gosched keeps the loop preemptible (no async preemption
// on tamago) so the GC and rebalancing can run.
func smpDemo(nharts int) []int {
	const window = 200 * time.Millisecond
	const seenLen = 16
	var seen [seenLen]uint32

	var wg sync.WaitGroup
	deadline := time.Now().Add(window)
	for i := 0; i < 4*nharts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				atomic.StoreUint32(&seen[virt.CurrentHart()%seenLen], 1)
				runtime.Gosched()
			}
		}()
	}
	wg.Wait()

	harts := make([]int, 0, seenLen)
	for h := 0; h < seenLen; h++ {
		if atomic.LoadUint32(&seen[h]) == 1 {
			harts = append(harts, h)
		}
	}
	return harts
}
