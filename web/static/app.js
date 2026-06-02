// Tier 2: boot the real honk.elf inside QEMU compiled to WebAssembly, wired to an
// xterm.js terminal through xterm-pty. Loaded on demand by replay.js.
//
// CONTRACT with the build (web/build-qemu-wasm.sh):
//   - ./vendor/qemu/qemu-system-riscv64.js is an ES6 module (-sEXPORT_ES6=1) whose
//     default export initialises the Emscripten Module.
//   - Its .wasm / .worker.js siblings sit next to it (resolved via locateFile).
//   - The OpenSBI firmware ships as a plain file next to them.
//   - Neither honk.elf nor the firmware is baked into the bundle: we fetch both at
//     runtime and drop them into the in-memory FS, so a kernel rebuild never requires
//     recompiling QEMU, and there is no file_packager/global-Module coupling.

// xterm.js and xterm-pty ship as UMD globals (Terminal, openpty). Load them lazily.
function loadScript(src) {
  return new Promise((resolve, reject) => {
    const s = document.createElement("script");
    s.src = src;
    s.onload = resolve;
    s.onerror = () => reject(new Error("failed to load " + src));
    document.head.appendChild(s);
  });
}

let booted = false;

export async function launchHonk(mountEl, setStatus) {
  if (booted) return;
  booted = true;

  await loadScript("./vendor/xterm.js");
  await loadScript("./vendor/xterm-pty.js");

  const term = new Terminal({
    cols: 80,
    rows: 30,
    convertEol: true,
    fontFamily: 'ui-monospace, "SF Mono", Menlo, Consolas, monospace',
    fontSize: 13,
    theme: { background: "#000000" },
  });
  term.open(mountEl);
  term.focus();

  // xterm-pty bridges the terminal to Emscripten's TTY (Module.pty).
  const { master, slave } = openpty();
  term.loadAddon(master);

  setStatus("fetching kernel + firmware…");
  const qemuDir = new URL("./vendor/qemu/", document.baseURI);
  const [elf, bios] = await Promise.all([
    fetchBytes("./honk.elf"),
    fetchBytes(new URL("opensbi-riscv64-generic-fw_dynamic.bin", qemuDir).href),
  ]);

  setStatus("starting QEMU…");
  const qemuUrl = new URL("qemu-system-riscv64.js", qemuDir);
  const initQemu = (await import(qemuUrl.href)).default;

  const Module = {
    // Mirrors `make run`: qemu-system-riscv64 -machine virt -m 128 -smp 1
    //   -bios <opensbi> -nographic -no-reboot -kernel honk.elf
    arguments: [
      "-machine", "virt",
      "-m", "128",
      "-smp", "1",
      "-bios", "/opensbi.bin",
      "-nographic",
      "-no-reboot",
      "-kernel", "/honk.elf",
      "-accel", "tcg,tb-size=500",
    ],
    pty: slave,
    mainScriptUrlOrBlob: qemuUrl.href,
    // The JS is renamed to .js for ESM import, so tell Emscripten where its
    // .wasm/.worker.js (referenced by their build-time basenames) actually live.
    locateFile: (path) => new URL(path, qemuDir).href,
    preRun: [
      function () {
        // `this` is the Module; FS is attached by the time preRun fires.
        Module.FS.writeFile("/honk.elf", elf);
        Module.FS.writeFile("/opensbi.bin", bios);
      },
    ],
    onExit: () => setStatus("machine powered off — reload to boot again."),
    printErr: (line) => console.warn("[qemu]", line),
  };

  await initQemu(Module);

  // Make the TTY poll non-blocking under ASYNCIFY so keystrokes flow (matches the
  // upstream qemu-wasm sample). TTY/FS are exported by the build's EXPORTED_RUNTIME_METHODS.
  try {
    const oldPoll = Module.TTY.stream_ops.poll;
    Module.TTY.stream_ops.poll = (stream) => oldPoll.call(stream, 0);
  } catch {
    /* older/newer emscripten may not expose TTY.poll; non-fatal */
  }

  setStatus("running — click the terminal and type `help`.");
}

async function fetchBytes(url) {
  const resp = await fetch(url);
  if (!resp.ok) throw new Error(url + " missing (" + resp.status + ")");
  return new Uint8Array(await resp.arrayBuffer());
}
