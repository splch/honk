// Two tiers, one module:
//   1. A zero-dependency replay of a real Honk OS boot (cast/honk-boot.log) that
//      always works — no WASM, no SharedArrayBuffer, no cross-origin isolation.
//   2. The live emulator: the real honk.elf booted in QEMU-WASM, wired to xterm.js
//      via xterm-pty. Opt-in, since it pulls a multi-MB bundle and needs COOP/COEP
//      (provided on GitHub Pages by coi-serviceworker.min.js).

const replayEl = document.getElementById("replay");
const termEl = document.getElementById("terminal");
const launchBtn = document.getElementById("launch");
const replayBtn = document.getElementById("replay-again");
const statusEl = document.getElementById("status");
const setStatus = (msg) => (statusEl.textContent = msg);

const qemuDir = new URL("./vendor/qemu/", document.baseURI);

// --- Tier 1: recorded boot replay -----------------------------------------

const escapeHtml = (s) =>
  s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

// Render one log line, highlighting the interactive `honk>` prompt.
const renderLine = (line) => {
  const m = line.match(/^(honk>)(.*)$/);
  return m ? `<span class="prompt">${escapeHtml(m[1])}</span>${escapeHtml(m[2])}` : escapeHtml(line);
};

let replayTimer = null;

async function playReplay() {
  clearInterval(replayTimer);
  replayEl.innerHTML = "";
  let lines;
  try {
    const resp = await fetch("./cast/honk-boot.log");
    if (!resp.ok) throw new Error(String(resp.status));
    lines = (await resp.text()).replace(/\n+$/, "").split("\n");
  } catch (e) {
    replayEl.textContent = "(boot recording unavailable: " + e.message + ")";
    return;
  }
  let i = 0;
  replayTimer = setInterval(() => {
    if (i >= lines.length) return clearInterval(replayTimer);
    replayEl.insertAdjacentHTML("beforeend", renderLine(lines[i++]) + "\n");
    replayEl.scrollTop = replayEl.scrollHeight;
  }, 45);
}

// --- Tier 2: live QEMU-WASM emulator --------------------------------------

// The QEMU-WASM bundle is built in CI and absent from a replay-only deployment, so
// probe for it once (cached) to avoid offering a launch that would 404 mid-boot.
let bundleAvailable = null;
async function liveBundleAvailable() {
  if (bundleAvailable === null) {
    bundleAvailable = await fetch(new URL("qemu-system-riscv64.wasm", qemuDir), { method: "HEAD" })
      .then((r) => r.ok)
      .catch(() => false);
  }
  return bundleAvailable;
}

// xterm.js and xterm-pty ship as UMD globals (Terminal, openpty); load them lazily.
const loadScript = (src) =>
  new Promise((resolve, reject) => {
    const s = document.createElement("script");
    s.src = src;
    s.onload = resolve;
    s.onerror = () => reject(new Error("failed to load " + src));
    document.head.appendChild(s);
  });

const fetchBytes = async (url) => {
  const resp = await fetch(url);
  if (!resp.ok) throw new Error(url + " missing (" + resp.status + ")");
  return new Uint8Array(await resp.arrayBuffer());
};

// Neither honk.elf nor the firmware is baked into the bundle: we fetch both at runtime
// and drop them into the in-memory FS, so a kernel rebuild never requires recompiling
// QEMU. The .wasm/.worker.js siblings resolve next to the renamed .js via locateFile.
async function bootHonk() {
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
  term.open(termEl);
  term.focus();
  window.__honkTerm = term; // exposed so the e2e test can read the terminal buffer

  const { master, slave } = openpty(); // bridges the terminal to Emscripten's TTY
  term.loadAddon(master);

  setStatus("fetching kernel + firmware…");
  const [elf, bios] = await Promise.all([
    fetchBytes("./honk.elf"),
    fetchBytes(new URL("opensbi-riscv64-generic-fw_dynamic.bin", qemuDir).href),
  ]);

  setStatus("starting QEMU…");
  const qemuUrl = new URL("qemu-system-riscv64.js", qemuDir).href;
  const initQemu = (await import(qemuUrl)).default;

  const Module = {
    // Mirrors `make run`: -machine virt -m 128 -smp 1 -bios <opensbi> -nographic
    //   -no-reboot -kernel honk.elf
    arguments: [
      "-machine", "virt", "-m", "128", "-smp", "1",
      "-bios", "/opensbi.bin", "-nographic", "-no-reboot",
      "-kernel", "/honk.elf", "-accel", "tcg,tb-size=500",
    ],
    pty: slave,
    mainScriptUrlOrBlob: qemuUrl,
    locateFile: (path) => new URL(path, qemuDir).href,
    preRun: [() => {
      Module.FS.writeFile("/honk.elf", elf);
      Module.FS.writeFile("/opensbi.bin", bios);
    }],
    onExit: () => setStatus("machine powered off — reload to boot again."),
    printErr: (line) => console.warn("[qemu]", line),
  };

  await initQemu(Module);

  // Make the TTY poll non-blocking under ASYNCIFY so keystrokes flow (matches the
  // upstream qemu-wasm sample); older/newer emscripten may not expose TTY.poll.
  try {
    const oldPoll = Module.TTY.stream_ops.poll;
    Module.TTY.stream_ops.poll = (stream) => oldPoll.call(stream, 0);
  } catch { /* non-fatal */ }

  setStatus("running — click the terminal and type `help`.");
}

let booting = false;
async function launchLive() {
  if (booting) return;
  launchBtn.disabled = true;
  if (!(await liveBundleAvailable())) {
    setStatus("the live emulator isn't available in this deployment (QEMU-WASM bundle not built) — the replay still works.");
    return;
  }
  if (!self.crossOriginIsolated) {
    // coi-serviceworker reloads once on first visit to install; still not isolated
    // means the environment can't grant SharedArrayBuffer.
    setStatus("cross-origin isolation unavailable — the live emulator needs it; the replay still works.");
    return;
  }
  booting = true;
  setStatus("loading emulator…");
  clearInterval(replayTimer);
  replayEl.hidden = true;
  termEl.hidden = false;
  try {
    await bootHonk();
  } catch (e) {
    setStatus("failed to start emulator: " + e.message);
    termEl.hidden = true;
    replayEl.hidden = false;
    launchBtn.disabled = false;
    booting = false;
  }
}

// --- wire-up --------------------------------------------------------------

replayBtn.addEventListener("click", playReplay);
launchBtn.addEventListener("click", launchLive);
playReplay();

// Reflect bundle availability up front so a replay-only deployment doesn't advertise
// a boot it can't deliver.
liveBundleAvailable().then((ok) => {
  if (!ok) {
    launchBtn.disabled = true;
    launchBtn.title = "Live emulator not built in this deployment — the replay still works.";
  }
});

// Deep link: /?live=1 auto-launches once cross-origin isolation is in place.
if (new URLSearchParams(location.search).has("live") && self.crossOriginIsolated) launchLive();
