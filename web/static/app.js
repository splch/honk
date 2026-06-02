// Two tiers, one module:
//   1. A zero-dependency replay of a real boot (cast/honk-boot.log) — always works.
//   2. The live emulator: honk.elf in QEMU-WASM via xterm.js/xterm-pty. Opt-in; needs
//      the multi-MB bundle and COOP/COEP (set on Pages by coi-serviceworker.min.js).

const $ = (id) => document.getElementById(id);
const replayEl = $("replay");
const termEl = $("terminal");
const launchBtn = $("launch");
const statusEl = $("status");
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

// The bundle is built in CI and absent from a replay-only deployment; probe once so
// the UI never offers a launch that would 404 mid-boot.
const bundleReady = fetch(new URL("qemu-system-riscv64.wasm", qemuDir), { method: "HEAD" })
  .then((r) => r.ok)
  .catch(() => false);

// xterm.js and xterm-pty ship as UMD globals (Terminal, openpty); load them lazily.
const loadScript = (src) =>
  new Promise((resolve, reject) => {
    const s = Object.assign(document.createElement("script"), {
      src,
      onload: resolve,
      onerror: () => reject(new Error("failed to load " + src)),
    });
    document.head.appendChild(s);
  });

const fetchBytes = async (url) => {
  const resp = await fetch(url);
  if (!resp.ok) throw new Error(url + " missing (" + resp.status + ")");
  return new Uint8Array(await resp.arrayBuffer());
};

// Neither honk.elf nor the firmware is baked into the bundle: we fetch both at runtime
// and write them into the in-memory FS, so a kernel rebuild never recompiles QEMU. The
// .wasm/.worker.js siblings resolve next to the renamed .js via locateFile.
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

async function launchLive() {
  launchBtn.disabled = true; // synchronous, so concurrent launches can't overlap
  if (!(await bundleReady))
    return setStatus("live emulator not built in this deployment — the replay still works.");
  // coi-serviceworker reloads once on first visit to install COOP/COEP; still not
  // isolated means the environment can't grant SharedArrayBuffer.
  if (!self.crossOriginIsolated)
    return setStatus("cross-origin isolation unavailable — the live emulator needs it; the replay still works.");

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
  }
}

// --- wire-up --------------------------------------------------------------

$("replay-again").addEventListener("click", playReplay);
launchBtn.addEventListener("click", launchLive);
playReplay();

// A replay-only deployment shouldn't advertise a boot it can't deliver.
bundleReady.then((ok) => {
  if (!ok) {
    launchBtn.disabled = true;
    launchBtn.title = "Live emulator not built in this deployment — the replay still works.";
  }
});

// Deep link: /?live=1 auto-launches once cross-origin isolation is in place.
if (new URLSearchParams(location.search).has("live") && self.crossOriginIsolated) launchLive();
