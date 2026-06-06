#!/usr/bin/env bash
# Build honk and boot it under QEMU, asserting the expected M0-M3 output for
# both block backends (NVMe primary, virtio-blk fallback). Exits non-zero on
# any missing line or a boot hang. CI-friendly.
set -euo pipefail
cd "$(dirname "$0")/.."

WATCHDOG="${WATCHDOG:-45}"
SMP="${SMP:-4}"

# Host race test of the process model (M2): the proc package is pure Go.
echo "== go test -race ./kernel/proc =="
go test -race -count=1 ./kernel/proc/ || { echo "SMOKE FAIL: proc race test" >&2; exit 1; }

tools/build.sh >/dev/null

NVME="$(mktemp)"
VB="$(mktemp)"
A="$(mktemp)"
B="$(mktemp)"
trap 'rm -f "$NVME" "$VB" "$A" "$B"' EXIT
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

# Run A: NVMe primary, full M0-M3 shell exercise.
boot $'\nhelp\nblk\nrun\nps\ncrash\nstress 16\nps\nexit\n' "$A" \
	-drive file="$NVME",if=none,id=nvm,format=raw -device nvme,serial=honk,drive=nvm
cat "$A"
want "$A" "NVMe" <<'EOF'
honk: entered main
honk: HS-mode boot ok
SMP up  harts=4  GOMAXPROCS=4
SMP OK - goroutines ran on
shell ready
storage = NVMe
blk: read/write self-test OK
init
kernel survives
ran across
honk: shutting down
EOF

# Run B: virtio-blk fallback (no NVMe attached).
boot $'\nblk\nexit\n' "$B" \
	-drive file="$VB",if=none,id=blk0,format=raw -device virtio-blk-device,drive=blk0
want "$B" "virtio-blk" <<'EOF'
storage = virtio-blk
blk: read/write self-test OK
EOF

echo "----------------------------------------"
echo "SMOKE PASS"
