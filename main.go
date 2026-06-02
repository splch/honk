//go:build tamago

// Command honk is a small RISC-V 64-bit operating system written in pure Go.
//
// honk runs as a TamaGo unikernel: the Go runtime is the kernel, goroutines are
// the tasks, and channels are the IPC. There is no user/kernel split and no
// process model — see DESIGN.md for the rationale and RV64.md for the hardware.
//
// The board is selected at build time via a tag (see board_sifive_u.go /
// board_virt.go), which blank-imports the board support package and sets
// boardName. The //go:build tamago constraint keeps host tooling (go test
// ./internal/..., gofmt) happy: honk is only ever built with GOOS=tamago.
package main

import (
	"fmt"
	"runtime"
	"time"

	"github.com/splch/honk/internal/banner"
)

func main() {
	fmt.Println()
	fmt.Println(banner.Line(runtime.Version(), runtime.GOARCH, boardName))
	fmt.Println()

	// A few "tasks" (goroutines) honk back over a channel — the unikernel's
	// concurrency model in miniature (GO.md §10).
	const tasks = 3
	honks := make(chan string)
	for id := 1; id <= tasks; id++ {
		go func() { honks <- fmt.Sprintf("task %d reporting in", id) }()
	}
	for range tasks {
		fmt.Printf("  honk: %s\n", <-honks)
	}

	// Heartbeat: proves the scheduler, the timer (nanotime), and the idle path
	// are alive. A perpetually-live goroutine also keeps the runtime's
	// all-goroutines-asleep deadlock detector quiet.
	start := time.Now()
	for n := 1; ; n++ {
		time.Sleep(3 * time.Second)
		fmt.Printf("honk #%d — up %s, %d goroutines\n",
			n, time.Since(start).Round(time.Second), runtime.NumGoroutine())
	}
}
