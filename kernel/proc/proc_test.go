package proc

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestSpawnRunDone(t *testing.T) {
	tab := NewTable()
	done := make(chan struct{})
	p := tab.Spawn("hello", Caps{CapConsole: true}, func(ctx context.Context) {
		close(done)
	})
	<-done
	waitState(t, p, Done)

	if !p.Can(CapConsole) || p.Can(CapNet) {
		t.Fatalf("caps wrong: %s", p.Caps)
	}
	if p.PID != 1 {
		t.Fatalf("first PID = %d, want 1", p.PID)
	}
}

func TestKillCancelsContext(t *testing.T) {
	tab := NewTable()
	started := make(chan struct{})
	p := tab.Spawn("worker", nil, func(ctx context.Context) {
		close(started)
		<-ctx.Done() // cooperative cancellation
	})
	<-started

	if !tab.Kill(p.PID) {
		t.Fatal("Kill of running proc returned false")
	}
	waitState(t, p, Killed)

	if tab.Kill(p.PID) {
		t.Fatal("Kill of already-killed proc returned true")
	}
	if tab.Kill(9999) {
		t.Fatal("Kill of missing PID returned true")
	}
}

func TestPanicIsContained(t *testing.T) {
	tab := NewTable()
	p := tab.Spawn("crasher", nil, func(ctx context.Context) {
		panic("boom")
	})
	waitState(t, p, Panicked)

	if err := p.Err(); err == nil {
		t.Fatal("panicked proc has nil Err")
	}
	// The table (kernel) is unharmed and still usable.
	q := tab.Spawn("after", nil, func(ctx context.Context) {})
	waitState(t, q, Done)
}

func TestSelfExposesCaps(t *testing.T) {
	tab := NewTable()
	got := make(chan bool, 1)
	tab.Spawn("net", Caps{CapNet: true}, func(ctx context.Context) {
		self := Self(ctx)
		got <- self != nil && self.Can(CapNet) && !self.Can(CapBlock)
	})
	if !<-got {
		t.Fatal("Self(ctx) did not expose the process capabilities")
	}
}

func TestReap(t *testing.T) {
	tab := NewTable()
	for i := 0; i < 10; i++ {
		p := tab.Spawn("x", nil, func(ctx context.Context) {})
		waitState(t, p, Done)
	}
	live := tab.Spawn("live", nil, func(ctx context.Context) { <-ctx.Done() })
	if n := tab.Reap(); n != 10 {
		t.Fatalf("reaped %d, want 10", n)
	}
	if tab.Count() != 1 {
		t.Fatalf("count after reap = %d, want 1 (the live proc)", tab.Count())
	}
	tab.Kill(live.PID)
}

// TestConcurrentStress hammers the table from many goroutines: spawning,
// killing, listing, and reaping, with a mix of cooperative, looping, and
// panicking workloads. Run with -race to catch data races on the table.
func TestConcurrentStress(t *testing.T) {
	tab := NewTable()
	const workers = 16
	const each = 200

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				switch i % 4 {
				case 0:
					p := tab.Spawn("loop", Caps{CapConsole: true}, func(ctx context.Context) {
						<-ctx.Done()
					})
					tab.Kill(p.PID)
				case 1:
					tab.Spawn("quick", nil, func(ctx context.Context) {})
				case 2:
					tab.Spawn("boom", nil, func(ctx context.Context) { panic("x") })
				case 3:
					_ = tab.List()
					tab.Reap()
				}
			}
		}(w)
	}
	wg.Wait()

	// Drain: kill anything still running, then reap; the table must be empty
	// and internally consistent.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range tab.List() {
			tab.Kill(p.PID)
		}
		tab.Reap()
		if tab.Count() == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("table not drained: %d procs remain", tab.Count())
}

func waitState(t *testing.T, p *Proc, want State) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.State() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("proc %d (%s) state = %s, want %s", p.PID, p.Name, p.State(), want)
}
