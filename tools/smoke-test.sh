#!/usr/bin/env bash
# Build honk and boot it under QEMU, asserting M0-M9 behavior across both block
# backends (NVMe primary, virtio-blk fallback): SMP + console + process model,
# block I/O, the kv store + overlay filesystem with reboot persistence, the
# immutable signed core image (verification, A/B selection + fallback), the
# stateless reset, networking (virtio-net + gVisor + net/http), the WASM/WASI
# tier, host files over 9p, the virtio-gpu framebuffer (a test pattern captured
# from the host via QMP screendump), and GUI + input (virtio-input events
# injected over QMP, dispatched into the image/draw toolkit). Exits non-zero on
# any missing line or a hang.
set -euo pipefail
cd "$(dirname "$0")/.."

WATCHDOG="${WATCHDOG:-45}"
SMP="${SMP:-4}"

# Host race tests of the pure-Go stack (M1 console ring, M2 proc, M4 kv + vfs,
# M5 image verity).
echo "== go test -race ./kernel/... ./board/virt/ring ./block =="
go test -race -count=1 ./kernel/proc/ ./board/virt/ring/ ./kernel/kv/ ./kernel/vfs/ ./kernel/image/ ./kernel/p9/ ./kernel/vmm/ ./block/ ||
	{ echo "SMOKE FAIL: host race tests" >&2; exit 1; }

tools/build.sh >/dev/null

# Build two extra signed images (newer + older) for the A/B selection test.
AIMG="$(mktemp)" BIMG="$(mktemp)"
env -u GOOS -u GOARCH -u GOOSPKG go run ./tools/mkimage -version 2 kernel/core "$AIMG" >/dev/null
env -u GOOS -u GOARCH -u GOOSPKG go run ./tools/mkimage -version 1 kernel/core "$BIMG" >/dev/null

NVME="$(mktemp)" RNVME="$(mktemp)" VB="$(mktemp)" AB="$(mktemp)"
A="$(mktemp)" P="$(mktemp)" B="$(mktemp)" R="$(mktemp)" S1="$(mktemp)" S2="$(mktemp)" N="$(mktemp)"
H="$(mktemp)" HNVME="$(mktemp)" HSHARE="$(mktemp -d)" VV="$(mktemp)"
G="$(mktemp)" GSER="$(mktemp)" GNVME="$(mktemp)" GPUPPM="$(mktemp).ppm" GPUSOCK="$(mktemp -u).qmp"
UOUT="$(mktemp)" USRL="$(mktemp)" UNVME="$(mktemp)" UPPM="$(mktemp).ppm" USOCK="$(mktemp -u).qmp"
trap 'rm -f "$NVME" "$RNVME" "$VB" "$AB" "$A" "$P" "$B" "$R" "$S1" "$S2" "$N" "$AIMG" "$BIMG" "$H" "$HNVME" "$VV" "$G" "$GSER" "$GNVME" "$GPUPPM" "$GPUSOCK" "$UOUT" "$USRL" "$UNVME" "$UPPM" "$USOCK"; rm -rf "$HSHARE"' EXIT
dd if=/dev/zero of="$NVME" bs=1048576 count=16 2>/dev/null
dd if=/dev/zero of="$VB" bs=1048576 count=16 2>/dev/null

# boot INPUT OUTFILE EXTRA-QEMU-ARGS... - SIGKILL watchdog (QEMU ignores
# alarm()/timeout, so a hung guest must be killed by PID).
boot() {
	local input="$1" outf="$2"
	shift 2
	printf '%s' "$input" |
		qemu-system-riscv64 -machine virt -global virtio-mmio.force-legacy=false \
			-cpu rv64,h=true -smp "$SMP" -m 512M -nographic -bios default -no-reboot \
			-kernel boot.bin -device loader,file=honk.elf "$@" >"$outf" 2>&1 &
	local qpid=$!
	(
		sleep "$WATCHDOG"
		kill -9 "$qpid" 2>/dev/null
	) &
	local wpid=$!
	wait "$qpid" 2>/dev/null || true
	kill "$wpid" 2>/dev/null || true
}

