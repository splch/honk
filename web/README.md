# Honk OS on the web

Honk OS as a static GitHub Pages site, all client-side. Two tiers:

1. **Replay** — a dependency-free typing animation of a recorded boot
   (`cast/honk-boot.log`). Always works.
2. **Live emulator** — the real `honk.elf` in
   [QEMU-WASM](https://github.com/ktock/qemu-wasm) via
   [xterm.js](https://xtermjs.org/)/[xterm-pty](https://github.com/mame/xterm-pty).
   Opt-in; pulls a multi-MB bundle and needs `SharedArrayBuffer`.

## Layout

```
web/
  static/      index.html, styles.css, app.js (replay + live-emulator glue)
  vendor/      xterm.{js,css}, xterm-pty.js, coi-serviceworker.min.js (committed)
  vendor/qemu/ qemu-system-riscv64.{js,wasm,worker.js} + opensbi…fw_dynamic.bin (built, gitignored)
  cast/        honk-boot.log (recorded boot)
  test/        serve.mjs (COOP/COEP static server), e2e.spec.mjs (Playwright)
  build-qemu-wasm.sh   QEMU-WASM build (Docker + emscripten)
```

`honk.elf` is loaded into the emulator at runtime, so rebuilding the kernel never
recompiles QEMU.

## Local workflow

```sh
make kernel     # build honk.elf
make web-qemu   # one-time: build the QEMU-WASM bundle (Docker, slow)
make web-serve  # assemble ./site and serve at http://localhost:8088
```

Without `make web-qemu`, `make web` still produces a working replay-only site. CI
(`.github/workflows/pages.yml`) builds the kernel, fetches the prebuilt bundle from its
`qemu-wasm-v1` Release (non-blocking — a missing bundle still ships the replay), runs
the e2e when present, and deploys.

## Cross-origin isolation

`SharedArrayBuffer` needs COOP `same-origin` + COEP `require-corp`. Pages can't set
headers, so `coi-serviceworker.min.js`
([gzuidhof/coi-serviceworker](https://github.com/gzuidhof/coi-serviceworker), MIT)
installs them via a service worker (one reload on first visit); all deps are vendored
locally. `test/serve.mjs` sets the headers directly for the e2e.

## Licensing

Honk OS is MIT. The emulator is **QEMU (GPLv2)**, built by `build-qemu-wasm.sh` from
[`ktock/qemu-wasm`](https://github.com/ktock/qemu-wasm); its binaries ship as the
`qemu-wasm-v1` Release asset, not in git. Redistributing them obliges you to point to
that QEMU source.
