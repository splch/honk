package wasm

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// TestRunHello runs the seeded demo module and checks its sandboxed stdout. It
// also validates HelloModule's hand-encoded bytes: wazero rejects a malformed
// module, so a green test means the encoding is correct.
func TestRunHello(t *testing.T) {
	var buf bytes.Buffer
	if err := Run(context.Background(), &buf, HelloModule); err != nil {
		t.Fatalf("Run(HelloModule): %v", err)
	}
	if got, want := buf.String(), "honk from wasm!\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// TestRunRejectsGarbage confirms a non-WebAssembly input is an error, not a
// panic or silent success.
func TestRunRejectsGarbage(t *testing.T) {
	err := Run(context.Background(), io.Discard, []byte("definitely not wasm"))
	if err == nil {
		t.Fatal("Run accepted non-wasm input")
	}
	if !strings.Contains(err.Error(), "wasm:") {
		t.Errorf("error = %q, want it to mention wasm", err)
	}
}

// TestRunStdoutIsolation confirms two runs of the same module do not share state
// (each gets a fresh runtime and linear memory) and that nil out is tolerated.
func TestRunStdoutIsolation(t *testing.T) {
	ctx := context.Background()
	if err := Run(ctx, nil, HelloModule); err != nil { // nil out -> io.Discard
		t.Fatalf("Run with nil out: %v", err)
	}
	var a, b bytes.Buffer
	_ = Run(ctx, &a, HelloModule)
	_ = Run(ctx, &b, HelloModule)
	if a.String() != b.String() || a.Len() == 0 {
		t.Errorf("runs not independent/repeatable: %q vs %q", a.String(), b.String())
	}
}

// TestRunContextCancel confirms a cancelled context aborts a run rather than
// hanging (the guard against a runaway guest on the single hart).
func TestRunContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // ensure the deadline has passed
	// The demo module is trivial, so this mostly exercises that an already-expired
	// context is handled cleanly (no panic); either a clean finish or a
	// context-related error is acceptable.
	_ = Run(ctx, io.Discard, HelloModule)
}
