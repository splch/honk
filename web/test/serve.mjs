// Minimal static server that sets the cross-origin-isolation headers QEMU-WASM needs
// (SharedArrayBuffer). Used by the e2e test and for local preview. On GitHub Pages
// these headers come from coi-serviceworker instead; here we set them directly so a
// headless browser gets `crossOriginIsolated === true` without a service worker.
//
//   node web/test/serve.mjs [dir=site] [port=8088]

import { createServer } from "node:http";
import { readFile, stat } from "node:fs/promises";
import { extname, join, normalize } from "node:path";

const dir = process.argv[2] || "site";
const port = Number(process.argv[3] || 8088);

const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json",
  ".wasm": "application/wasm",
  ".data": "application/octet-stream",
  ".elf": "application/octet-stream",
  ".log": "text/plain; charset=utf-8",
  ".png": "image/png",
  ".svg": "image/svg+xml",
};

const server = createServer(async (req, res) => {
  try {
    const urlPath = decodeURIComponent(new URL(req.url, "http://x").pathname);
    let rel = normalize(urlPath).replace(/^(\.\.[/\\])+/, "");
    if (rel.endsWith("/")) rel += "index.html";
    if (rel === "/" || rel === "") rel = "/index.html";
    const file = join(dir, rel);
    const info = await stat(file);
    const body = await readFile(info.isDirectory() ? join(file, "index.html") : file);
    // Cross-origin isolation for SharedArrayBuffer.
    res.setHeader("Cross-Origin-Opener-Policy", "same-origin");
    res.setHeader("Cross-Origin-Embedder-Policy", "require-corp");
    res.setHeader("Cross-Origin-Resource-Policy", "same-origin");
    res.setHeader("Content-Type", MIME[extname(file)] || "application/octet-stream");
    res.end(body);
  } catch {
    res.statusCode = 404;
    res.end("not found");
  }
});

server.listen(port, () => console.log(`serving ${dir} on http://localhost:${port}`));
