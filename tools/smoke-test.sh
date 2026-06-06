#!/usr/bin/env bash
# Build honk and boot it under QEMU, asserting M0-M4 behavior across both block
# backends (NVMe primary, virtio-blk fallback) and verifying kv/fs persistence
# across a reboot. Exits non-zero on any missing line or a boot hang.
set -euo pipefail
cd "$(dirname "$0")/.."

WATCHDOG="${WATCHDOG:-45}"
SMP="${SMP:-4}"

# Host race tests of the pure-Go storage stack (M2 proc, M4 kv + vfs).
echo "== go test -race ./kernel/... ./block =="
go test -race -count=1 ./kernel/proc/ ./kernel/kv/ ./kernel/vfs/ ./block/ ||
	{ echo "SMOKE FAIL: host race tests" >&2; exit 1; }

tools/build.sh >/dev/null

NVME="$(mktemp)" VB="$(mktemp)"
A="$(mktemp)" P="$(mktemp)" B="$(mktemp)"
trap 'rm -f "$NVME" "$VB" "$A" "$P" "$B"' EXIT
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

# Run A: NVMe primary; full M0-M4 exercise; writes land in the kv store.
boot $'\nhelp\nblk\ncat os-release\ncp motd readme\nput config/host honkbox\nls\nrun\ncrash\nstress 16\nexit\n' \
	"$A" "${nvme[@]}"
cat "$A"
want "$A" "NVMe/M0-M4" <<'EOF'
honk: HS-mode boot ok
SMP OK - goroutines ran on
storage = NVMe
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

echo "----------------------------------------"
echo "SMOKE PASS"
