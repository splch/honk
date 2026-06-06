#!/usr/bin/env bash
# Phase A acceptance test: boot honk under QEMU and assert the M0/M1/M2
# foundation is correct - the layer everything else is built on. It exercises
# the hardware-contact behavior that ONLY QEMU can prove (re-deriving the
# formulas on the host would just test the constants against themselves):
#
#   M0  HS-mode boot under OpenSBI; SMP bring-up of every hart as a Go M
#       (GOMAXPROCS = nharts) across -smp 1/4/8, boot-hart-agnostic; clean SBI
#       shutdown; RamSize derived from the DTB across -m 256M..2G.
#   M1  UART RX IRQ -> PLIC -> ring -> channel -> shell (interactive input +
#       line editing); the fatal trap path (EBREAK -> scause/sepc/stval -> halt).
#   M2  the process model live on real SMP: run/ps/kill/crash/reap/stress, the
#       recover() fault domain (a panicking process is reaped, the kernel lives),
#       and capability display.
#
# Phase A does not depend on storage (M3+), so NO block device is attached:
# honk boots on the embedded, verified core image alone. Pure-Go logic (the
# proc state machine, the SPSC console ring) is covered by host race tests
# (`go test -race ./kernel/proc ./board/virt/ring`), which this script runs
# first. Exits non-zero on any missing/forbidden line or a boot hang.
set -euo pipefail
cd "$(dirname "$0")/.."

WATCHDOG="${WATCHDOG:-30}"   # seconds before a hung guest is SIGKILLed
FAILED=0

# Host race tests of the pure-Go Phase A logic (authoritative for concurrency
# correctness the trap path can't reliably reach).
echo "== go test -race ./kernel/proc ./board/virt/ring =="
go test -race -count=1 ./kernel/proc/ ./board/virt/ring/ ||
	{ echo "PHASE-A FAIL: host race tests" >&2; exit 1; }

tools/build.sh >/dev/null

OUT="$(mktemp -d)"
trap 'rm -rf "$OUT"' EXIT

# boot SMP MEM INPUT OUTFILE - run honk with no storage; SIGKILL watchdog
# (QEMU ignores alarm()/timeout, so a hung guest is killed by PID). A hang
# manifests as missing expected output, which the asserts below catch.
boot() {
	local smp="$1" mem="$2" input="$3" outf="$4"
	printf '%s' "$input" |
		qemu-system-riscv64 -machine virt -cpu rv64,h=true \
			-smp "$smp" -m "$mem" -nographic -bios default -no-reboot \
			-kernel boot.bin -device loader,file=honk.elf >"$outf" 2>&1 &
	local qpid=$!
	( sleep "$WATCHDOG"; kill -9 "$qpid" 2>/dev/null ) &
	local wpid=$!
	wait "$qpid" 2>/dev/null || true
	kill "$wpid" 2>/dev/null || true
	wait "$wpid" 2>/dev/null || true
}

# want OUTFILE LABEL <<EOF ...required substrings (one per line)... EOF
want() {
	local outf="$1" label="$2" line
	while IFS= read -r line; do
		[ -z "$line" ] && continue
		grep -qF -- "$line" "$outf" || {
			echo "PHASE-A FAIL ($label): missing: $line" >&2
			FAILED=1
		}
	done
}

# reject OUTFILE LABEL SUBSTRING - assert a substring is ABSENT (e.g. a spurious
# fatal trap, or a panic that escaped the recover() fault domain).
reject() {
	local outf="$1" label="$2" sub="$3"
	if grep -qF -- "$sub" "$outf"; then
		echo "PHASE-A FAIL ($label): forbidden substring present: $sub" >&2
		FAILED=1
	fi
}

show_on_fail() { [ "$FAILED" -eq 0 ] || { echo "--- $1 ---" >&2; cat "$2" >&2; }; }

