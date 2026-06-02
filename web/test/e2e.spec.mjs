// Serve site/ with COOP/COEP, launch QEMU-WASM in a headless browser, drive the shell,
// and assert Honk OS responds. Needs a built web/vendor/qemu bundle. Run:
//   npm i -D @playwright/test && npx playwright install --with-deps chromium
//   npx playwright test web/test/e2e.spec.mjs
import { test, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import { setTimeout as sleep } from "node:timers/promises";

const PORT = 8099;
const BASE = `http://localhost:${PORT}`;
let server;

test.beforeAll(async () => {
  server = spawn("node", ["web/test/serve.mjs", "site", String(PORT)], { stdio: "inherit" });
  await sleep(700);
});
test.afterAll(() => server?.kill());

test("Honk OS boots and answers in the browser", async ({ page }) => {
  test.setTimeout(180_000); // first WASM instantiation + boot is slow under TCG

  await page.goto(BASE + "/", { waitUntil: "load" });
  expect(await page.evaluate(() => self.crossOriginIsolated)).toBe(true);

  await page.getByRole("button", { name: /launch live emulator/i }).click();
  await expect(page.locator("#terminal")).toBeVisible();

  // Read xterm's buffer directly via the exposed Terminal (renderer-independent).
  const readTerm = () =>
    page.evaluate(() => {
      const b = window.__honkTerm?.buffer.active;
      let s = "";
      for (let i = 0; b && i < b.length; i++) s += (b.getLine(i)?.translateToString(true) ?? "") + "\n";
      return s;
    });

  await expect.poll(readTerm, { timeout: 150_000 }).toContain("Honk OS");
  await page.keyboard.type("honk\n");
  await expect.poll(readTerm, { timeout: 20_000 }).toContain("HONK!");
});
