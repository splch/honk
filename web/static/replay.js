// Tier 1: a zero-dependency replay of a real Honk OS boot, plus the controls that
// hand off to the live QEMU-WASM emulator (Tier 2, loaded on demand from app.js).
//
// The replay always works — no SharedArrayBuffer, no WASM, no cross-origin isolation.
// The live emulator is opt-in because it pulls a multi-MB WASM build and needs
// COOP/COEP (provided by coi-serviceworker.min.js).

const replayEl = document.getElementById("replay");
const termEl = document.getElementById("terminal");
const launchBtn = document.getElementById("launch");
const replayBtn = document.getElementById("replay-again");
const statusEl = document.getElementById("status");

const escapeHtml = (s) =>
  s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

// Render one log line, highlighting the interactive `honk>` prompt.
function renderLine(line) {
  const m = line.match(/^(honk>)(.*)$/);
  if (m) return `<span class="prompt">${escapeHtml(m[1])}</span>${escapeHtml(m[2])}`;
  return escapeHtml(line);
}

// The live emulator's QEMU-WASM bundle is built in CI and is absent from a
// replay-only deployment (the WASM build is non-blocking, so a failed/skipped build
// still ships the site). Probe for it once so the UI never offers a launch that would
// 404 mid-boot. Cached for the session.
let bundleAvailable = null;
async function liveBundleAvailable() {
  if (bundleAvailable !== null) return bundleAvailable;
  try {
    const resp = await fetch("./vendor/qemu/qemu-system-riscv64.wasm", { method: "HEAD" });
    bundleAvailable = resp.ok;
  } catch {
    bundleAvailable = false;
  }
  return bundleAvailable;
}

let replayTimer = null;

async function playReplay() {
  if (replayTimer) {
    clearInterval(replayTimer);
    replayTimer = null;
  }
  replayEl.innerHTML = "";
  let text;
  try {
    const resp = await fetch("./cast/honk-boot.log");
    if (!resp.ok) throw new Error(String(resp.status));
    text = await resp.text();
  } catch (e) {
    replayEl.textContent = "(boot recording unavailable: " + e.message + ")";
    return;
  }
  const lines = text.replace(/\n+$/, "").split("\n");
  let i = 0;
  replayTimer = setInterval(() => {
    if (i >= lines.length) {
      clearInterval(replayTimer);
      replayTimer = null;
      return;
    }
    replayEl.insertAdjacentHTML("beforeend", renderLine(lines[i]) + "\n");
    replayEl.scrollTop = replayEl.scrollHeight;
    i += 1;
  }, 45);
}

async function launchLive() {
  launchBtn.disabled = true;
  if (!(await liveBundleAvailable())) {
    // Replay-only deployment: the firmware/WASM simply aren't here. Say so plainly
    // instead of letting the fetch 404. Leave the button disabled — nothing to retry
    // until a deploy ships the bundle.
    statusEl.textContent =
      "the live emulator isn't available in this deployment (QEMU-WASM bundle not built) — the replay still works.";
    return;
  }
  if (!self.crossOriginIsolated) {
    // coi-serviceworker reloads once on first visit to install; if we are still not
    // isolated after that, the environment can't grant SharedArrayBuffer.
    statusEl.textContent =
      "cross-origin isolation unavailable — the live emulator needs it; the replay still works.";
    return;
  }
  statusEl.textContent = "loading emulator…";
  if (replayTimer) clearInterval(replayTimer);
  replayEl.hidden = true;
  termEl.hidden = false;
  try {
    const { launchHonk } = await import("./app.js");
    await launchHonk(termEl, (msg) => (statusEl.textContent = msg));
  } catch (e) {
    statusEl.textContent = "failed to start emulator: " + e.message;
    termEl.hidden = true;
    replayEl.hidden = false;
    launchBtn.disabled = false;
  }
}

replayBtn.addEventListener("click", playReplay);
launchBtn.addEventListener("click", launchLive);

playReplay();

// Reflect bundle availability in the control up front, so a replay-only deployment
// shows the launch as unavailable instead of advertising a boot it can't deliver.
liveBundleAvailable().then((ok) => {
  if (!ok) {
    launchBtn.disabled = true;
    launchBtn.title = "Live emulator not built in this deployment — the replay still works.";
  }
});

// Deep link: /?live=1 auto-launches once cross-origin isolation is in place.
if (new URLSearchParams(location.search).has("live") && self.crossOriginIsolated) {
  launchLive();
}
