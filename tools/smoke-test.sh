#!/usr/bin/env bash
# Build honk and boot it under QEMU, asserting the expected M0 output.
# Exits non-zero on any missing line or on a boot hang (watchdog). CI-friendly.
set -euo pipefail
cd "$(dirname "$0")/.."

WATCHDOG="${WATCHDOG:-30}"

tools/build.sh >/dev/null

SMP="${SMP:-4}"
out="$(perl -e 'alarm shift; exec @ARGV' "$WATCHDOG" \
	qemu-system-riscv64 -machine virt -cpu rv64,h=true -smp "$SMP" -m 512M \
	-nographic -bios default -no-reboot \
	-kernel boot.bin -device loader,file=honk.elf 2>&1 || true)"

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
goroutine+channel round-trip
M0 ok - clean shutdown
EOF

if [ "$fail" -ne 0 ]; then
	echo "SMOKE FAIL" >&2
	exit 1
fi
echo "SMOKE PASS"
