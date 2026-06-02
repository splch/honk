# Honk OS on the web

Hosts Honk OS as a static site on GitHub Pages. Two tiers:

1. **Recorded boot replay** — a dependency-free typing animation of a real boot
   (`cast/honk-boot.log`). Always works; no WASM, no cross-origin isolation.
2. **Live emulator** — the real `honk.elf` booted in
   [QEMU compiled to WebAssembly](https://github.com/ktock/qemu-wasm), wired to
   [xterm.js](https://xtermjs.org/) via [xterm-pty](https://github.com/mame/xterm-pty).
   Opt-in (click **Launch live emulator**), since it pulls a multi-MB bundle and needs
   `SharedArrayBuffer`.

GitHub Pages serves only static files, so all emulation runs client-side in the
visitor's browser. Nothing executes on a server.

## Layout

```
web/
  static/      index.html, styles.css, replay.js (tier 1), app.js (tier 2 glue)
  vendor/      xterm.js, xterm.css, xterm-pty.js, coi-serviceworker.min.js (committed)
  vendor/qemu/ qemu-system-riscv64.{js,wasm,worker.js} + opensbi…fw_dynamic.bin (built, gitignored)
  cast/        honk-boot.log (recorded boot)
  test/        serve.mjs (COOP/COEP static server), e2e.spec.mjs (Playwright)
  build-qemu-wasm.sh   reproducible emulator build (Docker + emscripten)
```

`make web` assembles `./site` from these plus the freshly built `honk.elf`. The
kernel image is **loaded into the emulator at runtime**, so rebuilding the kernel
never requires recompiling QEMU.

## Local workflow

```sh
make kernel            # build honk.elf (Linux/Docker; see top-level README)
make web-qemu          # one-time: build the QEMU-WASM bundle (Docker, slow)
make web-serve         # assemble ./site and serve at http://localhost:8088
```

Without `make web-qemu`, `make web` still produces a working replay-only site.

## CI/CD (`.github/workflows/pages.yml`)

On push to `main`: build the kernel (reusing the toolchain cache), restore-or-build
the QEMU-WASM bundle (cached on its build-script hash + pinned ref), assemble `site/`,
run the Playwright e2e when the bundle is present, then deploy via `actions/deploy-pages`.
The emulator build is **non-blocking**: if it fails, the replay-only site still ships.
First-time setup: repo **Settings → Pages → Source: GitHub Actions**.

## Cross-origin isolation

The threaded QEMU-WASM build needs `SharedArrayBuffer`, which requires the
`Cross-Origin-Opener-Policy: same-origin` + `Cross-Origin-Embedder-Policy: require-corp`
headers. GitHub Pages can't set headers, so `coi-serviceworker.min.js`
([gzuidhof/coi-serviceworker](https://github.com/gzuidhof/coi-serviceworker), MIT)
installs them via a service worker — it reloads the page once on first visit. All
front-end deps are vendored locally so nothing is blocked under `require-corp`.
(`web/test/serve.mjs` sets the headers directly, so the e2e test needs no service worker.)

## Licensing

Honk OS is MIT. The in-browser emulator is **QEMU (GPLv2)**; its WebAssembly build is
produced by `web/build-qemu-wasm.sh` from
[`ktock/qemu-wasm`](https://github.com/ktock/qemu-wasm) (pin `QEMU_WASM_REF`). The
committed `vendor/qemu/.gitkeep` is just a placeholder; the GPL'd binaries are built in
CI and not committed. Redistributing them obliges you to point to that QEMU source.
