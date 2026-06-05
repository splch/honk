// honk - a modern pure-Go multiprocess RISC-V 64 operating system.
//
// This is the HS-mode Go program that is the whole OS. M0 proves the
// foundation: boot as an OpenSBI S-mode payload on QEMU virt, bring every hart
// up as a Go runtime M (SMP), exercise the scheduler across cores, and shut
// down cleanly.

//go:build tamago && riscv64

package main

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"honk/board/virt"
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

	// Prove the scheduler actually runs goroutines across multiple harts.
	harts := smpDemo(nharts)
	if len(harts) > 1 {
		fmt.Printf("honk: SMP OK - goroutines ran on %d/%d harts %v\n", len(harts), nharts, harts)
	} else {
		fmt.Printf("honk: SMP single-hart - goroutines ran on %v\n", harts)
	}

	// Goroutine + channel round-trip - honk's process + IPC primitives.
	ch := make(chan string, 1)
	go func() { ch <- "honk" }()
	fmt.Printf("honk: goroutine+channel round-trip -> %q\n", <-ch)

	fmt.Println("honk: M0 ok - clean shutdown")
	// Returning from main triggers runtime exit -> goos.Exit -> SBI shutdown,
	// which stops every hart and exits QEMU.
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
