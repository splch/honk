#!/usr/bin/env bash
# Run honk under QEMU virt: OpenSBI in M-mode, honk as the HS-mode payload.
#
# OpenSBI's fw_dynamic enters the boot trampoline (boot.bin) at 0x80200000,
# which jumps to honk.elf (loaded by -device loader at its link address).
#
#   MEM   QEMU RAM size   (default 512M; any size works - sized from the DTB)
#   SMP   number of harts (default 4)
#   HTTP  host port forwarded to honk's :80 httpd (default 8080)
#
# Quit an interactive session with Ctrl-A then x.
# With the defaults, `curl http://localhost:8080/` reaches honk's HTTP server.
set -euo pipefail
cd "$(dirname "$0")/.."

{ [ -f boot.bin ] && [ -f honk.elf ]; } || tools/build.sh

# Backing stores (created once, 16 MiB each). honk uses NVMe as the primary
# block device and virtio-blk as the fallback; both are attached so either path
# can be exercised.
NVME="${NVME:-nvme.img}"
DISK="${DISK:-disk.img}"
[ -f "$NVME" ] || dd if=/dev/zero of="$NVME" bs=1048576 count=16 2>/dev/null
[ -f "$DISK" ] || dd if=/dev/zero of="$DISK" bs=1048576 count=16 2>/dev/null

exec qemu-system-riscv64 \
	-machine virt \
	-global virtio-mmio.force-legacy=false \
	-cpu rv64,h=true \
	-smp "${SMP:-4}" \
	-m "${MEM:-512M}" \
	-nographic \
	-bios default \
	-no-reboot \
	-kernel boot.bin \
	-device loader,file=honk.elf \
	-drive file="$NVME",if=none,id=nvm,format=raw \
	-device nvme,serial=honk,drive=nvm \
	-drive file="$DISK",if=none,id=blk0,format=raw \
	-device virtio-blk-device,drive=blk0 \
	-netdev "user,id=n0,hostfwd=tcp::${HTTP:-8080}-:80" \
	-device virtio-net-device,netdev=n0