# ---------------------------------------------------------------------------
# M0: SMP bring-up scales across hart counts; clean shutdown. harts=N proves
# every secondary actually reached its park loop and became a Go M (InitSMP
# only counts a hart after its readyFlag is set). The leading newline absorbs
# the byte OpenSBI's UART init may swallow before honk's console is up.
# ---------------------------------------------------------------------------
for smp in 1 4 8; do
	o="$OUT/m0-smp$smp"
	boot "$smp" 512M $'\nharts\nexit\n' "$o"
	{
		echo "honk: HS-mode boot ok  hart="
		echo "honk: SMP up  harts=$smp  GOMAXPROCS=$smp"
		echo "harts: $smp online  GOMAXPROCS=$smp"
		echo "honk: shutting down"
	} | want "$o" "M0 smp=$smp"
	if [ "$smp" -gt 1 ]; then
		echo "honk: SMP OK - goroutines ran on" | want "$o" "M0 smp=$smp spread"
	else
		echo "honk: SMP single-hart" | want "$o" "M0 smp=1"
	fi
	reject "$o" "M0 smp=$smp" "FATAL"
	show_on_fail "M0 smp=$smp" "$o"
done

# ---------------------------------------------------------------------------
# M0: RamSize is derived from the DTB (RamSize = DTB - RamStart), not hardcoded.
# Reaching `mem` (which reads MemStats and has exercised the heap/GC) across a
# wide range of -m proves the derivation is correct: a wrong arena size would
# fault the allocator long before the prompt.
# ---------------------------------------------------------------------------
for mem in 256M 512M 1G 2G; do
	o="$OUT/m0-mem$mem"
	boot 4 "$mem" $'\nmem\nexit\n' "$o"
	{
		echo "honk: shell ready"
		echo "mem: heap="
		echo "honk: shutting down"
	} | want "$o" "M0 mem=$mem"
	reject "$o" "M0 mem=$mem" "FATAL"
	show_on_fail "M0 mem=$mem" "$o"
done

# ---------------------------------------------------------------------------
# M1: console input path + line editing. Input "ec",DEL,"cho phase-a-ok" edits
# to "echo phase-a-ok" (DEL = 0x7f = \177), proving UART RX IRQ -> PLIC -> ring
# -> channel -> shell AND backspace handling. An unknown command is reported.
# ---------------------------------------------------------------------------
o="$OUT/m1-console"
boot 4 512M $'\nec\177cho phase-a-ok\nbogus-cmd-xyz\nexit\n' "$o"
{
	echo "phase-a-ok"
	echo "unknown command"
	echo "honk: shutting down"
} | want "$o" "M1 console"
reject "$o" "M1 console" "phase-a-okc"   # the deleted 'c' must not survive editing
reject "$o" "M1 console" "FATAL"
show_on_fail "M1 console" "$o"

# ---------------------------------------------------------------------------
# M1: the fatal trap path. `fault` executes EBREAK (breakpoint, scause 3),
# which honk's S-mode trap vector reports and halts on. This proves the trap
# vector, the exception decode (scause/sepc/stval), and the clean SBI poweroff.
# ---------------------------------------------------------------------------
o="$OUT/m1-fault"
boot 4 512M $'\nfault\n' "$o"
{
	echo "fault: raising a supervisor exception"
	echo "honk: FATAL supervisor trap"
	echo "scause=0x0000000000000003"   # breakpoint
	echo "sepc=0x"
	echo "stval=0x"
} | want "$o" "M1 fault"
show_on_fail "M1 fault" "$o"

# ---------------------------------------------------------------------------
# M2: the process model, live on 4 harts. init is PID 1; the first `run` is
# PID 2. The sequence drives every command and, crucially, runs a command AFTER
# `crash`: its output appearing proves the recover() fault domain contained the
# panic (the kernel and shell survived a panicking process).
# ---------------------------------------------------------------------------
o="$OUT/m2"
boot 4 512M $'\nrun\nps\nkill 2\nkill 2\nkill 999\ncrash\necho after-crash-alive\nreap\nstress 8\nexit\n' "$o"
{
	echo "spawned PID 2 (worker)"
	echo "worker"
	echo "console"                       # the worker's capability, shown by ps
	echo "init"
	echo "killed PID 2"
	echo "PID 2 not running"             # second kill of the same pid
	echo "PID 999 not running"           # kill of a missing pid
	echo "crasher"
	echo "kernel survives"
	echo "after-crash-alive"             # <- recover() fault domain held
	echo "reaped"
	echo "8 processes ran across"        # stress spread across harts
	echo "honk: shutting down"
} | want "$o" "M2"
reject "$o" "M2" "FATAL"                 # a panic must not become a fatal trap
show_on_fail "M2" "$o"

echo "----------------------------------------"
if [ "$FAILED" -eq 0 ]; then
	echo "PHASE-A PASS"
else
	echo "PHASE-A FAIL" >&2
	exit 1
fi
