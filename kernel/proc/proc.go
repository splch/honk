// Package proc is honk's process model: the OS "process" mapped onto Go
// language primitives (HONK.md §1). A process is a goroutine plus a
// context.Context (cancel = kill, Done = reaped), a capability set (what
// interfaces it is allowed to touch), and a recover() fault domain so a
// panicking process is reaped while the kernel and siblings survive.
//
// This package is deliberately free of any bare-metal dependency so it builds
// and is race-tested (`go test -race`) on the host.
package proc

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// State is the lifecycle state of a process.
type State int32

const (
	Running  State = iota // goroutine is executing
	Done                  // returned normally
	Killed                // context was cancelled via Kill
	Panicked              // recovered from a panic (fault domain)
)

func (s State) String() string {
	switch s {
	case Running:
		return "running"
	case Done:
		return "done"
	case Killed:
		return "killed"
	case Panicked:
		return "panicked"
	default:
		return "unknown"
	}
}

// Cap is a capability: a named authority a process must hold to use a
// resource. In honk a capability is ultimately an interface value that is or
// is not handed to a process; Cap/Caps is the bookkeeping the kernel checks.
type Cap string

const (
	CapConsole Cap = "console"
	CapNet     Cap = "net"
	CapBlock   Cap = "block"
	CapProc    Cap = "proc" // may spawn/kill other processes
)

// Caps is a set of capabilities granted to a process.
type Caps map[Cap]bool

// Has reports whether the set grants c.
func (c Caps) Has(cap Cap) bool { return c[cap] }

// String renders the granted capabilities, sorted, e.g. "console,net".
func (c Caps) String() string {
	if len(c) == 0 {
		return "-"
	}
	names := make([]string, 0, len(c))
	for cap, ok := range c {
		if ok {
			names = append(names, string(cap))
		}
	}
	sort.Strings(names)
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ","
		}
		out += n
	}
	return out
}

// Proc is a running (or terminated) honk process.
type Proc struct {
	PID     int
	Name    string
	Caps    Caps      // immutable after Spawn
	Started time.Time // immutable after Spawn

	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex // guards state/err
	state State
	err   error
}

// Context returns the process context; its Done channel closes on Kill.
func (p *Proc) Context() context.Context { return p.ctx }

// Can reports whether the process holds capability c.
func (p *Proc) Can(c Cap) bool { return p.Caps.Has(c) }

// State returns the current lifecycle state.
func (p *Proc) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Err returns the panic/termination reason, if any.
func (p *Proc) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *Proc) finish(s State, err error) {
	p.mu.Lock()
	if p.state == Running { // a prior Kill already moved us to Killed; keep it
		p.state = s
		p.err = err
	}
	p.mu.Unlock()
}

func (p *Proc) markPanicked(err error) {
	p.mu.Lock()
	p.state = Panicked // a panic overrides any pending Killed
	p.err = err
	p.mu.Unlock()
}

type ctxKey struct{}

// Self returns the Proc associated with ctx, or nil if ctx is not a process
// context. A process uses it to consult its own capabilities.
func Self(ctx context.Context) *Proc {
	p, _ := ctx.Value(ctxKey{}).(*Proc)
	return p
}

// Table is the honk process table: the map[PID]*Proc that `ps` iterates and
// `kill` cancels. It is safe for concurrent use across harts.
type Table struct {
	mu      sync.Mutex
	procs   map[int]*Proc
	nextPID int
}

// NewTable returns an empty process table (PIDs start at 1).
func NewTable() *Table {
	return &Table{procs: make(map[int]*Proc), nextPID: 1}
}

// Spawn starts fn as a new process with the given name and capabilities, and
// returns its Proc. fn receives a context that is cancelled on Kill; it should
// return promptly once ctx.Done() fires (uncooperative code belongs in the
// WASM or VM tier). A panic in fn is contained: the process is reaped as
// Panicked and the kernel keeps running.
func (t *Table) Spawn(name string, caps Caps, fn func(ctx context.Context)) *Proc {
	ctx, cancel := context.WithCancel(context.Background())

	t.mu.Lock()
	pid := t.nextPID
	t.nextPID++
	p := &Proc{
		PID:     pid,
		Name:    name,
		Caps:    caps,
		Started: time.Now(),
		cancel:  cancel,
		state:   Running,
	}
	p.ctx = context.WithValue(ctx, ctxKey{}, p)
	t.procs[pid] = p
	t.mu.Unlock()

	go func() {
		defer p.cancel() // release context resources on any exit
		defer func() {
			if r := recover(); r != nil {
				p.markPanicked(fmt.Errorf("panic: %v", r))
			} else {
				p.finish(Done, nil)
			}
		}()
		fn(p.ctx)
	}()

	return p
}

// Kill cancels the process's context, reporting whether the PID existed and was
// still running. The process observes ctx.Done() and returns; its final state
// is Killed.
func (t *Table) Kill(pid int) bool {
	t.mu.Lock()
	p := t.procs[pid]
	t.mu.Unlock()
	if p == nil {
		return false
	}

	p.mu.Lock()
	running := p.state == Running
	if running {
		p.state = Killed
	}
	p.mu.Unlock()

	p.cancel()
	return running
}

// Get returns the process with the given PID.
func (t *Table) Get(pid int) (*Proc, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, ok := t.procs[pid]
	return p, ok
}

// List returns a snapshot of all processes, ordered by PID.
func (t *Table) List() []*Proc {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*Proc, 0, len(t.procs))
	for _, p := range t.procs {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// Reap removes all terminated (non-running) processes from the table and
// returns how many were removed.
func (t *Table) Reap() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for pid, p := range t.procs {
		if p.State() != Running {
			delete(t.procs, pid)
			n++
		}
	}
	return n
}

// Count returns the number of processes currently in the table.
func (t *Table) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.procs)
}
