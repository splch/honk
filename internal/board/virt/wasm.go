//go:build tamago && riscv64

package virt

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/splch/honk/internal/wasm"
)

// wasmTimeout bounds a sandboxed app. honk runs on a single hart with no async
// preemption, so a CPU-bound guest could otherwise starve the shell, the net
// poller, and the timer (DESIGN.md §15.7). wazero's interpreter honors context
// cancellation (WithCloseOnContextDone in internal/wasm), so the deadline stops
// a runaway module at the next instruction boundary.
const wasmTimeout = 30 * time.Second

// runApp loads a WebAssembly module from the disk and runs it in the wazero
// sandbox, streaming its WASI stdout/stderr to w (translating bare \n to the
// terminal's \r\n). The argument is "<file> [args...]"; the file is resolved at
// the disk root and args are passed to the guest. Backs the shell `run`
// command. The module gets no access to honk's memory, disk, or network — only
// what the sandbox hands it (DESIGN.md §15.5).
func runApp(w io.Writer, arg string) {
	name, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	if name == "" {
		io.WriteString(w, "usage: run <file.wasm> [args...]\r\n")
		return
	}
	module, err := ReadFile(name)
	if err != nil {
		io.WriteString(w, "no such file\r\n")
		return
	}
	args := append([]string{name}, strings.Fields(rest)...)

	ctx, cancel := context.WithTimeout(context.Background(), wasmTimeout)
	defer cancel()
	if err := wasm.Run(ctx, &crlfWriter{w}, module, args...); err != nil {
		fmt.Fprintf(w, "run: %v\r\n", err)
	}
}

// crlfWriter translates a guest's bare newlines to CRLF for a raw terminal,
// matching writeText (console.go) for streamed sandbox output.
type crlfWriter struct{ w io.Writer }

func (c *crlfWriter) Write(p []byte) (int, error) {
	start := 0
	for i, b := range p {
		if b != '\n' {
			continue
		}
		if _, err := c.w.Write(p[start:i]); err != nil {
			return 0, err
		}
		if _, err := io.WriteString(c.w, "\r\n"); err != nil {
			return 0, err
		}
		start = i + 1
	}
	if start < len(p) {
		if _, err := c.w.Write(p[start:]); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