# want OUTFILE LABEL <<EOF ...required substrings... EOF
want() {
	local outf="$1" label="$2" fail=0 line
	while IFS= read -r line; do
		grep -qF -- "$line" "$outf" || { echo "SMOKE FAIL ($label): missing: $line" >&2; fail=1; }
	done
	[ "$fail" -eq 0 ] || { cat "$outf"; exit 1; }
}

nvme=(-drive "file=$NVME,if=none,id=nvm,format=raw" -device nvme,serial=honk,drive=nvm)

# Run A: NVMe primary; full M0-M5 exercise; writes land in the kv store.
boot $'\nhelp\nblk\ncat os-release\ncp motd readme\nput config/host honkbox\nls\nrun\ncrash\nstress 16\nwasm hello.wasm\nexit\n' \
	"$A" "${nvme[@]}"
cat "$A"
want "$A" "NVMe/M0-M7" <<'EOF'
honk: HS-mode boot ok
SMP OK - goroutines ran on
storage = NVMe
core verified - embedded factory v1
blk: read/write self-test OK
NAME=honk
cp: motd -> readme
put: wrote config/host
readme
kernel survives
ran across
wasm runtime ready
honk from wasm
honk: shutting down
EOF

# Run P: reboot on the SAME NVMe disk - kv/fs state must persist.
boot $'\ncat config/host\ncat readme\nexit\n' "$P" "${nvme[@]}"
want "$P" "persistence" <<'EOF'
honkbox
Welcome to honk
EOF

# Run H: host files over 9p (M8). Share a host directory with a top-level and a
# nested file; honk mounts it as an io/fs.FS (its own virtio-9p driver + 9P2000.L
# client) and reads both through the overlay, which merges the host share with
# the embedded core and the writable kv layer. `mount` reports the layers.
echo "hello from the host filesystem" >"$HSHARE/hostfile.txt"
mkdir -p "$HSHARE/hostdir"
echo "nested host content" >"$HSHARE/hostdir/inner.txt"
dd if=/dev/zero of="$HNVME" bs=1048576 count=16 2>/dev/null
boot $'\nmount\nls\ncat hostfile.txt\ncat hostdir/inner.txt\nexit\n' "$H" \
	-drive "file=$HNVME,if=none,id=nvm,format=raw" -device nvme,serial=honk,drive=nvm \
	-fsdev local,id=hostdev,path="$HSHARE",security_model=none \
	-device virtio-9p-device,fsdev=hostdev,mount_tag=host
want "$H" "9p host files" <<'EOF'
host share mounted (9p, tag "host")
hostfile.txt
hello from the host filesystem
nested host content
EOF

# Run B: virtio-blk fallback (no NVMe attached).
boot $'\nblk\nexit\n' "$B" \
	-drive "file=$VB,if=none,id=blk0,format=raw" -device virtio-blk-device,drive=blk0
want "$B" "virtio-blk" <<'EOF'
storage = virtio-blk
blk: read/write self-test OK
EOF

# Run V: hypervisor (M11 + M12). M11: launch a VS-mode guest under the
# H-extension (the boot cpu is rv64,h=true); it prints a line via emulated SBI
# console_putchar and halts via SBI shutdown - proving H-ext enable, hgatp
# Sv39x4 G-stage paging, the HS<->VS world switch, and trap-and-emulate. M12:
# `vm timer` runs a guest that probes SBI Base for TIME, installs its own VS
# trap vector, arms an SBI timer, and on each VS-timer interrupt honk injects
# (hvip.VSTIP) prints a '*' and reprograms it, then shuts down after 5 ticks -
# proving SBI Base/TIME emulation, VS-timer interrupt injection, and timer-
# driven preemption. The '*****' appears only if the whole chain works. No
# devices needed; the guest runs from a G-stage-mapped RAM buffer.
boot $'\nvm\nvm timer\nexit\n' "$VV"
want "$VV" "vmm/M11+M12" <<'EOF'
launching a VS-mode guest
hello from a guest VM
guest halted (SBI shutdown)
launching a timer guest
guest ticks: *****
EOF

