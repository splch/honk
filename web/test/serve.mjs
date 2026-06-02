// Static server with the COOP/COEP headers QEMU-WASM (SharedArrayBuffer) needs, so a
// headless browser is crossOriginIsolated without the coi-serviceworker Pages relies on.
//   node web/test/serve.mjs [dir=site] [port=8088]
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";

const dir = process.argv[2] || "site";
const port = Number(process.argv[3] || 8088);
const MIME = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".wasm": "application/wasm",
};

createServer(async (req, res) => {
  try {
    let rel = normalize(decodeURIComponent(new URL(req.url, "http://x").pathname)).replace(/^(\.\.[/\\])+/, "");
    if (rel.endsWith("/") || rel === "") rel += "index.html";
    const body = await readFile(join(dir, rel));
    res.setHeader("Cross-Origin-Opener-Policy", "same-origin");
    res.setHeader("Cross-Origin-Embedder-Policy", "require-corp");
    res.setHeader("Cross-Origin-Resource-Policy", "same-origin");
    res.setHeader("Content-Type", MIME[extname(rel)] || "application/octet-stream");
    res.end(body);
  } catch {
    res.statusCode = 404;
    res.end("not found");
  }
}).listen(port, () => console.log(`serving ${dir} on http://localhost:${port}`));
