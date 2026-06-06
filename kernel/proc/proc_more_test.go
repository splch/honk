package proc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// --- pure helpers: State and Caps formatting (what `ps` renders) -------------

func TestStateString(t *testing.T) {
	cases := map[State]string{
		Running:   "running",
		Done:      "done",
		Killed:    "killed",
		Panicked:  "panicked",
		State(99): "unknown",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", int(s), got, want)
		}
	}
}

func TestCapsHasNilSafe(t *testing.T) {
	var c Caps // nil map
	if c.Has(CapProc) {
		t.Fatal("nil Caps.Has returned true")
	}
	if c.String() != "-" {
		t.Fatalf("nil Caps.String() = %q, want %q", c.String(), "-")
	}
}

func TestCapsStringSortedAndFiltered(t *testing.T) {
	c := Caps{CapNet: true, CapConsole: true, CapProc: true, CapBlock: false}
	// sorted by name, and a false-valued grant is excluded.
	if got, want := c.String(), "console,net,proc"; got != want {
		t.Fatalf("Caps.String() = %q, want %q", got, want)
	}
	if !c.Has(CapNet) || c.Has(CapBlock) {
		t.Fatalf("Has: net=%v block=%v, want true,false", c.Has(CapNet), c.Has(CapBlock))
	}
	if (Caps{}).String() != "-" {
		t.Fatal("empty Caps.String() != \"-\"")
	}
}

// --- Self: context carries the Proc only for process contexts ----------------

func TestSelfOnNonProcessContexts(t *testing.T) {
	if Self(context.Background()) != nil {
		t.Fatal("Self(Background) != nil")
	}
	if Self(context.TODO()) != nil {
		t.Fatal("Self(TODO) != nil")
	}
	// A context carrying a wrong-typed value under the same key must not be
	// mistaken for a Proc (the type assertion in Self must fail closed).
	ctx := context.WithValue(context.Background(), ctxKey{}, "not a *Proc")
	if Self(ctx) != nil {
		t.Fatal("Self with wrong-typed ctxKey value != nil")
	}
}

func TestSelfRoundTrip(t *testing.T) {
	tab := NewTable()
	got := make(chan *Proc, 1)
	p := tab.Spawn("x", Caps{CapBlock: true}, func(ctx context.Context) {
		got <- Self(ctx)
	})
	if self := <-got; self != p {
		t.Fatalf("Self(ctx) = %p, want the spawned Proc %p", self, p)
	}
}

// --- context lifecycle: cancel == kill ---------------------------------------

func TestContextCancelsExactlyOnKill(t *testing.T) {
	tab := NewTable()
	running := make(chan struct{})
	p := tab.Spawn("w", nil, func(ctx context.Context) {
		close(running)
		<-ctx.Done()
	})
	<-running
	if err := p.Context().Err(); err != nil {
		t.Fatalf("ctx.Err() before Kill = %v, want nil", err)
	}
	tab.Kill(p.PID)
	select {
	case <-p.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("ctx.Done() did not close after Kill")
	}
	if err := p.Context().Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx.Err() after Kill = %v, want context.Canceled", err)
	}
}

// --- Err reflects the terminal reason ----------------------------------------

func TestErrByTerminalState(t *testing.T) {
	tab := NewTable()

	done := tab.Spawn("d", nil, func(ctx context.Context) {})
	waitState(t, done, Done)
	if done.Err() != nil {
		t.Fatalf("Done proc Err() = %v, want nil", done.Err())
	}

	panicked := tab.Spawn("p", nil, func(ctx context.Context) { panic("kaboom") })
	waitState(t, panicked, Panicked)
	if err := panicked.Err(); err == nil || !contains(err.Error(), "kaboom") {
		t.Fatalf("Panicked proc Err() = %v, want one mentioning the panic value", err)
	}

	started := make(chan struct{})
	killed := tab.Spawn("k", nil, func(ctx context.Context) { close(started); <-ctx.Done() })
	<-started
	tab.Kill(killed.PID)
	waitState(t, killed, Killed)
	if killed.Err() != nil {
		t.Fatalf("Killed proc Err() = %v, want nil", killed.Err())
	}
}

// --- the state-machine guards (deterministic, no goroutine/timing) -----------

// TestFinishDoesNotOverrideKilled pins the rule in finish(): once Kill has moved
// a process to Killed, the deferred finish() that runs when a cooperative fn
// returns must NOT relabel it Done. (Otherwise `ps` would show a killed process
// as "done" and the reason for termination would be lost.)
func TestFinishDoesNotOverrideKilled(t *testing.T) {
	p := &Proc{state: Running}
	// simulate Kill marking it (Kill does this under the table lock):
	p.mu.Lock()
	p.state = Killed
	p.mu.Unlock()
	// simulate the deferred finish() after the cooperative fn returns:
	p.finish(Done, nil)
	if p.State() != Killed {
		t.Fatalf("state after finish(Done) = %s, want killed (Kill must win)", p.State())
	}
}

// TestMarkPanickedOverridesKilled pins the opposite rule: a panic is a fault and
// overrides a pending Killed, so a process that was killed but then panicked is
// reported as panicked (the more severe, more informative outcome).
func TestMarkPanickedOverridesKilled(t *testing.T) {
	p := &Proc{state: Killed}
	p.markPanicked(errors.New("fault"))
	if p.State() != Panicked {
		t.Fatalf("state after markPanicked = %s, want panicked", p.State())
	}
	if p.Err() == nil {
		t.Fatal("markPanicked left Err() nil")
	}
}

