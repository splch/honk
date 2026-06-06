// honk - the WASM/WASI tier (M7): untrusted, dynamic, any-toolchain code in a
// wazero sandbox (HONK.md §1 isolation tier 2, §2 WASI).
//
// honk embeds wazero in interpreter mode (no JIT on riscv64; the interpreter is
// OS-agnostic, which a compile/link spike confirmed builds on GOOS=tamago). A
// WASM module is a honk process: it runs as a goroutine under a context, so the
// process model's kill (context cancel) terminates even an uncooperative,
// tight-looping module - wazero's WithCloseOnContextDone aborts the interpreter
// when the context is done. This is the answer to "no async preemption of
// trusted goroutines": uncooperative code belongs in this tier, not as a
// trusted goroutine.
//
// Capability discipline (HONK.md §2): honk *implements* the WASI host functions
// once on the runtime, but a module is *granted* nothing by default - its
// access to the console, the filesystem, args, and env is decided per module by
// the wazero ModuleConfig honk builds from the process's honk capabilities.
// Implementing a host function is separate from granting it.

//go:build tamago && riscv64

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"time"

	"github.com/tetratelabs/wazero"
	wasi "github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"

	"honk/kernel/proc"
)

// wasmRuntime is the shared interpreter runtime; nil until InitWASM succeeds.
// wazero runtimes are safe for concurrent module instantiation, so all WASM
// processes share one (the WASI host functions are stateless - per-module state
// lives in each instance and is selected by the ModuleConfig grants).
var wasmRuntime wazero.Runtime

// InitWASM creates the WASM runtime (wazero interpreter) and instantiates the
// WASI Preview 1 host module on it. WithCloseOnContextDone makes a running
// module abort when its process context is cancelled (kill) or times out.
func InitWASM() {
	ctx := context.Background()
	cfg := wazero.NewRuntimeConfigInterpreter().WithCloseOnContextDone(true)
	r := wazero.NewRuntimeWithConfig(ctx, cfg)
	wasi.MustInstantiate(ctx, r) // implement WASI; granting is per-module below
	wasmRuntime = r
	fmt.Println("honk: wasm runtime ready (wazero interpreter, WASI preview 1)")
}

// consoleWriter routes a module's granted stdout/stderr to honk's console.
type consoleWriter struct{}

func (consoleWriter) Write(p []byte) (int, error) {
	fmt.Print(string(p))
	return len(p), nil
}

// runWASM starts a WASI module as a honk process: a goroutine + context (kill
// cancels it). The process's capabilities decide the sandbox grants - console
// grants stdout/stderr, block grants read access to the overlay filesystem -
// so a module with no capabilities can compute but cannot touch the outside.
// It returns the process and a channel closed when the module finishes.
func runWASM(name string, code []byte, caps proc.Caps) (*proc.Proc, <-chan struct{}) {
	done := make(chan struct{})
	p := procs.Spawn(name, caps, func(ctx context.Context) {
		defer close(done)

		cfg := wazero.NewModuleConfig().WithName(name)
		if caps.Has(proc.CapConsole) {
			cfg = cfg.WithStdout(consoleWriter{}).WithStderr(consoleWriter{})
		}
		if caps.Has(proc.CapBlock) && root != nil {
			cfg = cfg.WithFS(root) // grant read-only access to honk's filesystem
		}

		// Instantiation runs the module's _start (a WASI command); the context
		// is the process context, so kill/timeout terminates the interpreter.
		mod, err := wasmRuntime.InstantiateWithConfig(ctx, code, cfg)
		if err != nil {
			var exit *sys.ExitError
			if errors.As(err, &exit) {
				if exit.ExitCode() != 0 {
					fmt.Printf("wasm %s: exit %d\n", name, exit.ExitCode())
				}
				return // proc_exit (incl. 0) is normal WASI command termination
			}
			fmt.Printf("wasm %s: %v\n", name, err)
			return
		}
		_ = mod.Close(ctx)
	})
	return p, done
}

// wasmcmd is the shell's `wasm` command: load a module from the overlay
// filesystem and run it (granted the console, and read access to the
// filesystem). It waits briefly so a short command's output is shown before the
// prompt returns; a long-running module is left in the table, killable by PID.
func wasmcmd(fields []string) {
	if wasmRuntime == nil {
		fmt.Println("wasm: runtime unavailable")
		return
	}
	if len(fields) < 2 {
		fmt.Println("usage: wasm <file.wasm>")
		return
	}
	p, ok := fsPath(fields[1])
	if !ok {
		fmt.Println("wasm: invalid path")
		return
	}
	code, err := fs.ReadFile(root, p)
	if err != nil {
		fmt.Printf("wasm: %v\n", err)
		return
	}

	pr, done := runWASM(path.Base(p), code, proc.Caps{proc.CapConsole: true, proc.CapBlock: true})
	fmt.Printf("wasm: spawned PID %d (%s)\n", pr.PID, pr.Name)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		fmt.Printf("wasm: PID %d still running (kill %d to stop)\n", pr.PID, pr.PID)
	}
}