# Run R: stateless reset clears the writable layer; the immutable core remains.
# (Fresh NVMe disk so it cannot disturb the persistence run above.)
dd if=/dev/zero of="$RNVME" bs=1048576 count=16 2>/dev/null
boot $'\nput state/x hello\ncat state/x\nreset --confirm\ncat state/x\nexit\n' "$R" \
	-drive "file=$RNVME,if=none,id=nvm,format=raw" -device nvme,serial=honk,drive=nvm
want "$R" "reset" <<'EOF'
put: wrote state/x
hello
reset: writable layer cleared
state/x: file does not exist
EOF

# Runs S1/S2: A/B selection + fallback. Seed slot A (1 MiB @ block 0) with the
# newer image and slot B (1 MiB @ 1 MiB) with the older; honk boots the higher
# version, then falls back to the other when the active slot is corrupted.
dd if=/dev/zero of="$AB" bs=1048576 count=16 2>/dev/null
dd if="$AIMG" of="$AB" bs=1048576 seek=0 conv=notrunc 2>/dev/null
dd if="$BIMG" of="$AB" bs=1048576 seek=1 conv=notrunc 2>/dev/null
abnvme=(-drive "file=$AB,if=none,id=nvm,format=raw" -device nvme,serial=honk,drive=nvm)

boot $'\nexit\n' "$S1" "${abnvme[@]}"
want "$S1" "A/B select" <<'EOF'
core verified - slot A v2
EOF

printf '\377' | dd of="$AB" bs=1 seek=0 conv=notrunc 2>/dev/null # corrupt slot A header
boot $'\nexit\n' "$S2" "${abnvme[@]}"
want "$S2" "A/B fallback" <<'EOF'
core verified - slot B v1
EOF

# Run N: networking (M6). Boot with a virtio-net device whose :80 is forwarded
# to a host port; once honk's httpd is up, curl it from the host. This exercises
# honk's virtio-net driver, the gVisor stack, and stdlib net/http end to end -
# hermetic (no external network needed). The guest is left running (no `exit`)
# while we poll, then SIGKILLed.
NETPORT="${NETPORT:-18080}"
NNVME="$(mktemp)"
dd if=/dev/zero of="$NNVME" bs=1048576 count=16 2>/dev/null
printf '\n' |
	qemu-system-riscv64 -machine virt -global virtio-mmio.force-legacy=false \
		-cpu rv64,h=true -smp "$SMP" -m 512M -nographic -bios default -no-reboot \
		-kernel boot.bin -device loader,file=honk.elf \
		-drive "file=$NNVME,if=none,id=nvm,format=raw" -device nvme,serial=honk,drive=nvm \
		-netdev "user,id=n0,hostfwd=tcp::${NETPORT}-:80" -device virtio-net-device,netdev=n0 \
		>"$N" 2>&1 &
nqpid=$!
( sleep "$WATCHDOG"; kill -9 "$nqpid" 2>/dev/null ) &
nwpid=$!
httpbody=""
for i in $(seq 1 12); do
	httpbody="$(curl -sS --max-time 3 "http://localhost:${NETPORT}/" 2>/dev/null || true)"
	printf '%s' "$httpbody" | grep -qF "pure-Go RISC-V64 OS" && break
	sleep 1
done
kill -9 "$nqpid" 2>/dev/null || true
wait "$nqpid" 2>/dev/null || true
kill "$nwpid" 2>/dev/null || true
rm -f "$NNVME"
printf '%s' "$httpbody" | grep -qF "pure-Go RISC-V64 OS" ||
	{ echo "SMOKE FAIL (net): no HTTP 200 body from honk httpd" >&2; cat "$N"; exit 1; }
want "$N" "net" <<'EOF'
storage = NVMe
net up  ip=10.0.2.15/24  gw=10.0.2.2
httpd serving on :80
EOF

