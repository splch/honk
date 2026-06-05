// honk - a tiny interactive shell over the UART console (M1), extended with
// process-model commands (M2).

//go:build tamago && riscv64

package main

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"honk/board/virt"
	"honk/kernel/proc"
)

// procs is honk's process table: the map[PID]*proc that `ps` iterates and
// `kill` cancels. Goroutine + context + capabilities = process (HONK.md §1).
var procs = proc.NewTable()

// runShell reads bytes from the console channel, line-edits with echo, and
// dispatches commands. It returns only if the input channel closes.
func runShell(in <-chan byte) {
	fmt.Print("\nhonk: shell ready (type 'help')\nhonk> ")

	var line []byte
	for b := range in {
		switch b {
		case '\r', '\n':
			fmt.Print("\r\n")
			exec(string(line))
			line = line[:0]
			fmt.Print("honk> ")
		case 0x7f, 0x08: // DEL / backspace
			if len(line) > 0 {
				line = line[:len(line)-1]
				fmt.Print("\b \b")
			}
		default:
			if b >= 0x20 && b < 0x7f { // printable
				line = append(line, b)
				fmt.Printf("%c", b) // echo (terminal is raw under -nographic)
			}
		}
	}
}

func exec(line string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return
	}
	switch fields[0] {
	case "help":
		fmt.Println("commands:")
		fmt.Println("  help  harts  uptime  mem  echo <text>")
		fmt.Println("  run [name]   ps   kill <pid>   crash   reap   stress [n]")
		fmt.Println("  fault  exit")
	case "harts":
		fmt.Printf("harts: %d online  GOMAXPROCS=%d  this=hart %d\n",
			virt.NumHarts(), runtime.GOMAXPROCS(-1), virt.CurrentHart())
	case "uptime":
		fmt.Printf("uptime: %s\n", virt.Uptime())
	case "mem":
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		fmt.Printf("mem: heap=%dKiB sys=%dKiB goroutines=%d numgc=%d\n",
			m.HeapAlloc/1024, m.Sys/1024, runtime.NumGoroutine(), m.NumGC)
	case "echo":
		fmt.Println(strings.Join(fields[1:], " "))

	case "run":
		name := "worker"
		if len(fields) > 1 {
			name = fields[1]
		}
		p := procs.Spawn(name, proc.Caps{proc.CapConsole: true}, worker)
		fmt.Printf("spawned PID %d (%s)\n", p.PID, p.Name)
	case "ps":
		ps()
	case "kill":
		if len(fields) < 2 {
			fmt.Println("usage: kill <pid>")
			break
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			fmt.Printf("bad pid: %q\n", fields[1])
			break
		}
		if procs.Kill(pid) {
			fmt.Printf("killed PID %d\n", pid)
		} else {
			fmt.Printf("PID %d not running\n", pid)
		}
	case "crash":
		// Demonstrate the recover() fault domain: this process panics, is
		// reaped as 'panicked', and the kernel and every other process live on.
		p := procs.Spawn("crasher", nil, func(ctx context.Context) {
			panic("intentional crash")
		})
		fmt.Printf("spawned PID %d (crasher) - it will panic; kernel survives\n", p.PID)
	case "reap":
		fmt.Printf("reaped %d terminated processes\n", procs.Reap())
	case "stress":
		stress(fields)

	case "fault":
		fmt.Println("fault: raising a supervisor exception...")
		virt.Fault() // prints trap CSRs and halts; does not return
	case "exit", "halt", "quit":
		fmt.Println("honk: shutting down")
		virt.Shutdown() // does not return
	default:
		fmt.Printf("unknown command: %q (try 'help')\n", fields[0])
	}
}

// worker is a cooperative background process: it ticks until its context is
// cancelled (by `kill`), then returns. Uncooperative (tight-loop) code is a
// job for the WASM/VM tiers, not a trusted goroutine.
func worker(ctx context.Context) {
	tk := time.NewTicker(250 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}

func ps() {
	list := procs.List()
	fmt.Printf("%-4s %-10s %-9s %-9s %s\n", "PID", "NAME", "STATE", "UPTIME", "CAPS")
	for _, p := range list {
		fmt.Printf("%-4d %-10s %-9s %-9s %s\n",
			p.PID, p.Name, p.State(), time.Since(p.Started).Round(time.Millisecond), p.Caps)
	}
	fmt.Printf("(%d processes)\n", len(list))
}

// stress spawns many short-lived processes that run briefly across harts,
// exercising the process table under SMP. The host-side equivalent runs under
// `go test -race ./kernel/proc`.
func stress(fields []string) {
	n := 64
	if len(fields) > 1 {
		if v, err := strconv.Atoi(fields[1]); err == nil && v > 0 {
			n = v
		}
	}
	var seen [16]uint32
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		procs.Spawn("stress", proc.Caps{proc.CapProc: true}, func(ctx context.Context) {
			for j := 0; j < 3000 && ctx.Err() == nil; j++ {
				atomic.StoreUint32(&seen[virt.CurrentHart()%16], 1)
				runtime.Gosched()
			}
			done <- struct{}{}
		})
	}
	for i := 0; i < n; i++ {
		<-done
	}
	harts := 0
	for _, v := range seen {
		if v == 1 {
			harts++
		}
	}
	fmt.Printf("stress: %d processes ran across %d harts, reaped %d\n", n, harts, procs.Reap())
}
