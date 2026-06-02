#!/usr/bin/env bash
# Build qemu-system-riscv64 as WebAssembly into web/vendor/qemu/ (consumed by app.js).
# Heavy/infrequent (Docker, tens of minutes), so CI runs it, not every push.
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

# The pinned zlib-$VER.tar.xz 404s (zlib.net keeps only the current release); use the
# GitHub release .tar.gz instead.
echo ">> patching rotted zlib URL in Dockerfile"
sed -i.bak -E \
  -e 's@https://zlib\.net/zlib-\$ZLIB_VERSION\.tar\.xz@https://github.com/madler/zlib/releases/download/v$ZLIB_VERSION/zlib-$ZLIB_VERSION.tar.gz@' \
  -e 's@tar xJC /zlib@tar xzC /zlib@' \
  "$WORKDIR/qemu-wasm/Dockerfile"

echo ">> building emscripten build image"
docker build -t honk-buildqemu - <"$WORKDIR/qemu-wasm/Dockerfile"

echo ">> starting build container"
docker rm -f "$BUILD_CTR" 2>/dev/null || true
# Writable mount (not :ro) so meson can materialise the dtc/libfdt wrap subprojects
# (required for the riscv 'virt' machine) during configure.
docker run --rm -d --name "$BUILD_CTR" -v "$WORKDIR/qemu-wasm":/qemu/ honk-buildqemu

echo ">> emconfigure + emmake qemu-system-riscv64 (this is the slow part)"
docker exec "$BUILD_CTR" emconfigure /qemu/configure --static \
  --target-list=riscv64-softmmu --cpu=wasm32 --cross-prefix= \
  --without-default-features --enable-system --with-coroutine=fiber --enable-virtfs \
  --extra-cflags="$EXTRA_CFLAGS" --extra-cxxflags="$EXTRA_CFLAGS" \
  --extra-ldflags="-sEXPORTED_RUNTIME_METHODS=getTempRet0,setTempRet0,addFunction,removeFunction,TTY,FS"
docker exec "$BUILD_CTR" emmake make -j"$(nproc 2>/dev/null || echo 4)" qemu-system-riscv64

echo ">> staging artifacts into $OUT"
mkdir -p "$OUT"
# Rename the extensionless emscripten output to .js so it imports as an ES module.
docker cp "$BUILD_CTR":/build/qemu-system-riscv64 "$OUT/qemu-system-riscv64.js"
docker cp "$BUILD_CTR":/build/qemu-system-riscv64.wasm "$OUT/qemu-system-riscv64.wasm"
docker cp "$BUILD_CTR":/build/qemu-system-riscv64.worker.js "$OUT/qemu-system-riscv64.worker.js" 2>/dev/null \
  || echo "   (no .worker.js for this emscripten — fine)"
docker cp "$BUILD_CTR":/qemu/pc-bios/opensbi-riscv64-generic-fw_dynamic.bin \
  "$OUT/opensbi-riscv64-generic-fw_dynamic.bin"

docker rm -f "$BUILD_CTR" >/dev/null 2>&1 || true
echo ">> done:"
ls -la "$OUT"
