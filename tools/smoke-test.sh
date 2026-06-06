#!/usr/bin/env bash
# Build honk and boot it under QEMU, asserting the expected M0-M2 output.
# Exits non-zero on any missing line or on a boot hang. CI-friendly.
set -euo pipefail
cd "$(dirname "$0")/.."

WATCHDOG="${WATCHDOG:-45}"
SMP="${SMP:-4}"

# Host race test of the process model (M2): the proc package is pure Go.
echo "== go test -race ./kernel/proc =="
go test -race -count=1 ./kernel/proc/ || { echo "SMOKE FAIL: proc race test" >&2; exit 1; }

tools/build.sh >/dev/null

# Backing store for the virtio-blk device (M3).
DISK="$(mktemp)"
dd if=/dev/zero of="$DISK" bs=1048576 count=16 2>/dev/null
trap 'rm -f "$DISK"' EXIT

# Drive the shell over the UART. The leading newline absorbs a startup byte
# that OpenSBI may consume during its own UART init (interactive use, where the
# user types after the prompt, is unaffected). QEMU installs its own SIGALRM
# handler, so the watchdog must SIGKILL the PID, not rely on alarm()/timeout.
out_file="$(mktemp)"
printf '\nhelp\nblk\nrun\nps\ncrash\nstress 16\nps\nexit\n' | \
	qemu-system-riscv64 -machine virt -global virtio-mmio.force-legacy=false \
		-cpu rv64,h=true -smp "$SMP" -m 512M \
		-nographic -bios default -no-reboot \
		-kernel boot.bin -device loader,file=honk.elf \
		-drive file="$DISK",if=none,id=blk0,format=raw \
		-device virtio-blk-device,drive=blk0 >"$out_file" 2>&1 &
qpid=$!
( sleep "$WATCHDOG"; kill -9 "$qpid" 2>/dev/null ) &
wpid=$!
wait "$qpid" 2>/dev/null || true
kill "$wpid" 2>/dev/null || true
out="$(cat "$out_file")"
rm -f "$out_file"

echo "$out"
echo "----------------------------------------"

fail=0
while IFS= read -r want; do
	if ! grep -qF -- "$want" <<<"$out"; then
		echo "SMOKE FAIL: missing line: $want" >&2
		fail=1
	fi
done <<'EOF'
honk: entered main
honk: HS-mode boot ok
SMP up  harts=4  GOMAXPROCS=4
SMP OK - goroutines ran on
shell ready
blk: read/write self-test OK
spawned PID 2
init
kernel survives
ran across
honk: shutting down
EOF

if [ "$fail" -ne 0 ]; then
	echo "SMOKE FAIL" >&2
	exit 1
fi
echo "SMOKE PASS"
