#!/usr/bin/env bash
# Build qemu-system-riscv64 as WebAssembly and stage it under web/vendor/qemu/.
#
# This is the one heavy, infrequently-changing step. It needs Docker + several GB and
# tens of minutes, so it runs on CI runners (cached) rather than every push — the
# kernel image (honk.elf) is loaded into the emulator at runtime, so rebuilding the
# kernel never requires rerunning this.
#
# Output (consumed by web/static/app.js):
#   web/vendor/qemu/qemu-system-riscv64.js          (ES6 module; default export = init)
#   web/vendor/qemu/qemu-system-riscv64.wasm
#   web/vendor/qemu/qemu-system-riscv64.worker.js   (if emitted by this emscripten)
#   web/vendor/qemu/opensbi-riscv64-generic-fw_dynamic.bin
#
# Pin QEMU_WASM_REF to a commit for reproducible builds. Upstreaming into QEMU proper
# is in progress (TCI landed in QEMU 10.1); until then we build from ktock/qemu-wasm.
set -euo pipefail

QEMU_WASM_REPO="${QEMU_WASM_REPO:-https://github.com/ktock/qemu-wasm.git}"
QEMU_WASM_REF="${QEMU_WASM_REF:-master}" # pin to a commit SHA for reproducibility
WORKDIR="${WORKDIR:-$(mktemp -d)}"
OUT="$(cd "$(dirname "$0")/.." && pwd)/web/vendor/qemu"
BUILD_CTR="honk-qemu-wasm-build"

EXTRA_CFLAGS="-O3 -g -Wno-error=unused-command-line-argument -matomics -mbulk-memory \
-DNDEBUG -DG_DISABLE_ASSERT -D_GNU_SOURCE -sASYNCIFY=1 -pthread -sPROXY_TO_PTHREAD=1 \
-sFORCE_FILESYSTEM -sALLOW_TABLE_GROWTH -sTOTAL_MEMORY=2300MB -sWASM_BIGINT \
-sMALLOC=mimalloc --js-library=/build/node_modules/xterm-pty/emscripten-pty.js \
-sEXPORT_ES6=1 -sASYNCIFY_IMPORTS=ffi_call_js"

echo ">> cloning $QEMU_WASM_REPO @ $QEMU_WASM_REF (shallow)"
if [ "$QEMU_WASM_REF" = "master" ]; then
  git clone --depth 1 "$QEMU_WASM_REPO" "$WORKDIR/qemu-wasm"
else
  git clone --depth 1 --no-checkout "$QEMU_WASM_REPO" "$WORKDIR/qemu-wasm"
  git -C "$WORKDIR/qemu-wasm" fetch --depth 1 origin "$QEMU_WASM_REF"
  git -C "$WORKDIR/qemu-wasm" checkout FETCH_HEAD
fi

echo ">> building emscripten build image"
docker build -t honk-buildqemu - <"$WORKDIR/qemu-wasm/Dockerfile"

echo ">> starting build container"
docker rm -f "$BUILD_CTR" 2>/dev/null || true
docker run --rm -d --name "$BUILD_CTR" -v "$WORKDIR/qemu-wasm":/qemu/:ro honk-buildqemu

echo ">> emconfigure + emmake qemu-system-riscv64 (this is the slow part)"
docker exec "$BUILD_CTR" emconfigure /qemu/configure --static \
  --target-list=riscv64-softmmu --cpu=wasm32 --cross-prefix= \
  --without-default-features --enable-system --with-coroutine=fiber --enable-virtfs \
  --extra-cflags="$EXTRA_CFLAGS" --extra-cxxflags="$EXTRA_CFLAGS" \
  --extra-ldflags="-sEXPORTED_RUNTIME_METHODS=getTempRet0,setTempRet0,addFunction,removeFunction,TTY,FS"
docker exec "$BUILD_CTR" emmake make -j"$(nproc 2>/dev/null || echo 4)" qemu-system-riscv64

echo ">> staging artifacts into $OUT"
mkdir -p "$OUT"
# Emscripten names the JS output after the target (no extension); rename to .js so it
# imports as an ES module. app.js uses locateFile to find the siblings below.
docker cp "$BUILD_CTR":/build/qemu-system-riscv64 "$OUT/qemu-system-riscv64.js"
docker cp "$BUILD_CTR":/build/qemu-system-riscv64.wasm "$OUT/qemu-system-riscv64.wasm"
docker cp "$BUILD_CTR":/build/qemu-system-riscv64.worker.js "$OUT/qemu-system-riscv64.worker.js" 2>/dev/null \
  || echo "   (no .worker.js for this emscripten — fine)"
docker cp "$BUILD_CTR":/qemu/pc-bios/opensbi-riscv64-generic-fw_dynamic.bin \
  "$OUT/opensbi-riscv64-generic-fw_dynamic.bin"

docker rm -f "$BUILD_CTR" >/dev/null 2>&1 || true
echo ">> done:"
ls -la "$OUT"
