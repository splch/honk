// Package wasm runs sandboxed WebAssembly modules on the wazero runtime.
//
// honk is a single-address-space unikernel with no hardware isolation between
// tasks (DESIGN.md §1), so trusted code just runs as a goroutine. For
// *untrusted* code the answer is software isolation, not a privilege boundary:
// a WebAssembly module executes in its own linear memory and can only compute
// and write to the io.Writer wired to its WASI stdout/stderr. It cannot read or
// write honk's memory, touch the filesystem or network, or reach any device —
// the sandbox grants nothing it is not explicitly handed (DESIGN.md §15.5/§15.6:
// "host-module capabilities; goroutines stay the default for trusted code").
//
// The package is hardware-independent and builds on the host, so the runner is
// unit-tested with the stock toolchain (GO.md §16) — the same host-testability
// win as the fdt/ring/banner packages. wazero is pure Go with no cgo; on
// riscv64 it has no JIT backend, so modules run on its interpreter (slower, but
// identical semantics), which is why Run forces the interpreter config.
package wasm

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// memoryLimitPages caps a guest's linear memory (WebAssembly pages are 64 KiB)
// so a module that declares or grows to a huge memory cannot exhaust honk's
// single Go heap.
const memoryLimitPages = 1024 // 64 MiB

// Run compiles and executes a WebAssembly command module — one exporting the
// WASI _start entry — in a sandbox, returning when it finishes. The guest's
// WASI stdout and stderr are wired to out, and it is passed args (args[0] is
// conventionally the program name); it is granted no filesystem, no network,
// no stdin, and no environment.
//
// ctx bounds the guest: wazero's interpreter honors cancellation
// (WithCloseOnContextDone), so a runaway module is stopped at the next
// instruction boundary rather than starving honk's single hart (DESIGN.md
// §15.7). A guest that exits non-zero, traps, or is cancelled is reported as an
// error; a clean exit (normal return or proc_exit(0)) returns nil.
func Run(ctx context.Context, out io.Writer, module []byte, args ...string) error {
	if out == nil {
		out = io.Discard
	}
	cfg := wazero.NewRuntimeConfigInterpreter(). // riscv64 has no JIT; be explicit
							WithMemoryLimitPages(memoryLimitPages).
							WithCloseOnContextDone(true) // a ctx deadline can stop a runaway guest
	r := wazero.NewRuntimeWithConfig(ctx, cfg)
	defer r.Close(ctx)

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		return fmt.Errorf("wasm: install WASI: %w", err)
	}

	if len(args) == 0 {
		args = []string{"app"}
	}
	// No WithFSConfig / WithStdin / WithEnv: the sandbox hands the guest nothing
	// beyond stdout/stderr and its argv.
	modCfg := wazero.NewModuleConfig().
		WithStdout(out).
		WithStderr(out).
		WithArgs(args...)

	mod, err := r.InstantiateWithConfig(ctx, module, modCfg)
	if err != nil {
		// A WASI command that calls proc_exit surfaces here as *sys.ExitError;
		// exit 0 is a normal, successful finish, not a failure.
		var ee *sys.ExitError
		if errors.As(err, &ee) {
			if ee.ExitCode() == 0 {
				return nil
			}
			return fmt.Errorf("wasm: exit status %d", ee.ExitCode())
		}
		return fmt.Errorf("wasm: %w", err)
	}
	// _start returned without calling proc_exit: a normal finish.
	return mod.Close(ctx)
}
