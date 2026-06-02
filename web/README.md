# Honk OS on the web

Honk OS as a static GitHub Pages site, all client-side. Two tiers:

1. **Replay** — a dependency-free typing animation of a recorded boot. Always works.
2. **Live emulator** — the real `honk.elf` in QEMU-WASM (via xterm.js/xterm-pty).
   Opt-in; pulls a multi-MB bundle and needs `SharedArrayBuffer`.

`honk.elf` is loaded into the emulator at runtime, so rebuilding the kernel never
recompiles QEMU.

## Layout

- `static/` — the site (HTML, CSS, `app.js`).
- `vendor/` — committed front-end deps; `vendor/qemu/` holds the built, gitignored
  QEMU-WASM bundle.
- `cast/` — the recorded boot.
- `test/` — `serve.mjs` (sets COOP/COEP), `e2e.spec.mjs` (Playwright).
- `build-qemu-wasm.sh` — builds the emulator (Docker + emscripten).

## Local workflow

```sh
make kernel     # build honk.elf
make web-qemu   # one-time: build the QEMU-WASM bundle (Docker, slow)
make web-serve  # assemble ./site and serve it locally
```

Without `make web-qemu`, `make web` still produces a working replay-only site. CI builds
the kernel, fetches the prebuilt bundle from a GitHub Release (non-blocking — a missing
bundle still ships the replay), runs the e2e, and deploys.

## Cross-origin isolation

`SharedArrayBuffer` needs COOP `same-origin` + COEP `require-corp`. GitHub Pages can't
set headers, so coi-serviceworker installs them via a service worker (one reload on first
visit); all deps are vendored locally. `test/serve.mjs` sets the headers directly for the
e2e.

## Licensing

Honk OS is MIT. The emulator is **QEMU (GPLv2)**, built by `build-qemu-wasm.sh` from
ktock/qemu-wasm; its binaries ship as a GitHub Release asset, not in git. Redistributing
them obliges you to provide that QEMU source.
