// End-to-end test of the live emulator: serve site/ with COOP/COEP, open it in a
// headless browser, launch QEMU-WASM, drive the shell, and assert Honk OS responds.
//
// Run (after `make web` and a built web/vendor/qemu bundle):
//   npm i -D @playwright/test && npx playwright install --with-deps chromium
//   npx playwright test web/test/e2e.spec.mjs
//
// This is the authoritative end-to-end check for the browser path; CI runs it after
// building the QEMU-WASM bundle.

import { test, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import { setTimeout as sleep } from "node:timers/promises";

const PORT = 8099;
const BASE = `http://localhost:${PORT}`;
let server;

test.beforeAll(async () => {
  server = spawn("node", ["web/test/serve.mjs", "site", String(PORT)], {
    stdio: "inherit",
  });
  await sleep(700);
});

test.afterAll(() => server?.kill());

test("Honk OS boots and answers in the browser", async ({ page }) => {
  test.setTimeout(180_000); // first WASM instantiation + boot can be slow under TCG.

  const consoleText = [];
  page.on("console", (m) => consoleText.push(m.text()));

  await page.goto(BASE + "/", { waitUntil: "load" });

  // Sanity: cross-origin isolation is in effect (server set the headers).
  expect(await page.evaluate(() => self.crossOriginIsolated)).toBe(true);

  await page.getByRole("button", { name: /launch live emulator/i }).click();
  await expect(page.locator("#terminal")).toBeVisible();

  // Read xterm's buffer directly (renderer-independent) via the exposed Terminal.
  const readTerm = () =>
    page.evaluate(() => {
      const t = window.__honkTerm;
      if (!t) return "";
      const b = t.buffer.active;
      let s = "";
      for (let i = 0; i < b.length; i++) s += (b.getLine(i)?.translateToString(true) ?? "") + "\n";
      return s;
    });

  // Wait for the boot banner, then type a command and check the response.
  await expect.poll(readTerm, { timeout: 150_000 }).toContain("Honk OS");

  await page.keyboard.type("honk\n");
  await expect.poll(readTerm, { timeout: 20_000 }).toContain("HONK!");
});
