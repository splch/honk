#!/usr/bin/env bash
# Build honk and boot it under QEMU, asserting M0-M5 behavior across both block
# backends (NVMe primary, virtio-blk fallback): SMP + console + process model,
# block I/O, the kv store + overlay filesystem with reboot persistence, the
# immutable signed core image (verification, A/B selection + fallback), and the
# stateless reset. Exits non-zero on any missing line or a boot hang.
set -euo pipefail
cd "$(dirname "$0")/.."

WATCHDOG="${WATCHDOG:-45}"
SMP="${SMP:-4}"

# Host race tests of the pure-Go stack (M2 proc, M4 kv + vfs, M5 image verity).
echo "== go test -race ./kernel/... ./block =="
go test -race -count=1 ./kernel/proc/ ./kernel/kv/ ./kernel/vfs/ ./kernel/image/ ./block/ ||
	{ echo "SMOKE FAIL: host race tests" >&2; exit 1; }

tools/build.sh >/dev/null

# Build two extra signed images (newer + older) for the A/B selection test.
AIMG="$(mktemp)" BIMG="$(mktemp)"
env -u GOOS -u GOARCH -u GOOSPKG go run ./tools/mkimage -version 2 kernel/core "$AIMG" >/dev/null
env -u GOOS -u GOARCH -u GOOSPKG go run ./tools/mkimage -version 1 kernel/core "$BIMG" >/dev/null

NVME="$(mktemp)" RNVME="$(mktemp)" VB="$(mktemp)" AB="$(mktemp)"
A="$(mktemp)" P="$(mktemp)" B="$(mktemp)" R="$(mktemp)" S1="$(mktemp)" S2="$(mktemp)"
trap 'rm -f "$NVME" "$RNVME" "$VB" "$AB" "$A" "$P" "$B" "$R" "$S1" "$S2" "$AIMG" "$BIMG"' EXIT
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
boot $'\nhelp\nblk\ncat os-release\ncp motd readme\nput config/host honkbox\nls\nrun\ncrash\nstress 16\nexit\n' \
	"$A" "${nvme[@]}"
cat "$A"
want "$A" "NVMe/M0-M5" <<'EOF'
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
honk: shutting down
EOF

# Run P: reboot on the SAME NVMe disk - kv/fs state must persist.
boot $'\ncat config/host\ncat readme\nexit\n' "$P" "${nvme[@]}"
want "$P" "persistence" <<'EOF'
honkbox
Welcome to honk
EOF

# Run B: virtio-blk fallback (no NVMe attached).
boot $'\nblk\nexit\n' "$B" \
	-drive "file=$VB,if=none,id=blk0,format=raw" -device virtio-blk-device,drive=blk0
want "$B" "virtio-blk" <<'EOF'
storage = virtio-blk
blk: read/write self-test OK
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

echo "----------------------------------------"
echo "SMOKE PASS"