func TestFinishFromRunningSetsDone(t *testing.T) {
	p := &Proc{state: Running}
	p.finish(Done, nil)
	if p.State() != Done {
		t.Fatalf("state = %s, want done", p.State())
	}
}

// --- PIDs: monotonic, unique, first is 1, and unique under concurrency -------

func TestPIDsMonotonicFromOne(t *testing.T) {
	tab := NewTable()
	for want := 1; want <= 50; want++ {
		p := tab.Spawn("x", nil, func(ctx context.Context) {})
		if p.PID != want {
			t.Fatalf("PID = %d, want %d (PIDs must be strictly increasing from 1)", p.PID, want)
		}
	}
}

func TestConcurrentSpawnUniquePIDs(t *testing.T) {
	tab := NewTable()
	const workers, each = 16, 100
	var wg sync.WaitGroup
	var mu sync.Mutex
	pids := make(map[int]bool, workers*each)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				p := tab.Spawn("x", nil, func(ctx context.Context) { <-ctx.Done() })
				mu.Lock()
				if pids[p.PID] {
					t.Errorf("duplicate PID %d handed out", p.PID)
				}
				pids[p.PID] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(pids) != workers*each {
		t.Fatalf("got %d unique PIDs, want %d", len(pids), workers*each)
	}
	// clean up the live processes.
	for _, p := range tab.List() {
		tab.Kill(p.PID)
	}
}

// --- table queries: List ordering, Get, Reap, Count --------------------------

func TestListOrderedByPID(t *testing.T) {
	tab := NewTable()
	for i := 0; i < 20; i++ {
		tab.Spawn(fmt.Sprintf("p%d", i), nil, func(ctx context.Context) { <-ctx.Done() })
	}
	list := tab.List()
	if len(list) != 20 {
		t.Fatalf("List len = %d, want 20", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].PID >= list[i].PID {
			t.Fatalf("List not sorted by PID at %d: %d >= %d", i, list[i-1].PID, list[i].PID)
		}
	}
	for _, p := range list {
		tab.Kill(p.PID)
	}
}

func TestGetPresentAndMissing(t *testing.T) {
	tab := NewTable()
	p := tab.Spawn("x", nil, func(ctx context.Context) { <-ctx.Done() })
	if got, ok := tab.Get(p.PID); !ok || got != p {
		t.Fatalf("Get(%d) = %v, %v; want the proc, true", p.PID, got, ok)
	}
	if _, ok := tab.Get(424242); ok {
		t.Fatal("Get(missing) returned ok=true")
	}
	tab.Kill(p.PID)
}

func TestReapRemovesOnlyTerminated(t *testing.T) {
	tab := NewTable()
	// 5 that finish, 3 that keep running.
	for i := 0; i < 5; i++ {
		p := tab.Spawn("done", nil, func(ctx context.Context) {})
		waitState(t, p, Done)
	}
	var live []*Proc
	for i := 0; i < 3; i++ {
		live = append(live, tab.Spawn("live", nil, func(ctx context.Context) { <-ctx.Done() }))
	}
	if tab.Count() != 8 {
		t.Fatalf("Count = %d, want 8", tab.Count())
	}
	if n := tab.Reap(); n != 5 {
		t.Fatalf("Reap = %d, want 5", n)
	}
	if tab.Count() != 3 {
		t.Fatalf("Count after reap = %d, want 3 (the live procs)", tab.Count())
	}
	for _, p := range live {
		if _, ok := tab.Get(p.PID); !ok {
			t.Fatalf("Reap removed still-running PID %d", p.PID)
		}
		tab.Kill(p.PID)
	}
}

// --- Kill semantics: running -> true once; missing/again -> false ------------

func TestKillReturnValues(t *testing.T) {
	tab := NewTable()
	if tab.Kill(7) {
		t.Fatal("Kill(missing) returned true")
	}
	started := make(chan struct{})
	p := tab.Spawn("w", nil, func(ctx context.Context) { close(started); <-ctx.Done() })
	<-started
	if !tab.Kill(p.PID) {
		t.Fatal("first Kill of a running proc returned false")
	}
	waitState(t, p, Killed)
	if tab.Kill(p.PID) {
		t.Fatal("second Kill of the same proc returned true")
	}
}

// --- fault domain: many panics, kernel (table) stays healthy -----------------

func TestRecoverContainsManyPanics(t *testing.T) {
	tab := NewTable()
	for i := 0; i < 200; i++ {
		p := tab.Spawn("boom", nil, func(ctx context.Context) { panic("x") })
		waitState(t, p, Panicked)
	}
	// The table is unharmed: a fresh process runs to completion and queries work.
	done := tab.Spawn("after", Caps{CapConsole: true}, func(ctx context.Context) {})
	waitState(t, done, Done)
	if !done.Can(CapConsole) {
		t.Fatal("post-panic process lost its capability")
	}
	if n := tab.Reap(); n != 201 {
		t.Fatalf("Reap = %d, want 201 (200 panicked + 1 done)", n)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
