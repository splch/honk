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

// Deep link: /?live=1 auto-launches once cross-origin isolation is in place.
if (new URLSearchParams(location.search).has("live") && self.crossOriginIsolated) {
  launchLive();
}
