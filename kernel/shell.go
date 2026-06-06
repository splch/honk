// honk - a tiny interactive shell over the UART console (M1), extended with
// process-model commands (M2).

//go:build tamago && riscv64

package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
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
		fmt.Println("  ls [dir]   cat <file>   cp <src> <dst>   put <key> <text>   rm <file>")
		fmt.Println("  blk   net   mount   wasm <file.wasm>   fb   ui   reset --confirm   fault  exit")
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
	case "blk":
		blk()
	case "net":
		netcmd()
	case "mount":
		mounts()
	case "wasm":
		wasmcmd(fields)
	case "fb":
		fbcmd()
	case "ui":
		uicmd()
	case "ls":
		ls(fields)
	case "cat":
		cat(fields)
	case "cp":
		cp(fields)
	case "put":
		put(fields)
	case "rm":
		rm(fields)
	case "reset":
		reset(fields)

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

// blk reports the block device and runs a write/read-back self-test on its last
// block (avoiding live data) to prove the driver round-trips.
func blk() {
	d := virt.Block()
	if d == nil {
		fmt.Println("blk: no block device")
		return
	}
	bs := d.BlockSize()
	fmt.Printf("blk: %d blocks x %d B = %d MiB\n",
		d.Blocks(), bs, d.Blocks()*int64(bs)>>20)

	last := d.Blocks() - 1
	w := make([]byte, bs)
	for i := range w {
		w[i] = byte(i*7 + 1)
	}
	if err := d.WriteBlocks(last, w); err != nil {
		fmt.Printf("blk: write error: %v\n", err)
		return
	}
	r := make([]byte, bs)
	if err := d.ReadBlocks(last, r); err != nil {
		fmt.Printf("blk: read error: %v\n", err)
		return
	}
	if bytes.Equal(w, r) {
		fmt.Printf("blk: read/write self-test OK (block %d)\n", last)
	} else {
		fmt.Println("blk: read/write self-test MISMATCH")
	}
}

// mounts reports the filesystem layers that compose the overlay root, top
// (shadowing) to bottom.
func mounts() {
	if store != nil {
		fmt.Println("  kv     writable   (shadows the layers below)")
	}
	if hostTag != "" {
		fmt.Printf("  host   read-only  9p tag %q\n", hostTag)
	}
	fmt.Printf("  core   read-only  %s\n", bootCore)
}

// ls lists a directory in the overlay filesystem.
func ls(fields []string) {
	dir := "."
	if len(fields) > 1 {
		p, ok := fsPath(fields[1])
		if !ok {
			fmt.Println("ls: invalid path")
			return
		}
		dir = p
	}
	es, err := fs.ReadDir(root, dir)
	if err != nil {
		fmt.Printf("ls: %v\n", err)
		return
	}
	for _, e := range es {
		if e.IsDir() {
			fmt.Printf("  %-22s <dir>\n", e.Name()+"/")
			continue
		}
		fi, _ := e.Info()
		fmt.Printf("  %-22s %d\n", e.Name(), fi.Size())
	}
}

// cat prints a file from the overlay filesystem.
func cat(fields []string) {
	if len(fields) < 2 {
		fmt.Println("usage: cat <file>")
		return
	}
	p, ok := fsPath(fields[1])
	if !ok {
		fmt.Println("cat: invalid path")
		return
	}
	data, err := fs.ReadFile(root, p)
	if err != nil {
		fmt.Printf("cat: %v\n", err)
		return
	}
	fmt.Print(string(data))
	if n := len(data); n == 0 || data[n-1] != '\n' {
		fmt.Println()
	}
}

// cp copies a file into the writable kv layer (which shadows the core).
func cp(fields []string) {
	if len(fields) < 3 {
		fmt.Println("usage: cp <src> <dst>")
		return
	}
	if store == nil {
		fmt.Println("cp: filesystem is read-only (no block device)")
		return
	}
	src, ok := fsPath(fields[1])
	dst, ok2 := fsPath(fields[2])
	if !ok || !ok2 || dst == "." {
		fmt.Println("cp: invalid path")
		return
	}
	data, err := fs.ReadFile(root, src)
	if err != nil {
		fmt.Printf("cp: %v\n", err)
		return
	}
	if err := store.Put(dst, data); err != nil {
		fmt.Printf("cp: %v\n", err)
		return
	}
	fmt.Printf("cp: %s -> %s (%d bytes)\n", src, dst, len(data))
}

// put writes inline text to a key in the writable kv layer.
func put(fields []string) {
	if len(fields) < 3 {
		fmt.Println("usage: put <key> <text...>")
		return
	}
	if store == nil {
		fmt.Println("put: filesystem is read-only (no block device)")
		return
	}
	p, ok := fsPath(fields[1])
	if !ok || p == "." {
		fmt.Println("put: invalid key")
		return
	}
	if err := store.Put(p, []byte(strings.Join(fields[2:], " "))); err != nil {
		fmt.Printf("put: %v\n", err)
		return
	}
	fmt.Printf("put: wrote %s\n", p)
}

// rm deletes a key from the writable kv layer.
func rm(fields []string) {
	if len(fields) < 2 {
		fmt.Println("usage: rm <file>")
		return
	}
	if store == nil {
		fmt.Println("rm: filesystem is read-only (no block device)")
		return
	}
	p, ok := fsPath(fields[1])
	if !ok {
		fmt.Println("rm: invalid path")
		return
	}
	if !store.Has(p) {
		fmt.Printf("rm: %s: not in writable layer\n", p)
		return
	}
	if err := store.Delete(p); err != nil {
		fmt.Printf("rm: %v\n", err)
		return
	}
	fmt.Printf("rm: removed %s\n", p)
}

// reset is honk's stateless reset: it clears the writable kv layer so the
// immutable, verified core shows through unshadowed. It requires --confirm
// because it discards all persisted state.
func reset(fields []string) {
	if store == nil {
		fmt.Println("reset: no writable layer (read-only core)")
		return
	}
	if len(fields) < 2 || fields[1] != "--confirm" {
		fmt.Println("reset: clears ALL state in the writable layer; re-run as: reset --confirm")
		return
	}
	if err := store.Reset(); err != nil {
		fmt.Printf("reset: %v\n", err)
		return
	}
	fmt.Printf("reset: writable layer cleared (immutable core %s remains)\n", bootCore)
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
