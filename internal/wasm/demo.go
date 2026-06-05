package wasm

// HelloModule is a tiny WASI command module that writes "honk from wasm!\n" to
// stdout and returns. honk seeds it onto a freshly formatted disk as
// "hello.wasm" so `run hello.wasm` demonstrates the sandbox out of the box.
//
// It is hand-encoded (no wat2wasm in the build path) and validated by the
// package test, which runs it through wazero and checks the output — wazero is
// the oracle for the encoding. The equivalent WebAssembly text is:
//
//	(module
//	  (import "wasi_snapshot_preview1" "fd_write"
//	    (func $fd_write (param i32 i32 i32 i32) (result i32)))
//	  (memory 1)
//	  (export "memory" (memory 0))
//	  (data (i32.const 16) "honk from wasm!\n")   ;; message at [16,32)
//	  (func (export "_start")
//	    (i32.store (i32.const 0) (i32.const 16))   ;; iovec[0].buf = 16
//	    (i32.store (i32.const 4) (i32.const 16))   ;; iovec[0].len = 16
//	    (drop (call $fd_write
//	      (i32.const 1)     ;; fd = stdout
//	      (i32.const 0)     ;; *iovs   = 0
//	      (i32.const 1)     ;; iovs_len = 1
//	      (i32.const 12))))) ;; *nwritten = 12 (between iovec and the message)
var HelloModule = []byte{
	0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // "\0asm", version 1

	// Type section (id 1): [0] (i32 i32 i32 i32)->i32  [1] ()->()
	0x01, 0x0c, 0x02,
	0x60, 0x04, 0x7f, 0x7f, 0x7f, 0x7f, 0x01, 0x7f,
	0x60, 0x00, 0x00,

	// Import section (id 2): wasi_snapshot_preview1.fd_write : type 0
	0x02, 0x23, 0x01,
	0x16, 'w', 'a', 's', 'i', '_', 's', 'n', 'a', 'p', 's', 'h', 'o', 't', '_', 'p', 'r', 'e', 'v', 'i', 'e', 'w', '1',
	0x08, 'f', 'd', '_', 'w', 'r', 'i', 't', 'e',
	0x00, 0x00,

	// Function section (id 3): one function of type 1
	0x03, 0x02, 0x01, 0x01,

	// Memory section (id 5): one memory, min 1 page
	0x05, 0x03, 0x01, 0x00, 0x01,

	// Export section (id 7): "memory" -> mem 0, "_start" -> func 1
	0x07, 0x13, 0x02,
	0x06, 'm', 'e', 'm', 'o', 'r', 'y', 0x02, 0x00,
	0x06, '_', 's', 't', 'a', 'r', 't', 0x00, 0x01,

	// Code section (id 10): body of _start (no locals)
	0x0a, 0x1d, 0x01, 0x1b, 0x00,
	0x41, 0x00, 0x41, 0x10, 0x36, 0x02, 0x00, // i32.store [0]  = 16
	0x41, 0x04, 0x41, 0x10, 0x36, 0x02, 0x00, // i32.store [4]  = 16
	0x41, 0x01, 0x41, 0x00, 0x41, 0x01, 0x41, 0x0c, // fd=1, iovs=0, n=1, nwritten=12
	0x10, 0x00, // call fd_write
	0x1a, // drop
	0x0b, // end

	// Data section (id 11): "honk from wasm!\n" at offset 16
	0x0b, 0x16, 0x01, 0x00, 0x41, 0x10, 0x0b, 0x10,
	'h', 'o', 'n', 'k', ' ', 'f', 'r', 'o', 'm', ' ', 'w', 'a', 's', 'm', '!', '\n',
}
