const $ = (id) => document.getElementById(id);
const replayEl = $("replay");
const termEl = $("terminal");
const launchBtn = $("launch");
const setStatus = (m) => ($("status").textContent = m);
const qemuDir = new URL("./vendor/qemu/", document.baseURI);

let replayTimer;
async function playReplay() {
  clearInterval(replayTimer);
  replayEl.textContent = "";
  const lines = (await (await fetch("./cast/honk-boot.log")).text()).replace(/\n+$/, "").split("\n");
  let i = 0;
  replayTimer = setInterval(() => {
    if (i >= lines.length) return clearInterval(replayTimer);
    replayEl.append(lines[i++] + "\n");
    replayEl.scrollTop = replayEl.scrollHeight;
  }, 45);
}

const bundleReady = fetch(new URL("qemu-system-riscv64.wasm", qemuDir), { method: "HEAD" })
  .then((r) => r.ok)
  .catch(() => false);

const loadScript = (src) =>
  new Promise((ok, fail) => {
    const s = document.createElement("script");
    s.src = src;
    s.onload = ok;
    s.onerror = () => fail(new Error("load failed: " + src));
    document.head.append(s);
  });

const fetchBytes = async (url) => new Uint8Array(await (await fetch(url)).arrayBuffer());

async function bootHonk() {
  await loadScript("./vendor/xterm.js");
  await loadScript("./vendor/xterm-pty.js");

  const term = new Terminal({ cols: 80, rows: 30, convertEol: true });
  term.open(termEl);
  term.focus();
  window.__honkTerm = term; // read by the e2e test

  const { master, slave } = openpty();
  term.loadAddon(master);

  setStatus("fetching kernel + firmware…");
  const [elf, bios] = await Promise.all([
    fetchBytes("./honk.elf"),
    fetchBytes(new URL("opensbi-riscv64-generic-fw_dynamic.bin", qemuDir).href),
  ]);

  setStatus("starting QEMU…");
  const qemuUrl = new URL("qemu-system-riscv64.js", qemuDir).href;
  const initQemu = (await import(qemuUrl)).default;

  // Mirrors `make run`. honk.elf/firmware are written into the in-memory FS at runtime,
  // so a kernel rebuild never recompiles QEMU; siblings resolve via locateFile.
  const Module = {
    arguments: ["-machine", "virt", "-m", "128", "-smp", "1", "-bios", "/opensbi.bin",
      "-nographic", "-no-reboot", "-kernel", "/honk.elf", "-accel", "tcg,tb-size=500"],
    pty: slave,
    mainScriptUrlOrBlob: qemuUrl,
    locateFile: (p) => new URL(p, qemuDir).href,
    preRun: [() => {
      Module.FS.writeFile("/honk.elf", elf);
      Module.FS.writeFile("/opensbi.bin", bios);
    }],
  };
  await initQemu(Module);

  // Non-blocking TTY poll under ASYNCIFY so keystrokes flow.
  const poll = Module.TTY.stream_ops.poll;
  Module.TTY.stream_ops.poll = (s) => poll.call(s, 0);

  setStatus("running — click the terminal and type `help`.");
}

async function launchLive() {
  launchBtn.disabled = true;
  if (!(await bundleReady)) return setStatus("live emulator not built in this deployment — the replay still works.");
  if (!self.crossOriginIsolated) return setStatus("cross-origin isolation unavailable — the replay still works.");
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

$("replay-again").addEventListener("click", playReplay);
launchBtn.addEventListener("click", launchLive);
playReplay();
bundleReady.then((ok) => ok || (launchBtn.disabled = true));
if (new URLSearchParams(location.search).has("live") && self.crossOriginIsolated) launchLive();