# Run G: framebuffer (M9). Boot with a virtio-gpu device and NO display, then
# capture the scanout over QMP and verify the test pattern's pixels reached the
# host framebuffer. This exercises honk's virtio-gpu driver (control queue,
# resource + scanout + flush) and the stdlib image/draw render path end to end.
# QEMU keeps the console surface in memory under -display none, so screendump
# works headless. The channel-distinct quadrants also catch a swapped format.
dd if=/dev/zero of="$GNVME" bs=1048576 count=16 2>/dev/null
qemu-system-riscv64 -machine virt -global virtio-mmio.force-legacy=false \
	-cpu rv64,h=true -smp "$SMP" -m 512M -display none -bios default -no-reboot \
	-kernel boot.bin -device loader,file=honk.elf \
	-drive "file=$GNVME,if=none,id=nvm,format=raw" -device nvme,serial=honk,drive=nvm \
	-device virtio-gpu-device \
	-serial "file:$GSER" -qmp "unix:$GPUSOCK,server,nowait" >/dev/null 2>&1 &
gqpid=$!
( sleep "$WATCHDOG"; kill -9 "$gqpid" 2>/dev/null ) &
gwpid=$!
sleep 7 # let honk boot and bring up the scanout
if command -v python3 >/dev/null 2>&1; then
	python3 tools/screendump.py --check "$GPUSOCK" "$GPUPPM" >"$G" 2>&1 || true
	cat "$G"
fi
kill -9 "$gqpid" 2>/dev/null || true
wait "$gqpid" 2>/dev/null || true
kill "$gwpid" 2>/dev/null || true
# Always assert the driver brought the display up (serial); assert the captured
# pixels too when python3 is available (the stronger, end-to-end proof).
want "$GSER" "gpu driver" <<'EOF'
display = virtio-gpu
display up
EOF
if command -v python3 >/dev/null 2>&1; then
	grep -qF "GPU PATTERN OK" "$G" ||
		{ echo "SMOKE FAIL (gpu): test pattern not verified from screendump" >&2; cat "$GSER"; exit 1; }
else
	echo "SMOKE NOTE (gpu): python3 not found; skipped the screendump pixel check"
fi

# Run U: GUI + input (M10). Boot with a virtio-gpu plus virtio-keyboard and
# virtio-tablet, no display, and inject input over QMP: click the button, focus
# the text field, type "honk". honk decodes the evdev events through its
# virtio-input driver, dispatches them into the image/draw toolkit (focus,
# click, typing), and repaints the framebuffer. We assert honk logged the click
# and the typed text (serial - the robust primary proof) and, when python3 is
# present, that the button's toggled-on green reached the host framebuffer.
dd if=/dev/zero of="$UNVME" bs=1048576 count=16 2>/dev/null
qemu-system-riscv64 -machine virt -global virtio-mmio.force-legacy=false \
	-cpu rv64,h=true -smp "$SMP" -m 512M -display none -bios default -no-reboot \
	-kernel boot.bin -device loader,file=honk.elf \
	-drive "file=$UNVME,if=none,id=nvm,format=raw" -device nvme,serial=honk,drive=nvm \
	-device virtio-gpu-device -device virtio-keyboard-device -device virtio-tablet-device \
	-serial "file:$USRL" -qmp "unix:$USOCK,server,nowait" >/dev/null 2>&1 &
uqpid=$!
( sleep "$WATCHDOG"; kill -9 "$uqpid" 2>/dev/null ) &
uwpid=$!
sleep 7 # let honk boot and bring up the UI
if command -v python3 >/dev/null 2>&1; then
	# button center (40,160,240,220) ~ (140,190); field center (40,80,640,124) ~ (340,102)
	python3 tools/uitest.py "$USOCK" "$UPPM" 140 190 340 102 honk >"$UOUT" 2>&1 || true
	cat "$UOUT"
fi
kill -9 "$uqpid" 2>/dev/null || true
wait "$uqpid" 2>/dev/null || true
kill "$uwpid" 2>/dev/null || true
want "$USRL" "input" <<'EOF'
input = virtio-input
ui up
ui: button clicked (clicks=1)
ui: text="honk"
EOF
if command -v python3 >/dev/null 2>&1; then
	grep -qF "UI CLICK OK" "$UOUT" ||
		{ echo "SMOKE FAIL (ui): click not reflected in the framebuffer" >&2; cat "$USRL"; exit 1; }
else
	echo "SMOKE NOTE (ui): python3 not found; skipped the input-injection check"
fi

echo "----------------------------------------"
echo "SMOKE PASS"
